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
)

var (
	ErrNoCapacity      = errors.New("no healthy worker has sufficient capacity")
	ErrWorkerNotFound  = errors.New("worker not found")
	ErrSandboxNotFound = errors.New("sandbox not found")
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

type Placement struct {
	SandboxID string `json:"sandbox_id"`
	WorkerID  string `json:"worker_id"`
	Endpoint  string `json:"endpoint"`
}

type Store interface {
	RegisterWorker(context.Context, WorkerRegistration, time.Time) error
	RecordHeartbeat(context.Context, string, time.Time) error
	ReservePlacement(context.Context, string, PlacementRequest, time.Time) (Placement, error)
	GetPlacement(context.Context, string) (Placement, bool, error)
	ReleasePlacement(context.Context, string) error
}

type Service struct {
	store        Store
	heartbeatTTL time.Duration
	now          func() time.Time
}

func NewService(store Store, heartbeatTTL time.Duration) *Service {
	if heartbeatTTL <= 0 {
		heartbeatTTL = 30 * time.Second
	}
	return &Service{store: store, heartbeatTTL: heartbeatTTL, now: time.Now}
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
	return s.store.ReservePlacement(ctx, sandboxID, request, healthyAfter)
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
	return s.store.ReleasePlacement(ctx, sandboxID)
}

func validateEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("worker endpoint %q must be an absolute http(s) URL", endpoint)
	}
	return nil
}
