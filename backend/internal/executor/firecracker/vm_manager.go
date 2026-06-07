package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type VMState string

const (
	VMStateCreated   VMState = "created"
	VMStateBooting   VMState = "booting"
	VMStateRunning   VMState = "running"
	VMStateStopped   VMState = "stopped"
	VMStateDestroyed VMState = "destroyed"
)

type VMConfig struct {
	VCPUCount  int
	MemSizeMiB int
	Timeout    time.Duration
	KernelPath string
	RootfsPath string
	InitrdPath string // initramfs for OverlayFS setup; empty = no initramfs
	BootArgs   string
	Pro        bool // tier of the pool that owns this VM; drives provisioning priority
}

type VsockConfig struct {
	GuestCID int    `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

type MicroVM struct {
	ID         string
	Config     VMConfig
	State      VMState
	VsockPath  string
	SocketPath string
	TapName    string // host TAP device name, empty if network setup failed
	Slot       int    // network slot index (>= 0), or -1 for cold-boot VMs
	CreatedAt  time.Time
	Process    *exec.Cmd
	Stdout     *bytes.Buffer
	Stderr     *bytes.Buffer
}

type NetworkInterface struct {
	IfaceID     string `json:"iface_id"`
	GuestMAC    string `json:"guest_mac"`
	HostDevName string `json:"host_dev_name"`
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
	InitrdPath      string `json:"initrd_path,omitempty"`
}

type Drive struct {
	DriveId      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type MachineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMib int `json:"mem_size_mib"`
}

type FirecrackerConfig struct {
	BootSource    BootSource    `json:"boot-source"`
	Drives        []Drive       `json:"drives"`
	MachineConfig MachineConfig `json:"machine-config"`
}

type SnapshotTemplate struct {
	SnapPath   string // VM + device state file
	MemPath    string // guest RAM file
	RootfsPath string // kept so restored VMs can clone a fresh copy
	VsockPath  string // vsock UDS path baked into snapshot — renamed after each restore
	TapName    string // host TAP name baked into snapshot — recreated before each restore
}

type SnapshotCreateRequest struct {
	SnapshotType string `json:"snapshot_type"` // "Full"
	SnapshotPath string `json:"snapshot_path"`
	MemFilePath  string `json:"mem_file_path"`
}

type SnapshotLoadRequest struct {
	SnapshotPath        string `json:"snapshot_path"`
	MemFilePath         string `json:"mem_file_path"`
	EnableDiffSnapshots bool   `json:"enable_diff_snapshots"`
	ResumeVM            bool   `json:"resume_vm"`
}

type VMStateChange struct {
	State string `json:"state"`
}

// SlotPool manages pre-created network slots (netns + TAP + veth + iptables)
// set up by server.sh. Each slot is a self-contained network environment that
// a restored VM can be placed into without any guest-side reconfiguration.
type SlotPool struct {
	slots chan int
}

func newSlotPool(count int) *SlotPool {
	sp := &SlotPool{slots: make(chan int, count)}
	for i := 0; i < count; i++ {
		sp.slots <- i
	}
	return sp
}

func (s *SlotPool) Acquire(ctx context.Context) (int, error) {
	select {
	case slot := <-s.slots:
		return slot, nil
	case <-ctx.Done():
		return -1, fmt.Errorf("context cancelled waiting for network slot: %w", ctx.Err())
	}
}

func (s *SlotPool) Release(slot int) {
	s.slots <- slot
}

type FireCrackerManager struct {
	SocketDir  string
	AssetsPath string
	BinaryPath string
	Vms        map[string]*MicroVM
	mu         sync.RWMutex
	nextCID    atomic.Uint32
	nextNetIdx atomic.Uint32 // for TAP subnet allocation used by cold-boot VMs
	restoreMu  sync.Mutex    // serializes snapshot restores — prevents vsock path conflicts
	slotPool   *SlotPool     // pre-created network slots from server.sh
	// Provisioning admission gate. There is ONE total budget because the 4 pools share
	// one physical CPU; provisioning (Firecracker spawn + memory page-in + vCPU resume)
	// is CPU-heavy, and too many in parallel saturate the host and push the API-socket
	// bring-up past its 5s wait, forcing cold-boot fallbacks. The budget is split so Pro
	// is never starved by a Free burst: proReserved is Pro-only; sharedProvision is used
	// by Free and by Pro overflow. Buffered chan = counting semaphore (send=acquire).
	proReserved     chan struct{}
	sharedProvision chan struct{}
}

type VMManager interface {
	Create(ctx context.Context, cfg VMConfig) (*MicroVM, error)
	Boot(ctx context.Context, vmID string) error
	Stop(ctx context.Context, vmID string) error
	Destroy(ctx context.Context, vmID string) error
	Snapshot(ctx context.Context, vmID string, snapPath, memPath string) error
	LoadFromSnapshot(ctx context.Context, cfg VMConfig, tmpl *SnapshotTemplate) (*MicroVM, error)
}

func NewFirecrackerManager(socketDir, assetsPath, binaryPath string, slotCount, maxConcurrentProvisions, proReserved int) *FireCrackerManager {
	if maxConcurrentProvisions < 1 {
		maxConcurrentProvisions = 1
	}
	// Clamp the Pro reserve so at least one shared slot always remains for Free.
	if proReserved < 0 {
		proReserved = 0
	}
	if proReserved > maxConcurrentProvisions-1 {
		proReserved = maxConcurrentProvisions - 1
	}
	shared := maxConcurrentProvisions - proReserved
	return &FireCrackerManager{
		SocketDir:       socketDir,
		AssetsPath:      assetsPath,
		BinaryPath:      binaryPath,
		Vms:             make(map[string]*MicroVM),
		slotPool:        newSlotPool(slotCount),
		proReserved:     make(chan struct{}, proReserved),
		sharedProvision: make(chan struct{}, shared),
	}
}

// acquireProvision is the tier-aware admission gate. One total budget (host CPU is
// shared) split into a Pro-only reserve plus a shared pool. Pro prefers its reserve
// and falls back to shared; Free uses only the shared pool. Both wait up to ctx's
// deadline. Returns whether a reserved slot was taken so it's released to the right
// pool. Must be paired with a deferred releaseProvision(reserved).
func (f *FireCrackerManager) acquireProvision(ctx context.Context, pro bool) (reserved bool, err error) {
	if pro {
		// Fast path: take a reserved slot if one is free right now.
		select {
		case f.proReserved <- struct{}{}:
			return true, nil
		default:
		}
		// Otherwise wait for either a reserved or a shared slot.
		select {
		case f.proReserved <- struct{}{}:
			return true, nil
		case f.sharedProvision <- struct{}{}:
			return false, nil
		case <-ctx.Done():
			return false, fmt.Errorf("waiting for provision slot: %w", ctx.Err())
		}
	}
	// Free: shared pool only; waits up to the request deadline.
	select {
	case f.sharedProvision <- struct{}{}:
		return false, nil
	case <-ctx.Done():
		return false, fmt.Errorf("waiting for provision slot: %w", ctx.Err())
	}
}

func (f *FireCrackerManager) releaseProvision(reserved bool) {
	if reserved {
		<-f.proReserved
	} else {
		<-f.sharedProvision
	}
}

// allocateNetwork returns unique TAP/IP values for a new VM.
// Index N → host: 172.16.N.1/30, guest: 172.16.N.2/30, tap: fctapN
// Supports up to 256 concurrent VMs (one /30 subnet each).
func (f *FireCrackerManager) allocateNetwork() (tapName, hostIP, guestIP, mac string) {
	idx := int(f.nextNetIdx.Add(1) - 1)
	tapName = fmt.Sprintf("fctap%d", idx)
	hostIP = fmt.Sprintf("172.16.%d.1", idx)
	guestIP = fmt.Sprintf("172.16.%d.2", idx)
	mac = fmt.Sprintf("AA:FC:00:00:%02X:%02X", idx>>8, idx&0xFF)
	return
}

// createTAP creates a host-side TAP device and assigns it the given /30 host IP.
func createTAP(tapName, hostIP string) error {
	if out, err := exec.Command("ip", "tuntap", "add", tapName, "mode", "tap").CombinedOutput(); err != nil {
		return fmt.Errorf("create TAP %s: %w: %s", tapName, err, out)
	}
	if out, err := exec.Command("ip", "addr", "add", hostIP+"/30", "dev", tapName).CombinedOutput(); err != nil {
		exec.Command("ip", "link", "delete", tapName).Run()
		return fmt.Errorf("assign IP to TAP %s: %w: %s", tapName, err, out)
	}
	if out, err := exec.Command("ip", "link", "set", tapName, "up").CombinedOutput(); err != nil {
		exec.Command("ip", "link", "delete", tapName).Run()
		return fmt.Errorf("bring up TAP %s: %w: %s", tapName, err, out)
	}
	return nil
}

func (f *FireCrackerManager) getSocketPath(vmID string) string {
	return filepath.Join(f.SocketDir, fmt.Sprintf("%s.sock", vmID))
}

func (f *FireCrackerManager) putJSON(client *http.Client, url string, payload interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API call failed: %s - %s", resp.Status, body)
	}
	return nil
}

func (f *FireCrackerManager) patchJSON(client *http.Client, url string, payload interface{}) error {
	data, _ := json.Marshal(payload)
	req, _ := http.NewRequest("PATCH", url, bytes.NewBuffer(data))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PATCH failed: %s - %s", resp.Status, body)
	}
	return nil
}

func (f *FireCrackerManager) Create(ctx context.Context, cfg VMConfig) (*MicroVM, error) {
	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)
	vsockPath := filepath.Join(f.SocketDir, fmt.Sprintf("vsock-%s.sock", vmID))

	if cfg.InitrdPath == "" {
		// No initramfs: fall back to full rootfs copy (legacy cold boot)
		rootfsCopy := filepath.Join(f.SocketDir, fmt.Sprintf("rootfs-%s.ext4", vmID))
		if err := copyFile(cfg.RootfsPath, rootfsCopy); err != nil {
			return nil, fmt.Errorf("failed to copy rootfs for VM %s: %w", vmID, err)
		}
		cfg.RootfsPath = rootfsCopy
	}
	// OverlayFS mode (InitrdPath != ""): no per-VM disk file needed — upper layer is tmpfs
	// captured in the snapshot's RAM, recreated fresh on first boot.

	vm := &MicroVM{
		ID:         vmID,
		Config:     cfg,
		State:      VMStateCreated,
		SocketPath: socketPath,
		VsockPath:  vsockPath,
		Slot:       -1, // cold-boot VM, not using slot pool
		CreatedAt:  time.Now(),
		Process:    nil,
	}

	f.mu.Lock()
	f.Vms[vmID] = vm
	f.mu.Unlock()

	return vm, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func (f *FireCrackerManager) Boot(ctx context.Context, vmID string) error {
	f.mu.RLock()
	vm, exists := f.Vms[vmID]
	f.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM %s not found", vmID)
	}

	// Admission gate (tier-aware): cold boot also spawns Firecracker, so it shares the
	// same provisioning bound. Tier comes from the VM's config.
	resv, gerr := f.acquireProvision(ctx, vm.Config.Pro)
	if gerr != nil {
		return fmt.Errorf("provision gate: %w", gerr)
	}
	defer f.releaseProvision(resv)

	// Allocate the host-side TAP + IPs for this VM's network interface. The guest's own
	// IP/gateway is baked into the rootfs (/etc/vm-network.env) at build time, so we no
	// longer mount the rootfs here to write it — that runtime mount was the leak source.
	tapName, hostIP, _, mac := f.allocateNetwork()

	cmd := exec.Command(f.BinaryPath, "--api-sock", vm.SocketPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Start()
	if err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait up to 5 seconds for socket
	for range 50 {
		_, err := os.Stat(vm.SocketPath)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", vm.SocketPath)
			},
		},
	}

	if err := f.putJSON(client, "http://localhost/boot-source", BootSource{
		KernelImagePath: vm.Config.KernelPath,
		BootArgs:        vm.Config.BootArgs,
		InitrdPath:      vm.Config.InitrdPath,
	}); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	if vm.Config.InitrdPath != "" {
		// OverlayFS mode: single read-only lower drive; upper layer is tmpfs (in RAM, no block device)
		if err := f.putJSON(client, "http://localhost/drives/lower", Drive{
			DriveId:      "lower",
			PathOnHost:   vm.Config.RootfsPath,
			IsRootDevice: true,
			IsReadOnly:   true,
		}); err != nil {
			return fmt.Errorf("failed to set lower drive: %w", err)
		}
	} else {
		// Legacy mode: single writable rootfs copy
		if err := f.putJSON(client, "http://localhost/drives/rootfs", Drive{
			DriveId:      "rootfs",
			PathOnHost:   vm.Config.RootfsPath,
			IsRootDevice: true,
			IsReadOnly:   false,
		}); err != nil {
			return fmt.Errorf("failed to set drive: %w", err)
		}
	}

	if err := f.putJSON(client, "http://localhost/machine-config", MachineConfig{
		VCPUCount:  vm.Config.VCPUCount,
		MemSizeMib: vm.Config.MemSizeMiB,
	}); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	guestCID := int(f.nextCID.Add(1)) + 2 // CIDs 0,1,2 are reserved; start from 3
	if err := f.putJSON(client, "http://localhost/vsock", VsockConfig{
		GuestCID: guestCID,
		UDSPath:  vm.VsockPath,
	}); err != nil {
		return fmt.Errorf("failed to set vsock: %w", err)
	}

	// Enable virtio-rng entropy device: feeds host entropy into the guest at boot.
	// Required for kernels < 4.19 (our kernel is 4.14) where random.trust_cpu=on
	// is not supported. Without this, Node.js v20+ blocks indefinitely on getrandom()
	// because the guest CRNG never reaches full initialization in nested-virt environments.
	// The seeded CRNG state is captured in the snapshot so restored VMs start ready.
	if err := f.putJSON(client, "http://localhost/entropy", struct{}{}); err != nil {
		slog.Warn("entropy device setup failed, node/python bridges may hang on getrandom()", "vm_id", vmID, "err", err)
	}

	// Create TAP device on the host and wire it into Firecracker.
	// Non-fatal: VM boots without network if TAP setup fails (e.g. no permissions).
	// NETWORK CARD
	if err := createTAP(tapName, hostIP); err != nil {
		slog.Warn("TAP creation failed, VM will boot without network", "tap", tapName, "err", err)
	} else {
		if err := f.putJSON(client, "http://localhost/network-interfaces/eth0", NetworkInterface{
			IfaceID:     "eth0",
			GuestMAC:    mac,
			HostDevName: tapName,
		}); err != nil {
			slog.Warn("failed to configure Firecracker network interface", "tap", tapName, "err", err)
			exec.Command("ip", "link", "delete", tapName).Run()
		} else {
			vm.TapName = tapName
		}
	}

	if err := f.putJSON(client, "http://localhost/actions", map[string]string{"action_type": "InstanceStart"}); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	f.mu.Lock()
	vm.Process = cmd
	vm.Stdout = &stdout
	vm.Stderr = &stderr
	vm.State = VMStateRunning
	f.mu.Unlock()

	return nil
}

func (f *FireCrackerManager) Stop(ctx context.Context, vmID string) error {
	f.mu.RLock()
	vm, exists := f.Vms[vmID]
	f.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM %s not found", vmID)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", vm.SocketPath)
			},
		},
	}

	if err := f.putJSON(client, "http://localhost/actions", map[string]string{"action_type": "SendCtrlAltDel"}); err != nil {
		return fmt.Errorf("failed to stop instance: %w", err)
	}

	f.mu.Lock()
	vm.State = VMStateStopped
	f.mu.Unlock()
	return nil
}

func (f *FireCrackerManager) Destroy(ctx context.Context, vmID string) error {
	f.mu.Lock()
	vm, exists := f.Vms[vmID]
	if !exists {
		f.mu.Unlock()
		return fmt.Errorf("VM %s not found", vmID)
	}
	vm.State = VMStateDestroyed
	delete(f.Vms, vmID)
	f.mu.Unlock()

	if vm.Process != nil {
		vm.Process.Process.Kill()
		vm.Process.Wait()
	}

	os.Remove(vm.SocketPath)
	os.Remove(vm.VsockPath)
	if vm.Config.InitrdPath == "" {
		os.Remove(vm.Config.RootfsPath) // legacy: delete the VM-specific rootfs copy
	}
	// OverlayFS mode: lower is the shared rootfs (not deleted), upper is tmpfs in RAM (no file)

	// Slot-based VMs (snapshot restores): return the slot to the pool so the next
	// restore can reuse the pre-created netns + TAP. Cold-boot VMs (template creation)
	// have Slot == -1 and own their TAP — delete it directly.
	if vm.Slot >= 0 {
		f.slotPool.Release(vm.Slot)
	} else if vm.TapName != "" {
		if out, err := exec.Command("ip", "link", "delete", vm.TapName).CombinedOutput(); err != nil {
			slog.Warn("failed to delete TAP device", "tap", vm.TapName, "err", err, "output", string(out))
		}
	}

	return nil
}

func (f *FireCrackerManager) Snapshot(ctx context.Context, vmID, snapPath, memPath string) error {
	f.mu.RLock()
	vm, exists := f.Vms[vmID]
	f.mu.RUnlock()
	if !exists {
		return fmt.Errorf("VM %s not found", vmID)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", vm.SocketPath)
			},
		},
	}

	if err := f.patchJSON(client, "http://localhost/vm", VMStateChange{State: "Paused"}); err != nil {
		return fmt.Errorf("failed to pause VM: %w", err)
	}

	return f.putJSON(client, "http://localhost/snapshot/create", SnapshotCreateRequest{
		SnapshotType: "Full",
		SnapshotPath: snapPath,
		MemFilePath:  memPath,
	})
}

func (f *FireCrackerManager) LoadFromSnapshot(ctx context.Context, cfg VMConfig, tmpl *SnapshotTemplate) (*MicroVM, error) {
	t0 := time.Now()
	lastPhase := t0
	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)
	vsockPath := filepath.Join(f.SocketDir, fmt.Sprintf("vsock-%s.sock", vmID))

	// Admission gate (tier-aware): bound concurrent provisioning so the host isn't
	// stampeded. Pro prefers its reserve, Free uses the shared pool. Held until the VM
	// is restored and resumed. Acquired before the slot so we don't hold one while waiting.
	resv, gerr := f.acquireProvision(ctx, cfg.Pro)
	if gerr != nil {
		return nil, fmt.Errorf("provision gate: %w", gerr)
	}
	defer f.releaseProvision(resv)

	// Acquire a pre-created network slot. Each slot has its own netns + TAP + veth
	// + iptables rules set up by server.sh. The guest's baked-in gateway (172.16.0.1)
	// matches the TAP's host IP in the slot, so no guest-side reconfiguration is needed.
	slot, err := f.slotPool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire network slot: %w", err)
	}
	nsName := fmt.Sprintf("fc-ns-%d", slot)
	tapSlotName := fmt.Sprintf("fc-tap-%d", slot)

	// Run Firecracker inside the slot's network namespace so it sees the slot's TAP.
	// Unix sockets (API socket, vsock UDS) are filesystem-based and work across netns.
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("ip", "netns", "exec", nsName, f.BinaryPath, "--api-sock", socketPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		f.slotPool.Release(slot)
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait for API socket (up to 5s) — per-VM socket, no lock needed
	for range 50 {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	socketReadyMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()
	slog.Debug("restore: firecracker socket ready", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Rename slot TAP to the template TAP name inside the slot's netns.
	// Firecracker's snapshot has tmpl.TapName baked in — it must find that name.
	// Safe to do before the lock: each slot has its own namespace, no conflict.
	if tmpl.TapName != "" {
		if out, err := exec.Command("ip", "netns", "exec", nsName,
			"ip", "link", "set", tapSlotName, "name", tmpl.TapName).CombinedOutput(); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			f.slotPool.Release(slot)
			return nil, fmt.Errorf("TAP pre-rename failed in %s: %w: %s", nsName, err, out)
		}
	}

	tapRenameMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

	// Serialize on vsock path: tmpl.VsockPath is shared across all concurrent restores.
	// The lock covers only the snapshot load → resume → vsock rename window.
	f.restoreMu.Lock()
	lockWaitMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()
	slog.Debug("restore: lock acquired", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	restoreCleanup := func() {
		f.restoreMu.Unlock()
		cmd.Process.Kill()
		cmd.Wait()
		// Rename TAP back so the slot is reusable
		exec.Command("ip", "netns", "exec", nsName, "ip", "link", "set", tmpl.TapName, "name", tapSlotName).Run()
		f.slotPool.Release(slot)
	}

	// Clean up stale vsock from a previous failed restore
	os.Remove(tmpl.VsockPath)

	// Load snapshot paused — Firecracker (in nsName) opens tmpl.TapName inside the netns
	if err := f.putJSON(client, "http://localhost/snapshot/load", SnapshotLoadRequest{
		SnapshotPath:        tmpl.SnapPath,
		MemFilePath:         tmpl.MemPath,
		EnableDiffSnapshots: false,
		ResumeVM:            false,
	}); err != nil {
		restoreCleanup()
		return nil, fmt.Errorf("snapshot load failed: %w", err)
	}
	snapshotLoadMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

	// Update lower drive — shared template rootfs (read-only); upper is tmpfs in RAM
	if err := f.patchJSON(client, "http://localhost/drives/lower", map[string]string{
		"drive_id":     "lower",
		"path_on_host": tmpl.RootfsPath,
	}); err != nil {
		restoreCleanup()
		return nil, fmt.Errorf("lower drive update failed: %w", err)
	}
	drivePatchMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

	// Resume — Firecracker binds tmpl.VsockPath; guest wakes up with its baked-in IP
	if err := f.patchJSON(client, "http://localhost/vm", VMStateChange{State: "Resumed"}); err != nil {
		restoreCleanup()
		return nil, fmt.Errorf("resume failed: %w", err)
	}
	resumeMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

	// Rename vsock → unique per-VM path (fd survives rename, Firecracker keeps listening)
	if err := os.Rename(tmpl.VsockPath, vsockPath); err != nil {
		restoreCleanup()
		return nil, fmt.Errorf("vsock rename failed: %w", err)
	}

	f.restoreMu.Unlock()
	postResumeRenameMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()
	slog.Debug("restore: lock released", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	// Rename tmpl.TapName back to the slot name now that the lock is released.
	// The slot TAP keeps its pre-assigned IP (172.16.0.1/30) — no IP add needed.
	if tmpl.TapName != "" {
		exec.Command("ip", "netns", "exec", nsName, "ip", "link", "set", tmpl.TapName, "name", tapSlotName).Run()
	}

	// No configure_network call: the guest's baked-in IP (172.16.0.2) and gateway
	// (172.16.0.1) match the slot TAP's pre-configured address. No vsock round-trip,
	// no virtio-vsock teardown interrupt, no ~4s stall.

	vm := &MicroVM{
		ID:         vmID,
		Config:     cfg,
		State:      VMStateRunning,
		SocketPath: socketPath,
		VsockPath:  vsockPath,
		TapName:    tapSlotName,
		Slot:       slot,
		CreatedAt:  time.Now(),
		Process:    cmd,
		Stdout:     &stdout,
		Stderr:     &stderr,
	}

	f.mu.Lock()
	f.Vms[vmID] = vm
	f.mu.Unlock()

	slog.Info("snapshot restore timings",
		"vm_id", vmID,
		"slot", slot,
		"firecracker_socket_ms", socketReadyMs,
		"tap_rename_ms", tapRenameMs,
		"restore_lock_wait_ms", lockWaitMs,
		"snapshot_load_ms", snapshotLoadMs,
		"drive_patch_ms", drivePatchMs,
		"resume_ms", resumeMs,
		"post_resume_rename_ms", postResumeRenameMs,
		"finalize_ms", time.Since(lastPhase).Milliseconds(),
		"total_ms", time.Since(t0).Milliseconds(),
	)

	return vm, nil
}

func (f *FireCrackerManager) CreateTemplate(ctx context.Context, cfg VMConfig, snapDir string) (*SnapshotTemplate, error) {
	if err := os.MkdirAll(snapDir, 0755); err != nil {
		return nil, err
	}

	// Boot a normal VM
	vm, err := f.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := f.Boot(ctx, vm.ID); err != nil {
		f.Destroy(ctx, vm.ID)
		return nil, err
	}

	// Wait until guest agent is actually accepting connections check this our
	vsock := NewVsockClient(vm.VsockPath)
	vsockReady := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _, _ := vsock.Ping(); ok {
			vsockReady = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !vsockReady {
		slog.Error("template VM console output", "stdout", vm.Stdout.String(), "stderr", vm.Stderr.String())
		f.Destroy(ctx, vm.ID)
		return nil, fmt.Errorf("vsock never became ready for template VM")
	}

	// Attempt bridge warm-up inside the template VM, but do not fail template
	// creation if a runtime is unhealthy. This keeps snapshot restore available
	// while still logging runtime-specific issues for diagnosis.
	nodeResp, err := vsock.Execute("1+1", "node", "stateless", 60)
	if err != nil {
		slog.Warn("template node warmup transport failed",
			"vm_id", vm.ID,
			"err", err,
			"vm_console", vm.Stdout.String(),
		)
	} else {
		nodeStderr := strings.TrimSpace(nodeResp.Stderr)
		if nodeResp.ExitCode != 0 || strings.Contains(strings.ToLower(nodeStderr), "timeout") {
			slog.Warn("template node warmup failed",
				"vm_id", vm.ID,
				"exit_code", nodeResp.ExitCode,
				"stderr", nodeStderr,
				"guest_duration_ms", int64(nodeResp.Duration*1000),
				"vm_console", vm.Stdout.String(),
			)
		} else {
			slog.Info("template node warmup succeeded",
				"vm_id", vm.ID,
				"guest_duration_ms", int64(nodeResp.Duration*1000),
			)
		}
	}

	pyResp, err := vsock.Execute("pass", "python", "stateful", 60)
	if err != nil {
		slog.Warn("template python warmup transport failed",
			"vm_id", vm.ID,
			"err", err,
			"vm_console", vm.Stdout.String(),
		)
	} else {
		pyStderr := strings.TrimSpace(pyResp.Stderr)
		if pyResp.ExitCode != 0 || strings.Contains(strings.ToLower(pyStderr), "timeout") {
			slog.Warn("template python warmup failed",
				"vm_id", vm.ID,
				"exit_code", pyResp.ExitCode,
				"stderr", pyStderr,
				"guest_duration_ms", int64(pyResp.Duration*1000),
				"vm_console", vm.Stdout.String(),
			)
		} else {
			slog.Info("template python warmup succeeded",
				"vm_id", vm.ID,
				"guest_duration_ms", int64(pyResp.Duration*1000),
			)
		}
	}

	// Take snapshot
	snapPath := filepath.Join(snapDir, "template.snap")
	memPath := filepath.Join(snapDir, "template.mem")
	if err := f.Snapshot(ctx, vm.ID, snapPath, memPath); err != nil {
		f.Destroy(ctx, vm.ID)
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}

	// Partial cleanup: kill process and remove sockets but KEEP rootfs copy.
	// Also delete the TAP so it's free to be recreated for each restore.
	rootfsPath := vm.Config.RootfsPath
	tapName := vm.TapName
	vsockPath := vm.VsockPath
	f.mu.Lock()
	vm.State = VMStateDestroyed
	delete(f.Vms, vm.ID)
	f.mu.Unlock()
	if vm.Process != nil {
		vm.Process.Process.Kill()
		vm.Process.Wait()
	}
	os.Remove(vm.SocketPath)
	os.Remove(vsockPath)
	if tapName != "" {
		exec.Command("ip", "link", "delete", tapName).Run()
	}
	// rootfsPath intentionally NOT removed — restored VMs will clone from it

	slog.Info("snapshot template created", "snap", snapPath, "mem", memPath)
	return &SnapshotTemplate{
		SnapPath:   snapPath,
		MemPath:    memPath,
		RootfsPath: rootfsPath,
		VsockPath:  vsockPath,
		TapName:    tapName,
	}, nil
}
