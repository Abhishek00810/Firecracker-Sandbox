// Package orchestrator owns durable worker registration and sandbox placement.
// It does not proxy execution traffic; the control plane uses the selected
// worker endpoint directly after placement.
package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"backend/internal/plane"
)

var (
	ErrNoCapacity      = errors.New("no healthy worker has sufficient capacity")
	ErrWorkerNotFound  = errors.New("worker not found")
	ErrSandboxNotFound = errors.New("sandbox not found")
	ErrInvalidState    = errors.New("sandbox lifecycle transition is no longer valid")
)

var workerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type WorkerRegistration struct {
	ID                  string `json:"id"`
	Endpoint            string `json:"endpoint"`
	Pool                string `json:"pool"`
	AllocatableVCPUs    int    `json:"allocatable_vcpus"`
	AllocatableMemoryMB int    `json:"allocatable_memory_mb"`
	AllocatableDiskGB   int    `json:"allocatable_disk_gb"`
	MaxSandboxes        int    `json:"max_sandboxes"`
}

type PlacementRequest struct {
	Pool string `json:"pool"`
}

// PlacementPolicy is owned by the orchestrator and cannot be overridden by an
// API caller. Workers continue to advertise their physical capacity.
type PlacementPolicy struct {
	CPUOvercommitRatio    float64
	MemoryOvercommitRatio float64
}

type Placement struct {
	SandboxID string `json:"sandbox_id"`
	WorkerID  string `json:"worker_id"`
	Endpoint  string `json:"endpoint"`
}

type ProvisionRequest struct {
	Pool string `json:"pool,omitempty"`
	plane.CreateRequest
}

type WorkerClient interface {
	Create(context.Context, plane.CreateRequest) (plane.CreateResponse, error)
	Pause(context.Context, string) error
	Resume(context.Context, string) error
	Destroy(context.Context, string) error
}

type WorkerClientFactory func(endpoint string) WorkerClient

type Store interface {
	RegisterWorker(context.Context, WorkerRegistration, time.Time) error
	RecordHeartbeat(context.Context, string, time.Time) error
	ReservePlacement(context.Context, string, PlacementRequest, PlacementPolicy, time.Time) (Placement, error)
	GetPlacement(context.Context, string) (Placement, bool, error)
	UpdatePlacementState(context.Context, string, string, []string, string) error
	ReleasePlacement(context.Context, string, string) error
	ReleaseWorkerPlacement(context.Context, string, string, string) error
}

type Service struct {
	store           Store
	heartbeatTTL    time.Duration
	now             func() time.Time
	workerClient    WorkerClientFactory
	placementPolicy PlacementPolicy
}

type Option func(*Service)

func WithWorkerClientFactory(factory WorkerClientFactory) Option {
	return func(service *Service) {
		service.workerClient = factory
	}
}

func WithPlacementPolicy(policy PlacementPolicy) Option {
	return func(service *Service) {
		service.placementPolicy = normalizePlacementPolicy(policy)
	}
}

func NewService(store Store, heartbeatTTL time.Duration, options ...Option) *Service {
	if heartbeatTTL <= 0 {
		heartbeatTTL = 30 * time.Second
	}
	service := &Service{
		store:           store,
		heartbeatTTL:    heartbeatTTL,
		now:             time.Now,
		placementPolicy: normalizePlacementPolicy(PlacementPolicy{}),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) RegisterWorker(ctx context.Context, registration WorkerRegistration) error {
	registration.ID = strings.TrimSpace(registration.ID)
	registration.Endpoint = strings.TrimRight(strings.TrimSpace(registration.Endpoint), "/")
	registration.Pool = strings.TrimSpace(registration.Pool)
	if registration.Pool == "" {
		registration.Pool = "default"
	}
	if !workerIDPattern.MatchString(registration.ID) {
		return fmt.Errorf("invalid worker id %q", registration.ID)
	}
	if err := validateEndpoint(registration.Endpoint); err != nil {
		return err
	}
	if registration.AllocatableVCPUs <= 0 ||
		registration.AllocatableMemoryMB <= 0 ||
		registration.AllocatableDiskGB <= 0 ||
		registration.MaxSandboxes <= 0 {
		return errors.New("worker allocatable resources and max_sandboxes must be positive")
	}
	return s.store.RegisterWorker(ctx, registration, s.now().UTC())
}

func (s *Service) Heartbeat(ctx context.Context, workerID string) error {
	workerID = strings.TrimSpace(workerID)
	if !workerIDPattern.MatchString(workerID) {
		return fmt.Errorf("invalid worker id %q", workerID)
	}
	return s.store.RecordHeartbeat(ctx, workerID, s.now().UTC())
}

func (s *Service) Place(ctx context.Context, sandboxID string, request PlacementRequest) (Placement, error) {
	sandboxID = strings.TrimSpace(sandboxID)
	request.Pool = strings.TrimSpace(request.Pool)
	if sandboxID == "" {
		return Placement{}, errors.New("sandbox id is required")
	}
	if request.Pool == "" {
		request.Pool = "default"
	}
	healthyAfter := s.now().UTC().Add(-s.heartbeatTTL)
	return s.store.ReservePlacement(ctx, sandboxID, request, s.placementPolicy, healthyAfter)
}

func normalizePlacementPolicy(policy PlacementPolicy) PlacementPolicy {
	if policy.CPUOvercommitRatio < 1 {
		policy.CPUOvercommitRatio = 1
	}
	if policy.MemoryOvercommitRatio < 1 {
		policy.MemoryOvercommitRatio = 1
	}
	return policy
}

func (s *Service) Placement(ctx context.Context, sandboxID string) (Placement, bool, error) {
	if strings.TrimSpace(sandboxID) == "" {
		return Placement{}, false, errors.New("sandbox id is required")
	}
	return s.store.GetPlacement(ctx, sandboxID)
}

func (s *Service) Release(ctx context.Context, sandboxID string) error {
	if strings.TrimSpace(sandboxID) == "" {
		return errors.New("sandbox id is required")
	}
	return s.store.ReleasePlacement(ctx, sandboxID, "")
}

// Provision reserves capacity before booting and finalizes the sandbox only
// after the worker confirms that its guest agent is ready.
func (s *Service) Provision(ctx context.Context, request ProvisionRequest) (Placement, error) {
	if s.workerClient == nil {
		return Placement{}, errors.New("worker client is not configured")
	}
	sandboxID := strings.TrimSpace(request.SandboxID)
	if sandboxID == "" {
		return Placement{}, errors.New("sandbox id is required")
	}

	placement, err := s.Place(ctx, sandboxID, PlacementRequest{Pool: request.Pool})
	if err != nil {
		if !errors.Is(err, ErrSandboxNotFound) {
			if releaseErr := s.store.ReleasePlacement(ctx, sandboxID, "error"); releaseErr != nil {
				return Placement{}, errors.Join(err, fmt.Errorf("mark unscheduled sandbox failed: %w", releaseErr))
			}
		}
		return Placement{}, err
	}

	response, err := s.workerClient(placement.Endpoint).Create(ctx, request.CreateRequest)
	if err != nil {
		// A timeout may happen after the worker completed the boot. Destroy by the
		// idempotency key before releasing capacity to avoid an untracked VM.
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		_ = s.workerClient(placement.Endpoint).Destroy(cleanupCtx, sandboxID)
		cancel()
		releaseErr := s.store.ReleasePlacement(ctx, sandboxID, "error")
		if releaseErr != nil {
			return Placement{}, errors.Join(
				fmt.Errorf("provision sandbox on worker: %w", err),
				fmt.Errorf("release failed placement: %w", releaseErr),
			)
		}
		return Placement{}, fmt.Errorf("provision sandbox on worker: %w", err)
	}
	if response.SandboxID != sandboxID {
		releaseErr := s.store.ReleasePlacement(ctx, sandboxID, "error")
		mismatch := fmt.Errorf("worker returned sandbox id %q, expected %q", response.SandboxID, sandboxID)
		if releaseErr != nil {
			return Placement{}, errors.Join(mismatch, fmt.Errorf("release mismatched placement: %w", releaseErr))
		}
		return Placement{}, mismatch
	}
	if err := s.store.UpdatePlacementState(ctx, sandboxID, placement.WorkerID, []string{"provisioning"}, "active"); err != nil {
		return Placement{}, fmt.Errorf("mark sandbox active: %w", err)
	}
	return placement, nil
}

func (s *Service) Pause(ctx context.Context, sandboxID string) error {
	return s.workerTransition(ctx, sandboxID, []string{"active"}, "paused", func(client WorkerClient, id string) error {
		return client.Pause(ctx, id)
	})
}

func (s *Service) Resume(ctx context.Context, sandboxID string) error {
	return s.workerTransition(ctx, sandboxID, []string{"paused"}, "active", func(client WorkerClient, id string) error {
		return client.Resume(ctx, id)
	})
}

func (s *Service) Destroy(ctx context.Context, sandboxID string) error {
	if s.workerClient == nil {
		return errors.New("worker client is not configured")
	}
	placement, ok, err := s.Placement(ctx, sandboxID)
	if err != nil {
		return err
	}
	if !ok {
		return s.store.ReleasePlacement(ctx, sandboxID, "destroyed")
	}
	if err := s.store.UpdatePlacementState(
		ctx,
		sandboxID,
		placement.WorkerID,
		[]string{"active", "paused", "error", "provisioning"},
		"destroying",
	); err != nil {
		return err
	}
	if err := s.workerClient(placement.Endpoint).Destroy(ctx, sandboxID); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "not found") {
		return fmt.Errorf("destroy sandbox on worker: %w", err)
	}
	return s.store.ReleasePlacement(ctx, sandboxID, "destroyed")
}

func (s *Service) ReportWorkerState(ctx context.Context, workerID, sandboxID, state string) error {
	workerID = strings.TrimSpace(workerID)
	sandboxID = strings.TrimSpace(sandboxID)
	state = strings.TrimSpace(state)
	if !workerIDPattern.MatchString(workerID) {
		return fmt.Errorf("invalid worker id %q", workerID)
	}
	if sandboxID == "" {
		return errors.New("sandbox id is required")
	}

	switch state {
	case "destroyed":
		return s.store.ReleaseWorkerPlacement(ctx, sandboxID, workerID, "destroyed")
	case "paused":
		return s.store.UpdatePlacementState(ctx, sandboxID, workerID, []string{"active"}, "paused")
	case "active":
		return s.store.UpdatePlacementState(ctx, sandboxID, workerID, []string{"paused", "provisioning"}, "active")
	default:
		return fmt.Errorf("invalid worker sandbox state %q", state)
	}
}

func (s *Service) workerTransition(
	ctx context.Context,
	sandboxID string,
	from []string,
	to string,
	operation func(WorkerClient, string) error,
) error {
	if s.workerClient == nil {
		return errors.New("worker client is not configured")
	}
	placement, ok, err := s.Placement(ctx, sandboxID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrSandboxNotFound
	}
	if err := operation(s.workerClient(placement.Endpoint), sandboxID); err != nil {
		return err
	}
	return s.store.UpdatePlacementState(ctx, sandboxID, placement.WorkerID, from, to)
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("worker endpoint %q must be an absolute http(s) URL", endpoint)
	}
	return nil
}
