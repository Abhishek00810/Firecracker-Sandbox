package firecracker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"backend/internal/writabledisk"
)

type recordingDiskStore struct {
	createdID   string
	createdMiB  int
	createdPath string
}

func (s *recordingDiskStore) Create(_ context.Context, sandboxID string, sizeMiB int) (string, error) {
	s.createdID = sandboxID
	s.createdMiB = sizeMiB
	s.createdPath = filepath.Join("/mounted-volume", fmt.Sprintf("writable-%s.ext4", sandboxID))
	return s.createdPath, nil
}

func (s *recordingDiskStore) Clone(context.Context, string, string) (string, error) {
	return "", nil
}

func (s *recordingDiskStore) Delete(context.Context, string) error   { return nil }
func (s *recordingDiskStore) List(context.Context) ([]string, error) { return nil, nil }
func (s *recordingDiskStore) Root() string                           { return "/mounted-volume" }

func newTestDiskStore(t *testing.T) writabledisk.Store {
	t.Helper()
	store, err := writabledisk.NewFilesystem(t.TempDir(), writabledisk.CloneAuto)
	if err != nil {
		t.Fatalf("create writable disk store: %v", err)
	}
	return store
}

func TestCreate(t *testing.T) {
	// Setup
	socketDir := t.TempDir()
	manager := NewFirecrackerManager(socketDir, "./assets", "/bin/true", newTestDiskStore(t), 8, 8, 0, 0)
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

func TestCreateUsesConfiguredWritableDiskStore(t *testing.T) {
	store := &recordingDiskStore{}
	manager := NewFirecrackerManager(t.TempDir(), "./assets", "/bin/true", store, 8, 8, 0, 0)

	vm, err := manager.Create(context.Background(), VMConfig{
		VCPUCount:  1,
		MemSizeMiB: 128,
		DiskMB:     512,
		RootfsPath: "/templates/rootfs.ext4",
		InitrdPath: "/templates/initramfs.cpio.gz",
	})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if store.createdID != vm.ID {
		t.Fatalf("store sandbox ID = %q, want %q", store.createdID, vm.ID)
	}
	if store.createdMiB != 512 {
		t.Fatalf("store size = %d MiB, want 512", store.createdMiB)
	}
	if vm.WritableDiskPath != store.createdPath {
		t.Fatalf("VM writable path = %q, want %q", vm.WritableDiskPath, store.createdPath)
	}
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
