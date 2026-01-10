package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
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
}

type BootSource struct {
	KernelImagePath string `json:"kernel_image_path"` //check now
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

type FireCrackerManager struct {
	SocketDir  string
	AssetsPath string
	Vms        map[string]*MicroVM
	mu         sync.RWMutex
}

type VMManager interface {
	Create(ctx context.Context, cfg VMConfig) (*MicroVM, error)
	Boot(ctx context.Context, vmID string) error
	Stop(ctx context.Context, vmID string) error
	Destroy(ctx context.Context, vmID string) error
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

func (f *FireCrackerManager) Create(ctx context.Context, cfg VMConfig) (*MicroVM, error) {
	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)
	vsockPath := filepath.Join(f.SocketDir, fmt.Sprintf("vsock-%s.sock", vmID)) // ← Add this

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

	// due to multiple request it can cause race condition so we are putting mutex here

	return vm, nil
}

func (f *FireCrackerManager) Boot(ctx context.Context, vmID string) error {

	f.mu.RLock()
	vm, exists := f.Vms[vmID]
	f.mu.RUnlock()

	if !exists {
		return fmt.Errorf("VM %s not found", vmID)
	}

	cmd := exec.CommandContext(ctx,
		"/Users/abhishekdadwal/nothing/sandbox_env/release-v1.7.0-aarch64/firecracker-v1.7.0-aarch64",
		"--api-sock", vm.SocketPath,
	)
	err := cmd.Start()

	if err != nil {
		return fmt.Errorf("failed to start firecracker: %w", err) // ← Include actual error
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

	//PUT boot-source
	BootSourcePayload := BootSource{
		KernelImagePath: vm.Config.KernelPath,
		BootArgs:        vm.Config.BootArgs,
	}

	if err := f.putJSON(client, "http://localhost/boot-source", BootSourcePayload); err != nil {
		return fmt.Errorf("failed to set boot source: %w", err)
	}

	//PUT drives

	drivePayload := Drive{
		DriveId:      "rootfs",
		PathOnHost:   vm.Config.RootfsPath,
		IsRootDevice: true,
		IsReadOnly:   false,
	}

	if err := f.putJSON(client, "http://localhost/drives/rootfs", drivePayload); err != nil {
		return fmt.Errorf("failed to set drive: %w", err)
	}

	// PUT machinec-config

	machineconfigPayload := MachineConfig{
		VCPUCount:  vm.Config.VCPUCount,
		MemSizeMib: vm.Config.MemSizeMiB,
	}

	if err := f.putJSON(client, "http://localhost/machine-config", machineconfigPayload); err != nil {
		return fmt.Errorf("failed to set machine config: %w", err)
	}

	//PUT vsock
	vsockPayload := VsockConfig{
		GuestCID: 3,
		UDSPath:  vm.VsockPath,
	}
	if err := f.putJSON(client, "http://localhost/vsock", vsockPayload); err != nil {
		return fmt.Errorf("failed to set vsock: %w", err)
	}
	//PUT actions (start VM)

	startAction := map[string]string{"action_type": "InstanceStart"}
	if err := f.putJSON(client, "http://localhost/actions", startAction); err != nil {
		return fmt.Errorf("failed to start instance: %w", err)
	}

	f.mu.Lock()
	vm.Process = cmd
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

	stopAction := map[string]string{"action_type": "SendCtrlAltDel"}
	if err := f.putJSON(client, "http://localhost/actions", stopAction); err != nil {
		return fmt.Errorf("failed to stop instance: %w", err)
	}
	// Update state
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

	return nil
}
