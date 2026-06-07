package firecracker

import (
	"context"
	"testing"
	"time"
)

func TestAdmissionControlSeparatesSharedAndProReservedCapacity(t *testing.T) {
	manager := NewFirecrackerManager(t.TempDir(), "./assets", "/bin/true", 8, 3, 1)

	freeReserved, err := manager.acquireProvision(context.Background(), false)
	if err != nil {
		t.Fatalf("first free acquire failed: %v", err)
	}
	if freeReserved {
		t.Fatal("free acquire must not use pro reserved capacity")
	}
	defer manager.releaseProvision(freeReserved)

	freeReserved2, err := manager.acquireProvision(context.Background(), false)
	if err != nil {
		t.Fatalf("second free acquire failed: %v", err)
	}
	if freeReserved2 {
		t.Fatal("free acquire must not use pro reserved capacity")
	}
	defer manager.releaseProvision(freeReserved2)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := manager.acquireProvision(ctx, false); err == nil {
		t.Fatal("expected free acquire to wait/fail when shared capacity is full")
	}

	proReserved, err := manager.acquireProvision(context.Background(), true)
	if err != nil {
		t.Fatalf("pro acquire should use reserved capacity when shared is full: %v", err)
	}
	if !proReserved {
		t.Fatal("expected pro acquire to use reserved capacity")
	}
	defer manager.releaseProvision(proReserved)
}

func TestAdmissionControlClampsProReserve(t *testing.T) {
	manager := NewFirecrackerManager(t.TempDir(), "./assets", "/bin/true", 8, 2, 99)

	if got := cap(manager.proReserved); got != 1 {
		t.Fatalf("expected pro reserve clamped to 1, got %d", got)
	}
	if got := cap(manager.sharedProvision); got != 1 {
		t.Fatalf("expected at least one shared provision slot, got %d", got)
	}
}
