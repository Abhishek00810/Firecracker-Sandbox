package firecracker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreate(t *testing.T) {
	// Setup
	socketDir := t.TempDir()
	manager := NewFirecrackerManager(socketDir, "./assets", "/bin/true", 8, 8, 0, 0)
	rootfsPath := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := os.WriteFile(rootfsPath, []byte("rootfs"), 0644); err != nil {
		t.Fatalf("write temp rootfs: %v", err)
	}

	// Create a VM
	cfg := VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		KernelPath: "/path/to/kernel",
		RootfsPath: rootfsPath,
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

func TestWaitForPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vsock.sock")
	go func() {
		time.Sleep(25 * time.Millisecond)
		_ = os.WriteFile(path, nil, 0o600)
	}()
	if err := waitForPath(context.Background(), path, time.Second); err != nil {
		t.Fatalf("waitForPath failed: %v", err)
	}
}

func TestWaitForPathTimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sock")
	if err := waitForPath(context.Background(), path, 20*time.Millisecond); err == nil {
		t.Fatal("waitForPath unexpectedly succeeded")
	}
}
