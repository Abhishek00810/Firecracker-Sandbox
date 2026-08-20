// Package controlplane holds the control-plane side of the split architecture.
//
// Service is a plane.Service implementation with no local VMs. The
// orchestrator resolves durable placement and the control plane sends execution
// directly to the selected worker over its private HTTP endpoint.
package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"backend/internal/agent"
	"backend/internal/orchestrator"
	"backend/internal/plane"
	"backend/internal/platform"
	"backend/internal/sandboximage"

	"github.com/google/uuid"
)

type Service struct {
	db           *platform.Client
	orchestrator *orchestrator.Client
	workerToken  string
}

var _ plane.Service = (*Service)(nil)

func NewService(db *platform.Client, orchestrationClient *orchestrator.Client, workerToken string) *Service {
	return &Service{db: db, orchestrator: orchestrationClient, workerToken: workerToken}
}

// Create writes the scheduling row first, then asks the orchestrator to reserve
// a healthy worker and boot this exact UUID. The worker never writes Postgres.
func (s *Service) Create(ctx context.Context, userID, billingModel, image string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*plane.SessionInfo, error) {
	image, err := sandboximage.Normalize(image)
	if err != nil {
		return nil, err
	}
	sandboxID := uuid.NewString()
	if err := s.db.InsertSandbox(ctx, platform.Sandbox{
		ID:            sandboxID,
		UserID:        userID,
		Image:         image,
		Name:          "sandbox",
		State:         "scheduling",
		BillingModel:  billingModel,
		VCPUs:         vcpus,
		MemoryMB:      memoryMB,
		DiskGB:        diskGB,
		Internet:      internet,
		IdleTimeoutMs: int(idleTimeout.Milliseconds()),
	}); err != nil {
		return nil, fmt.Errorf("create sandbox scheduling row: %w", err)
	}

	_, err = s.orchestrator.Provision(ctx, orchestrator.ProvisionRequest{
		CreateRequest: plane.CreateRequest{
			SandboxID:    sandboxID,
			UserID:       userID,
			Image:        image,
			BillingModel: billingModel,
			Env:          env,
			VCPUs:        vcpus,
			MemoryMB:     memoryMB,
			DiskGB:       diskGB,
			Internet:     internet,
			IdleTimeoutS: int(idleTimeout.Seconds()),
			MaxLifetimeS: int(maxLifetime.Seconds()),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("provision sandbox: %w", err)
	}

	now := time.Now()
	return &plane.SessionInfo{
		ID:           sandboxID,
		UserID:       userID,
		Image:        image,
		BillingModel: billingModel,
		Env:          env,
		VCPUs:        vcpus,
		MemoryMB:     memoryMB,
		DiskGB:       diskGB,
		Internet:     internet,
		IdleTimeout:  idleTimeout,
		MaxLifetime:  maxLifetime,
		CreatedAt:    now,
		LastUsed:     now,
		State:        plane.StateActive,
	}, nil
}

func (s *Service) Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (plane.ExecResult, error) {
	worker, err := s.worker(ctx, sessionID)
	if err != nil {
		return plane.ExecResult{}, err
	}
	res, err := worker.Run(ctx, sessionID, plane.RunRequest{Code: code, Language: language, TimeoutS: timeoutSec})
	return res, s.reconcile(ctx, sessionID, err)
}

func (s *Service) Exec(ctx context.Context, sessionID, command string, timeoutSec int) (plane.ExecResult, error) {
	worker, err := s.worker(ctx, sessionID)
	if err != nil {
		return plane.ExecResult{}, err
	}
	res, err := worker.Exec(ctx, sessionID, plane.ExecRequest{Command: command, TimeoutS: timeoutSec})
	return res, s.reconcile(ctx, sessionID, err)
}

// reconcile self-heals zombie rows: if the agent no longer has the VM (e.g. it
// restarted and lost all in-memory VMs), the DB row is still "active" but dead.
// On a not-found error we mark it destroyed so it drops out of the dashboard,
// and return a clear message instead of the raw agent error.
func (s *Service) reconcile(ctx context.Context, sessionID string, err error) error {
	if err == nil {
		s.db.TouchSandbox(ctx, sessionID)
		return nil
	}
	if !strings.Contains(err.Error(), "not found") {
		return err
	}
	_ = s.orchestrator.Destroy(ctx, sessionID)
	return fmt.Errorf("this sandbox is no longer running (its VM was reclaimed) — create a new one")
}

func (s *Service) Pause(ctx context.Context, sessionID string) error {
	return s.orchestrator.Pause(ctx, sessionID)
}

func (s *Service) Resume(ctx context.Context, sessionID string) error {
	return s.orchestrator.Resume(ctx, sessionID)
}

func (s *Service) Destroy(ctx context.Context, sessionID string) error {
	return s.orchestrator.Destroy(ctx, sessionID)
}

func (s *Service) worker(ctx context.Context, sandboxID string) (*agent.Client, error) {
	placement, err := s.orchestrator.Placement(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox worker: %w", err)
	}
	return agent.NewClient(placement.Endpoint, s.workerToken), nil
}

// GetSession reads the sandbox row; destroyed sandboxes read as gone.
func (s *Service) GetSession(ctx context.Context, id string) (*plane.SessionInfo, bool) {
	ref, ok, err := s.db.GetSandbox(ctx, id)
	if err != nil || !ok {
		return nil, false
	}
	var state plane.SessionState
	switch ref.State {
	case "active":
		state = plane.StateActive
	case "paused":
		state = plane.StatePaused
	default:
		// Scheduling/provisioning/error/destroying rows remain visible in the
		// dashboard, but cannot accept execution or lifecycle commands.
		return nil, false
	}
	return &plane.SessionInfo{
		ID:           ref.ID,
		UserID:       ref.UserID,
		Image:        ref.Image,
		BillingModel: ref.BillingModel,
		VCPUs:        ref.VCPUs,
		MemoryMB:     ref.MemoryMB,
		DiskGB:       ref.DiskGB,
		Internet:     ref.Internet,
		IdleTimeout:  time.Duration(ref.IdleTimeoutMs) * time.Millisecond,
		CreatedAt:    ref.Created,
		LastUsed:     ref.LastUsed,
		State:        state,
	}, true
}
