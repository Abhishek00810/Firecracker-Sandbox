package firecracker

import (
	"context"
	"testing"
	"time"
)

func TestAdmissionControlBoundsHostProvisioning(t *testing.T) {
	manager := NewFirecrackerManager(t.TempDir(), "./assets", "/bin/true", 8, 2, 0, 0)

	if err := manager.acquireProvision(context.Background()); err != nil {
		t.Fatalf("first acquire failed: %v", err)
	}
	defer manager.releaseProvision()

	if err := manager.acquireProvision(context.Background()); err != nil {
		t.Fatalf("second acquire failed: %v", err)
	}
	defer manager.releaseProvision()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.acquireProvision(ctx); err == nil {
		t.Fatal("expected acquire to fail when host capacity is full")
	}
}

func TestAdmissionControlHasMinimumCapacity(t *testing.T) {
	manager := NewFirecrackerManager(t.TempDir(), "./assets", "/bin/true", 8, 0, 0, 0)
	if got := cap(manager.provisionSlots); got != 1 {
		t.Fatalf("expected minimum capacity 1, got %d", got)
	}
}
