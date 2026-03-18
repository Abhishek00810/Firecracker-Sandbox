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

type FireCrackerManager struct {
	SocketDir  string
	AssetsPath string
	Vms        map[string]*MicroVM
	mu         sync.RWMutex
	nextCID    atomic.Uint32
	nextNetIdx atomic.Uint32 // for TAP subnet allocation: index N → 172.16.N.0/30
	restoreMu  sync.Mutex    // serializes snapshot restores — prevents vsock binding conflicts
}

type VMManager interface {
	Create(ctx context.Context, cfg VMConfig) (*MicroVM, error)
	Boot(ctx context.Context, vmID string) error
	Stop(ctx context.Context, vmID string) error
	Destroy(ctx context.Context, vmID string) error
	Snapshot(ctx context.Context, vmID string, snapPath, memPath string) error
	LoadFromSnapshot(ctx context.Context, cfg VMConfig, tmpl *SnapshotTemplate) (*MicroVM, error)
}

func NewFirecrackerManager(socketDir, assetsPath string) *FireCrackerManager {
	return &FireCrackerManager{
		SocketDir:  socketDir,
		AssetsPath: assetsPath,
		Vms:        make(map[string]*MicroVM),
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

// writeNetworkConfig mounts the VM's rootfs copy as a loop device, writes
// /etc/vm-network.env with the guest IP and gateway, then unmounts.
// The guest-agent reads this file at boot to configure eth0.
func writeNetworkConfig(rootfsPath, guestIP, gwIP string) error {
	mntDir := rootfsPath + ".mnt"
	if err := os.MkdirAll(mntDir, 0755); err != nil {
		return err
	}
	if out, err := exec.Command("/bin/mount", "-o", "loop", rootfsPath, mntDir).CombinedOutput(); err != nil {
		os.Remove(mntDir)
		return fmt.Errorf("mount rootfs: %w: %s", err, out)
	}
	content := fmt.Sprintf("GUESTIP=%s\nGWIP=%s\n", guestIP, gwIP)
	writeErr := os.WriteFile(filepath.Join(mntDir, "etc", "vm-network.env"), []byte(content), 0644)
	exec.Command("/bin/umount", mntDir).Run()
	os.Remove(mntDir)
	return writeErr
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

	// Allocate network config upfront so IPs can be written into the rootfs.
	tapName, hostIP, guestIP, mac := f.allocateNetwork()

	// Write guest IP + gateway into the rootfs copy so the guest-agent can
	// configure eth0 at boot without needing /proc to be mounted.
	if err := writeNetworkConfig(vm.Config.RootfsPath, guestIP, hostIP); err != nil {
		slog.Warn("failed to write network config to rootfs, VM will boot without network", "err", err)
	}

	firecrackerBinary := os.Getenv("FIRECRACKER_BINARY")
	if firecrackerBinary == "" {
		firecrackerBinary = "/app/firecracker/firecracker-v1.7.0-aarch64"
	}

	cmd := exec.Command(firecrackerBinary, "--api-sock", vm.SocketPath)
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

	// Delete the TAP device allocated for this VM.
	if vm.TapName != "" {
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
	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)
	vsockPath := filepath.Join(f.SocketDir, fmt.Sprintf("vsock-%s.sock", vmID))

	firecrackerBinary := os.Getenv("FIRECRACKER_BINARY")
	if firecrackerBinary == "" {
		firecrackerBinary = "/app/firecracker/firecracker-v1.7.0-aarch64"
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(firecrackerBinary, "--api-sock", socketPath)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start firecracker: %w", err)
	}

	// Wait for API socket (up to 5s) — per-VM socket, no lock needed
	for range 50 {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Debug("restore: firecracker socket ready", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Allocate unique TAP/IP for this VM — atomic counter, no lock needed
	tapName, hostIP, guestIP, _ := f.allocateNetwork()

	// Serialize only the TAP/vsock critical section: tmpl.TapName and tmpl.VsockPath
	// are shared across all restores. Two concurrent restores would conflict trying
	// to both recreate fctap0 and bind the same vsock path.
	f.restoreMu.Lock()
	slog.Debug("restore: lock acquired", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	// Recreate the template's TAP device (no IP yet — just needs to exist for Firecracker).
	// The template cleanup deleted it so it's free to recreate.
	if tmpl.TapName != "" {
		exec.Command("ip", "link", "delete", tmpl.TapName).Run() // clean stale if any
		if out, err := exec.Command("ip", "tuntap", "add", tmpl.TapName, "mode", "tap").CombinedOutput(); err != nil {
			slog.Warn("recreate template TAP failed, VM will boot without network", "tap", tmpl.TapName, "err", err, "out", string(out))
		} else {
			exec.Command("ip", "link", "set", tmpl.TapName, "up").Run()
		}
	}

	// Clean up stale vsock from a previous failed restore
	os.Remove(tmpl.VsockPath)

	// Load snapshot paused — Firecracker opens tmpl.TapName
	if err := f.putJSON(client, "http://localhost/snapshot/load", SnapshotLoadRequest{
		SnapshotPath:        tmpl.SnapPath,
		MemFilePath:         tmpl.MemPath,
		EnableDiffSnapshots: false,
		ResumeVM:            false,
	}); err != nil {
		f.restoreMu.Unlock()
		cmd.Process.Kill()
		exec.Command("ip", "link", "delete", tmpl.TapName).Run()
		return nil, fmt.Errorf("snapshot load failed: %w", err)
	}

	// Update lower drive — shared template rootfs (read-only); upper is tmpfs in RAM, no block device.
	if err := f.patchJSON(client, "http://localhost/drives/lower", map[string]string{
		"drive_id":     "lower",
		"path_on_host": tmpl.RootfsPath,
	}); err != nil {
		f.restoreMu.Unlock()
		cmd.Process.Kill()
		exec.Command("ip", "link", "delete", tmpl.TapName).Run()
		return nil, fmt.Errorf("lower drive update failed: %w", err)
	}

	// Resume — Firecracker binds tmpl.VsockPath and uses tmpl.TapName
	if err := f.patchJSON(client, "http://localhost/vm", VMStateChange{State: "Resumed"}); err != nil {
		f.restoreMu.Unlock()
		cmd.Process.Kill()
		exec.Command("ip", "link", "delete", tmpl.TapName).Run()
		return nil, fmt.Errorf("resume failed: %w", err)
	}

	// Rename template TAP → unique name so the next restore can recreate tmpl.TapName fresh.
	// Then assign unique IP to the renamed TAP for proper host routing.
	if tmpl.TapName != "" {
		if out, err := exec.Command("ip", "link", "set", tmpl.TapName, "name", tapName).CombinedOutput(); err != nil {
			slog.Warn("TAP rename failed", "from", tmpl.TapName, "to", tapName, "err", err, "out", string(out))
			tapName = ""
		} else {
			exec.Command("ip", "addr", "add", hostIP+"/30", "dev", tapName).Run()
		}
	}

	// Rename vsock → unique per-VM path (fd survives rename, Firecracker keeps listening)
	if err := os.Rename(tmpl.VsockPath, vsockPath); err != nil {
		f.restoreMu.Unlock()
		cmd.Process.Kill()
		if tapName != "" {
			exec.Command("ip", "link", "delete", tapName).Run()
		}
		return nil, fmt.Errorf("vsock rename failed: %w", err)
	}

	// TAP and vsock are now unique to this VM — release the lock so the next
	// restore can proceed with its own TAP/vsock setup in parallel.
	f.restoreMu.Unlock()
	slog.Debug("restore: lock released", "vm_id", vmID, "ms", time.Since(t0).Milliseconds())

	// Reconfigure guest network via vsock — guest still has template's old IP baked in.
	// We flush and reassign a unique IP so routing works correctly.
	if tapName != "" {
		vc := NewVsockClient(vsockPath)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if vc.Ping() {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		netCmd := fmt.Sprintf(
			"ip addr flush dev eth0; ip addr add %s/30 dev eth0; ip link set eth0 up; ip route add default via %s; echo nameserver 8.8.8.8 > /etc/resolv.conf",
			guestIP, hostIP,
		)
		if _, err := vc.Execute(netCmd, "bash", 10); err != nil {
			slog.Warn("guest network reconfiguration failed", "vm_id", vmID, "err", err)
		}
	}

	vm := &MicroVM{
		ID:         vmID,
		Config:     cfg,
		State:      VMStateRunning,
		SocketPath: socketPath,
		VsockPath:  vsockPath,
		TapName:    tapName,
		CreatedAt:  time.Now(),
		Process:    cmd,
		Stdout:     &stdout,
		Stderr:     &stderr,
	}

	f.mu.Lock()
	f.Vms[vmID] = vm
	f.mu.Unlock()

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

	// Wait until guest agent is actually accepting connections
	vsock := NewVsockClient(vm.VsockPath)
	vsockReady := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if vsock.Ping() {
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

	// Warm up the Python kernel before snapshotting. The kernel_bridge.py process,
	// ipykernel process, IPC sockets, and guest-agent's kernels map are all captured
	// in the snapshot RAM — every restored VM inherits the live kernel for free.
	if _, err := vsock.Execute("pass", "python", 30); err != nil {
		slog.Warn("kernel warmup failed for template, snapshot will have cold kernel", "err", err,
			"vm_console", vm.Stderr.String())
	} else {
		slog.Info("kernel warmed up in template VM")
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
