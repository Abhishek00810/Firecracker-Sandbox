package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	registration WorkerRegistration
	registeredAt time.Time
	heartbeatID  string
	heartbeatAt  time.Time
	sandboxID    string
	request      PlacementRequest
	healthyAfter time.Time
	placement    Placement
	err          error
}

func (f *fakeStore) RegisterWorker(_ context.Context, registration WorkerRegistration, at time.Time) error {
	f.registration, f.registeredAt = registration, at
	return f.err
}

func (f *fakeStore) RecordHeartbeat(_ context.Context, id string, at time.Time) error {
	f.heartbeatID, f.heartbeatAt = id, at
	return f.err
}

func (f *fakeStore) ReservePlacement(_ context.Context, sandboxID string, request PlacementRequest, healthyAfter time.Time) (Placement, error) {
	f.sandboxID, f.request, f.healthyAfter = sandboxID, request, healthyAfter
	return f.placement, f.err
}

func (f *fakeStore) GetPlacement(_ context.Context, _ string) (Placement, bool, error) {
	return f.placement, f.placement.WorkerID != "", f.err
}

func (f *fakeStore) ReleasePlacement(_ context.Context, _ string) error { return f.err }

func TestRegisterWorkerNormalizesRegistration(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store, 30*time.Second)
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	err := svc.RegisterWorker(context.Background(), WorkerRegistration{
		ID:                  " worker-1 ",
		Endpoint:            "http://worker-1.internal:9876/",
		AllocatableVCPUs:    96,
		AllocatableMemoryMB: 180000,
		AllocatableDiskGB:   1800,
		MaxSandboxes:        1200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.registration.ID != "worker-1" {
		t.Fatalf("unexpected worker id %q", store.registration.ID)
	}
	if store.registration.Endpoint != "http://worker-1.internal:9876" {
		t.Fatalf("unexpected endpoint %q", store.registration.Endpoint)
	}
	if store.registration.Pool != "default" {
		t.Fatalf("unexpected pool %q", store.registration.Pool)
	}
	if !store.registeredAt.Equal(now) {
		t.Fatalf("unexpected registration time %s", store.registeredAt)
	}
}

func TestRegisterWorkerRejectsInvalidCapacity(t *testing.T) {
	svc := NewService(&fakeStore{}, 30*time.Second)
	err := svc.RegisterWorker(context.Background(), WorkerRegistration{
		ID:       "worker-1",
		Endpoint: "http://worker-1.internal:9876",
	})
	if err == nil {
		t.Fatal("expected invalid capacity error")
	}
}

func TestPlaceUsesHeartbeatCutoff(t *testing.T) {
	store := &fakeStore{placement: Placement{
		SandboxID: "sandbox-1",
		WorkerID:  "worker-1",
		Endpoint:  "http://worker-1.internal:9876",
	}}
	svc := NewService(store, 45*time.Second)
	now := time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	placement, err := svc.Place(context.Background(), "sandbox-1", PlacementRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if placement.WorkerID != "worker-1" {
		t.Fatalf("unexpected worker %q", placement.WorkerID)
	}
	if store.request.Pool != "default" {
		t.Fatalf("unexpected pool %q", store.request.Pool)
	}
	wantCutoff := now.Add(-45 * time.Second)
	if !store.healthyAfter.Equal(wantCutoff) {
		t.Fatalf("cutoff=%s want=%s", store.healthyAfter, wantCutoff)
	}
}

func TestPlacePropagatesNoCapacity(t *testing.T) {
	svc := NewService(&fakeStore{err: ErrNoCapacity}, 30*time.Second)
	_, err := svc.Place(context.Background(), "sandbox-1", PlacementRequest{})
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("error=%v want ErrNoCapacity", err)
	}
}
