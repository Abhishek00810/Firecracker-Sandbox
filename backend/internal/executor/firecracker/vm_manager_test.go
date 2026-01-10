package firecracker

import (
	"context"
	"testing"
)

func TestCreate(t *testing.T) {
	// Setup
	manager := NewFirecrackerManager("/tmp/test-fc", "./assets")

	// Create a VM
	cfg := VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		KernelPath: "/path/to/kernel",
		RootfsPath: "/path/to/rootfs",
		BootArgs:   "console=ttyS0",
	}

	vm, err := manager.Create(context.Background(), cfg)

	// Verify
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	if vm.ID == "" {
		t.Error("VM ID is empty")
	}

	if vm.State != VMStateCreated {
		t.Errorf("Expected state Created, got %s", vm.State)
	}

	// Check it's in the map
	if manager.Vms[vm.ID] == nil {
		t.Error("VM not found in manager map")
	}

	t.Logf("✓ Created VM: %s", vm.ID)
}
