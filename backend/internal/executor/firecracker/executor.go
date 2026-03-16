package firecracker

import (
	"backend/internal/executor"
	"context"
	"fmt"
	"log/slog"
	"time"
)

type FirecrackerExecutor struct {
	VmManager VMManager
	Pool      *VMPool
}

func NewFirecrackerExecutor(vmManager VMManager) *FirecrackerExecutor {
	return &FirecrackerExecutor{VmManager: vmManager}
}

func (f *FirecrackerExecutor) Execute(ctx context.Context, code, language string) (executor.ExecutionResult, error) {
	startTime := time.Now()

	if f.Pool == nil {
		return executor.ExecutionResult{}, fmt.Errorf("VM pool not initialized")
	}

	// Acquire VM from pool
	pooledVM, err := f.Pool.Acquire(30 * time.Second)
	if err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("failed to acquire VM: %w", err)
	}

	// Always release VM back to pool
	defer f.Pool.Release(pooledVM)

	// Execute code via vsock
	vsockClient := NewVsockClient(pooledVM.VM.VsockPath)
	resp, err := vsockClient.Execute(code, language, 15)
	if err != nil {
		slog.Warn("execution failed", "vm_id", pooledVM.VM.ID, "err", err,
			"vm_console", pooledVM.VM.Stderr.String())
		return executor.ExecutionResult{}, fmt.Errorf("failed to execute: %w", err)
	}
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
