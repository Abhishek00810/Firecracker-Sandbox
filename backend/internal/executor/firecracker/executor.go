package firecracker

import (
	"backend/internal/executor"
	"context"
	"fmt"
	"log"
	"os"
	"time"
)

type FirecrackerExecutor struct {
	VmManager VMManager
}

func NewFirecrackerExecutor(vmManager VMManager) *FirecrackerExecutor {
	return &FirecrackerExecutor{VmManager: vmManager}
}

func (f *FirecrackerExecutor) Execute(ctx context.Context, code, language string) (executor.ExecutionResult, error) {
	startTime := time.Now()
	log.Println("DEBUG: Starting VM creation...")

	vm, err := f.VmManager.Create(ctx, VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    30 * time.Second,
		KernelPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/kernel/vmlinux",
		RootfsPath: "/Users/abhishekdadwal/nothing/sandbox_env/assets/rootfs/rootfs.ext4",
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off init=/usr/local/bin/guest-agent",
	})
	if err != nil {
		log.Printf("DEBUG: VM creation failed: %v", err)
		return executor.ExecutionResult{}, err
	}
	log.Printf("DEBUG: VM created with ID: %s, VsockPath: %s", vm.ID, vm.VsockPath)

	defer f.VmManager.Destroy(ctx, vm.ID)

	log.Println("DEBUG: Booting VM...")
	err = f.VmManager.Boot(ctx, vm.ID)
	if err != nil {
		log.Printf("DEBUG: Boot failed: %v", err)
		return executor.ExecutionResult{}, err
	}
	log.Println("DEBUG: VM booted, waiting for guest agent...")

	time.Sleep(8 * time.Second)

	log.Printf("DEBUG: Checking if vsock socket exists at: %s", vm.VsockPath)
	if _, err := os.Stat(vm.VsockPath); os.IsNotExist(err) {
		log.Printf("DEBUG: Vsock socket DOES NOT EXIST!")
	} else {
		log.Printf("DEBUG: Vsock socket exists")
	}

	// Print VM console output for debugging
	log.Println("DEBUG: ===== VM Console Output =====")
	if vm.Stdout != nil && vm.Stdout.Len() > 0 {
		log.Printf("DEBUG: VM stdout:\n%s", vm.Stdout.String())
	} else {
		log.Println("DEBUG: VM stdout: (empty)")
	}
	if vm.Stderr != nil && vm.Stderr.Len() > 0 {
		log.Printf("DEBUG: VM stderr:\n%s", vm.Stderr.String())
	} else {
		log.Println("DEBUG: VM stderr: (empty)")
	}
	log.Println("DEBUG: ==============================")

	vsockClient := NewVsockClient(vm.VsockPath)
	log.Println("DEBUG: Connecting to guest agent via vsock...")

	// Execute code via vsock
	resp, err := vsockClient.Execute(code, language, 15) // 15 second timeout for code execution
	if err != nil {
		log.Printf("DEBUG: Vsock execute failed: %v", err)
		return executor.ExecutionResult{}, fmt.Errorf("failed to execute via vsock: %w", err)
	}
	log.Printf("DEBUG: Got response - exit_code: %d, stdout_len: %d", resp.ExitCode, len(resp.Stdout))

	duration := time.Since(startTime).Seconds()

	output := resp.Stdout
	if resp.Stderr != "" {
		output += "\n" + resp.Stderr
	}

	return executor.ExecutionResult{
		Output:            output,
		Duration:          duration,
		ExitCode:          int64(resp.ExitCode),
		TerminationReason: "success",
	}, nil
}
