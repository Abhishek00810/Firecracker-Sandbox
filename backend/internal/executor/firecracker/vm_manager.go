package firecracker

import (
	"context"
	"fmt"
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

type MicroVM struct {
	ID         string
	Config     VMConfig
	State      VMState
	SocketPath string
	CreatedAt  time.Time
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

func (f *FireCrackerManager) Create(ctx context.Context, cfg VMConfig) (*MicroVM, error) {
	vmID := uuid.New().String()
	socketPath := f.getSocketPath(vmID)

	vm := &MicroVM{
		ID:         vmID,
		Config:     cfg,
		State:      VMStateCreated,
		SocketPath: socketPath,
		CreatedAt:  time.Now(),
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
		return fmt.Errorf("command doesn't started")
	}

	// Wait up to 5 seconds for socket
	for i := 0; i < 50; i++ {
		_, err := os.Stat(vm.SocketPath)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	return nil
}
