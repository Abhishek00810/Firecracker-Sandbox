package firecracker

import (
	"backend/internal/executor"
	"context"
)

type FirecrackerExecutor struct {
	VmManager VMManager
}

func NewFirecrackerExecutor(vmManager VMManager) *FirecrackerExecutor {
	return &FirecrackerExecutor{VmManager: vmManager}
}

func (f *FirecrackerExecutor) Execute(ctx context.Context, code, language string) (executor.ExecutionResult, error) {
	return executor.ExecutionResult{
		Output:            "firecracker stub",
		Duration:          0.0,
		ExitCode:          0,
		TerminationReason: "success",
	}, nil
}
