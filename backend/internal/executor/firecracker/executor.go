package firecracker

import (
	"backend/internal/executor"
	"bytes"
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
	defer exec.Command("sudo", "umount", mountPoint).Run()

	codeFile := filepath.Join(mountPoint, "root", "user_code.py")

	mkdirCmd := exec.Command("sudo", "mkdir", "-p", filepath.Dir(codeFile))
	if err := mkdirCmd.Run(); err != nil {
		return fmt.Errorf("failed to create code directory: %w", err)
	}

	writeCmd := exec.Command("sudo", "tee", codeFile)
	writeCmd.Stdin = bytes.NewReader([]byte(code))
	if err := writeCmd.Run(); err != nil {
		return fmt.Errorf("failed to write code file: %w", err)
	}

	chmodCmd := exec.Command("sudo", "chmod", "644", codeFile)
	if err := chmodCmd.Run(); err != nil {
		return fmt.Errorf("failed to set file permissions: %w", err)
	}

	return nil
}

func (f *FirecrackerExecutor) Execute(ctx context.Context, code, language string) (executor.ExecutionResult, error) {
	startTime := time.Now()

	vm, err := f.VmManager.Create(ctx, VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    30 * time.Second,
		KernelPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/kernel/vmlinux",
		RootfsPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/rootfs/rootfs.ext4",
		BootArgs:   "keep_bootcon console=ttyS0 reboot=k panic=1 pci=off",
	})
	if err != nil {
		return executor.ExecutionResult{}, err
	}

	copiedRootfsPath := filepath.Join(os.TempDir(), fmt.Sprintf("fc-rootfs-%s.ext4", vm.ID))
	err = copyAndInjectCode(vm.Config.RootfsPath, copiedRootfsPath, code, language)
	if err != nil {
		f.VmManager.Destroy(ctx, vm.ID)
		return executor.ExecutionResult{}, fmt.Errorf("failed to inject code: %w", err)
	}
	defer os.Remove(copiedRootfsPath)
	vm.Config.RootfsPath = copiedRootfsPath

	err = f.VmManager.Boot(ctx, vm.ID)
	if err != nil {
		f.VmManager.Destroy(ctx, vm.ID)
		return executor.ExecutionResult{}, err
	}
	defer f.VmManager.Destroy(ctx, vm.ID)

	output, exitCode, err := f.VmManager.WaitForCompletion(ctx, vm.ID, vm.Config.Timeout)
	if err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("failed to wait for completion: %w", err)
	}

	duration := time.Since(startTime).Seconds()

	return executor.ExecutionResult{
		Output:            output,
		Duration:          duration,
		ExitCode:          exitCode,
		TerminationReason: "success",
	}, nil
}
