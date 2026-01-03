package firecracker

type FirecrackerExecutor struct {
	VmManager VMManager
}

func NewFirecrackerExecutor(vmManager VMManager) *FirecrackerExecutor {
	return &FirecrackerExecutor{VmManager: vmManager}
}
