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
	// lets go with this one and will include vm lifecycle after this
	return executor.ExecutionResult{
		Output:            "firecracker stub",
		Duration:          0.0,
		ExitCode:          0,
		TerminationReason: "success",
	}, nil
}
