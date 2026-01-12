package firecracker

import (
	"backend/internal/executor"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

type FirecrackerExecutor struct {
	VmManager VMManager
}

func NewFirecrackerExecutor(vmManager VMManager) *FirecrackerExecutor {
	return &FirecrackerExecutor{VmManager: vmManager}
}

func copyAndInjectCode(srcRootfs, dstRootfs, code, language string) error {
	copyCmd := exec.Command("cp", srcRootfs, dstRootfs)

	if err := copyCmd.Run(); err != nil {
		return fmt.Errorf("failed to copy rootfs: %w", err)
	}

	mountPoint := filepath.Join(os.TempDir(), fmt.Sprintf("fc-mount-%d", time.Now().UnixNano()))

	if err := os.MkdirAll(mountPoint, 0755); err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}

	defer os.RemoveAll(mountPoint)

	mountCmd := exec.Command("sudo", "mount", "-o", "loop", dstRootfs, mountPoint)
	if err := mountCmd.Run(); err != nil {
		return fmt.Errorf("failed to mount rootfs: %w", err)
	}
	defer exec.Command("sudo", "umount", mountPoint).Run() // Unmount on exit
}

func (f *FirecrackerExecutor) Execute(ctx context.Context, code, language string) (executor.ExecutionResult, error) {
	// lets go with this one and will include vm lifecycle after this
	// 1. create vm
	//startTime := time.Now()
	vm, err := f.VmManager.Create(ctx, VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    5 * time.Second,
		KernelPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/kernel/vmlinux",
		RootfsPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/rootfs/rootfs.ext4",
		BootArgs:   "keep_bootcon console=ttyS0 reboot=k panic=1 pci=off",
	})
	if err != nil {
		return executor.ExecutionResult{}, err
	}

	//copiedRootfsPath := filepath.Join(os.TempDir(), fmt.Sprintf("fc-rootfs-%s.ext4", vm.ID))
	//2. boot vm

	err = f.VmManager.Boot(ctx, vm.ID)
	if err != nil {
		f.VmManager.Destroy(ctx, vm.ID) // Cleanup on error
		return executor.ExecutionResult{}, err
	}

	return executor.ExecutionResult{
		Output:            "firecracker stub",
		Duration:          0.0,
		ExitCode:          0,
		TerminationReason: "success",
	}, nil
}
