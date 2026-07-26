package orchestrator

import (
	"context"
	"errors"
	"testing"
	"time"

	"backend/internal/plane"
)

type fakeStore struct {
	registration  WorkerRegistration
	registeredAt  time.Time
	heartbeatID   string
	heartbeatAt   time.Time
	sandboxID     string
	request       PlacementRequest
	policy        PlacementPolicy
	healthyAfter  time.Time
	placement     Placement
	updatedFrom   []string
	updatedState  string
	releaseState  string
	releaseWorker string
	err           error
}

func (f *fakeStore) RegisterWorker(_ context.Context, registration WorkerRegistration, at time.Time) error {
	f.registration, f.registeredAt = registration, at
	return f.err
}

func (f *fakeStore) RecordHeartbeat(_ context.Context, id string, at time.Time) error {
	f.heartbeatID, f.heartbeatAt = id, at
	return f.err
}

func (f *fakeStore) ReservePlacement(_ context.Context, sandboxID string, request PlacementRequest, policy PlacementPolicy, healthyAfter time.Time) (Placement, error) {
	f.sandboxID, f.request, f.policy, f.healthyAfter = sandboxID, request, policy, healthyAfter
	return f.placement, f.err
}

func (f *fakeStore) GetPlacement(_ context.Context, _ string) (Placement, bool, error) {
	return f.placement, f.placement.WorkerID != "", f.err
}

func (f *fakeStore) UpdatePlacementState(_ context.Context, _, _ string, from []string, state string) error {
	f.updatedFrom, f.updatedState = from, state
	return f.err
}

func (f *fakeStore) ReleasePlacement(_ context.Context, _ string, state string) error {
	f.releaseState = state
	return f.err
}

func (f *fakeStore) ReleaseWorkerPlacement(_ context.Context, _, workerID, state string) error {
	f.releaseWorker, f.releaseState = workerID, state
	return f.err
}

type fakeWorkerClient struct {
	response  plane.CreateResponse
	createErr error
	created   plane.CreateRequest
	paused    string
	resumed   string
	destroyed string
}

func (f *fakeWorkerClient) Create(_ context.Context, request plane.CreateRequest) (plane.CreateResponse, error) {
	f.created = request
	return f.response, f.createErr
}

func (f *fakeWorkerClient) Pause(_ context.Context, id string) error {
	f.paused = id
	return nil
}

func (f *fakeWorkerClient) Resume(_ context.Context, id string) error {
	f.resumed = id
	return nil
}

func (f *fakeWorkerClient) Destroy(_ context.Context, id string) error {
	f.destroyed = id
	return nil
}

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
	svc := NewService(
		store,
		45*time.Second,
		WithPlacementPolicy(PlacementPolicy{
			CPUOvercommitRatio:    2,
			MemoryOvercommitRatio: 1,
		}),
	)
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
	if store.policy.CPUOvercommitRatio != 2 || store.policy.MemoryOvercommitRatio != 1 {
		t.Fatalf("unexpected placement policy: %+v", store.policy)
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

func TestProvisionMarksSandboxActiveAfterWorkerReady(t *testing.T) {
	store := &fakeStore{placement: Placement{
		SandboxID: "sandbox-1",
		WorkerID:  "worker-1",
		Endpoint:  "http://worker-1.internal:9876",
	}}
	worker := &fakeWorkerClient{response: plane.CreateResponse{SandboxID: "sandbox-1"}}
	service := NewService(
		store,
		time.Minute,
		WithWorkerClientFactory(func(string) WorkerClient { return worker }),
	)

	_, err := service.Provision(context.Background(), ProvisionRequest{
		CreateRequest: plane.CreateRequest{SandboxID: "sandbox-1", VCPUs: 2, MemoryMB: 1024, DiskGB: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if worker.created.SandboxID != "sandbox-1" {
		t.Fatalf("worker received sandbox id %q", worker.created.SandboxID)
	}
	if store.updatedState != "active" {
		t.Fatalf("state=%q want active", store.updatedState)
	}
}

func TestProvisionFailureReleasesCapacityAndMarksError(t *testing.T) {
	store := &fakeStore{placement: Placement{
		SandboxID: "sandbox-1",
		WorkerID:  "worker-1",
		Endpoint:  "http://worker-1.internal:9876",
	}}
	worker := &fakeWorkerClient{createErr: errors.New("guest agent did not become ready")}
	service := NewService(
		store,
		time.Minute,
		WithWorkerClientFactory(func(string) WorkerClient { return worker }),
	)

	_, err := service.Provision(context.Background(), ProvisionRequest{
		CreateRequest: plane.CreateRequest{SandboxID: "sandbox-1"},
	})
	if err == nil {
		t.Fatal("expected provisioning failure")
	}
	if store.releaseState != "error" {
		t.Fatalf("release state=%q want error", store.releaseState)
	}
}

func TestDestroyedWorkerEventReleasesOnlyReportedPlacement(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, time.Minute)

	if err := service.ReportWorkerState(context.Background(), "worker-1", "sandbox-1", "destroyed"); err != nil {
		t.Fatal(err)
	}
	if store.releaseWorker != "worker-1" || store.releaseState != "destroyed" {
		t.Fatalf("worker=%q state=%q", store.releaseWorker, store.releaseState)
	}
}

func TestLifecycleCommandsReachPlacedWorkerAndUpdateState(t *testing.T) {
	newService := func() (*Service, *fakeStore, *fakeWorkerClient) {
		store := &fakeStore{placement: Placement{
			SandboxID: "sandbox-1",
			WorkerID:  "worker-1",
			Endpoint:  "http://worker-1.internal:9876",
		}}
		worker := &fakeWorkerClient{}
		service := NewService(
			store,
			time.Minute,
			WithWorkerClientFactory(func(string) WorkerClient { return worker }),
		)
		return service, store, worker
	}

	pauseService, pauseStore, pauseWorker := newService()
	if err := pauseService.Pause(context.Background(), "sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if pauseWorker.paused != "sandbox-1" || pauseStore.updatedState != "paused" {
		t.Fatalf("pause worker=%q state=%q", pauseWorker.paused, pauseStore.updatedState)
	}

	resumeService, resumeStore, resumeWorker := newService()
	if err := resumeService.Resume(context.Background(), "sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if resumeWorker.resumed != "sandbox-1" || resumeStore.updatedState != "active" {
		t.Fatalf("resume worker=%q state=%q", resumeWorker.resumed, resumeStore.updatedState)
	}

	destroyService, destroyStore, destroyWorker := newService()
	if err := destroyService.Destroy(context.Background(), "sandbox-1"); err != nil {
		t.Fatal(err)
	}
	if destroyWorker.destroyed != "sandbox-1" ||
		destroyStore.updatedState != "destroying" ||
		destroyStore.releaseState != "destroyed" {
		t.Fatalf(
			"destroy worker=%q transition=%q final=%q",
			destroyWorker.destroyed,
			destroyStore.updatedState,
			destroyStore.releaseState,
		)
	}
}
