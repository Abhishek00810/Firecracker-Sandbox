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
	CreatedAt  time.Time
	Process    *exec.Cmd
	Stdout     *bytes.Buffer
	Stderr     *bytes.Buffer
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
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
	VsockPath  string // vsock UDS path baked into the snapshot — must be deleted before each restore
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
	restoreMu  sync.Mutex // serializes snapshot restores — prevents vsock binding conflicts
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

	// Each VM gets its own rootfs copy so state never leaks between executions
	rootfsCopy := filepath.Join(f.SocketDir, fmt.Sprintf("rootfs-%s.ext4", vmID))
	if err := copyFile(cfg.RootfsPath, rootfsCopy); err != nil {
		return nil, fmt.Errorf("failed to copy rootfs for VM %s: %w", vmID, err)
	}

	vmCfg := cfg
	vmCfg.RootfsPath = rootfsCopy

	vm := &MicroVM{
		ID:         vmID,
		Config:     vmCfg,
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
	}); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	if err := f.putJSON(client, "http://localhost/drives/rootfs", Drive{
		DriveId:      "rootfs",
		PathOnHost:   vm.Config.RootfsPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}); err != nil {
		return fmt.Errorf("failed to set drive: %w", err)
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
	os.Remove(vm.Config.RootfsPath) // delete the VM-specific rootfs copy

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
	// Serialize restores: each snapshot restore binds a new vsock path, but the
	// Firecracker process needs the API socket to come up before we can issue
	// the load request. Running restores concurrently races on vsock binding.
	f.restoreMu.Lock()
	defer f.restoreMu.Unlock()

	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)
	vsockPath := filepath.Join(f.SocketDir, fmt.Sprintf("vsock-%s.sock", vmID))

	// Fresh writable rootfs copy for this VM
	rootfsCopy := filepath.Join(f.SocketDir, fmt.Sprintf("rootfs-%s.ext4", vmID))
	if err := copyFile(tmpl.RootfsPath, rootfsCopy); err != nil {
		return nil, fmt.Errorf("failed to copy rootfs: %w", err)
	}

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

	// Wait for API socket (up to 5s)
	for range 50 {
		if _, err := os.Stat(socketPath); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", socketPath)
			},
		},
	}

	// Delete the stale vsock socket file baked into the snapshot.
	// Firecracker tries to bind that path during snapshot/load. If the file
	// still exists from a previous (failed) restore, bind fails with EADDRINUSE.
	os.Remove(tmpl.VsockPath)

	// Load snapshot paused — don't resume yet
	if err := f.putJSON(client, "http://localhost/snapshot/load", SnapshotLoadRequest{
		SnapshotPath:        tmpl.SnapPath,
		MemFilePath:         tmpl.MemPath,
		EnableDiffSnapshots: false,
		ResumeVM:            false,
	}); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("snapshot load failed: %w", err)
	}

	// Update rootfs backend to fresh copy.
	// After snapshot/load, PUT /drives is rejected ("not supported after starting the microVM").
	// PATCH /drives/{id} is the runtime path-swap endpoint — only path_on_host is required.
	if err := f.patchJSON(client, "http://localhost/drives/rootfs", map[string]string{
		"drive_id":     "rootfs",
		"path_on_host": rootfsCopy,
	}); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("drive update failed: %w", err)
	}

	// Resume VM first — Firecracker re-binds the vsock socket during resume.
	// We must rename AFTER resume so the re-bind succeeds on the original path.
	if err := f.patchJSON(client, "http://localhost/vm", VMStateChange{State: "Resumed"}); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("resume failed: %w", err)
	}

	// Rename the vsock socket file to a unique per-VM path.
	// Firecracker holds the listening fd — renaming the file on disk gives this
	// VM a unique path while the fd stays intact. The next restore can then
	// delete and rebind the template path cleanly.
	if err := os.Rename(tmpl.VsockPath, vsockPath); err != nil {
		cmd.Process.Kill()
		return nil, fmt.Errorf("vsock rename failed: %w", err)
	}

	vm := &MicroVM{
		ID:         vmID,
		Config:     cfg,
		State:      VMStateRunning,
		SocketPath: socketPath,
		VsockPath:  vsockPath,
		CreatedAt:  time.Now(),
		Process:    cmd,
		Stdout:     &stdout,
		Stderr:     &stderr,
	}
	vm.Config.RootfsPath = rootfsCopy

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

	// Wait for vsock socket (guest agent ready) — up to 15s
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(vm.VsockPath); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, err := os.Stat(vm.VsockPath); err != nil {
		f.Destroy(ctx, vm.ID)
		return nil, fmt.Errorf("vsock never appeared for template VM")
	}

	// Take snapshot
	snapPath := filepath.Join(snapDir, "template.snap")
	memPath := filepath.Join(snapDir, "template.mem")
	if err := f.Snapshot(ctx, vm.ID, snapPath, memPath); err != nil {
		f.Destroy(ctx, vm.ID)
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}

	// Partial cleanup: kill process and remove sockets but KEEP rootfs copy
	rootfsPath := vm.Config.RootfsPath
	f.mu.Lock()
	vm.State = VMStateDestroyed
	delete(f.Vms, vm.ID)
	f.mu.Unlock()
	if vm.Process != nil {
		vm.Process.Process.Kill()
		vm.Process.Wait()
	}
	os.Remove(vm.SocketPath)
	os.Remove(vm.VsockPath)
	// rootfsPath intentionally NOT removed — restored VMs will clone from it

	slog.Info("snapshot template created", "snap", snapPath, "mem", memPath)
	return &SnapshotTemplate{
		SnapPath:   snapPath,
		MemPath:    memPath,
		RootfsPath: rootfsPath,
		VsockPath:  vm.VsockPath,
	}, nil
}
