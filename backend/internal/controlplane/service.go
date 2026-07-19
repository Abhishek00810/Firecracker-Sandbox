// Package controlplane holds the control-plane side of the split architecture.
//
// Service is a plane.Service implementation with no local VMs: it dispatches
// execution to a remote host agent over SSH, while Postgres (the sandboxes
// table, owned by the sk platform's migrations) stays the source of truth for
// state, ownership, and listing.
package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"backend/internal/agent"
	"backend/internal/plane"
	"backend/internal/platform"
)

type Service struct {
	db    *platform.Client
	agent *agent.Client
}

var _ plane.Service = (*Service)(nil)

func NewService(db *platform.Client, ag *agent.Client) *Service {
	return &Service{db: db, agent: ag}
}

// Create boots the sandbox on the agent and returns the agent-assigned id; the
// handler then inserts the sandbox row under that id. If the agent fails, no
// row is written — create is all-or-nothing.
func (s *Service) Create(ctx context.Context, userID, billingModel string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*plane.SessionInfo, error) {
	resp, err := s.agent.Create(ctx, plane.CreateRequest{
		UserID:       userID,
		BillingModel: billingModel,
		Env:          env,
		VCPUs:        vcpus,
		MemoryMB:     memoryMB,
		DiskGB:       diskGB,
		Internet:     internet,
		IdleTimeoutS: int(idleTimeout.Seconds()),
		MaxLifetimeS: int(maxLifetime.Seconds()),
	})
	if err != nil {
		return nil, fmt.Errorf("provision sandbox on agent: %w", err)
	}
	now := time.Now()
	return &plane.SessionInfo{
		ID:           resp.SandboxID,
		UserID:       userID,
		BillingModel: billingModel,
		VCPUs:        resp.VCPUs,
		MemoryMB:     resp.MemoryMB,
		DiskGB:       resp.DiskGB,
		Env:          env,
		Internet:     internet,
		IdleTimeout:  idleTimeout,
		MaxLifetime:  maxLifetime,
		CreatedAt:    now,
		LastUsed:     now,
		State:        plane.StateActive,
	}, nil
}

func (s *Service) Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (plane.ExecResult, error) {
	res, err := s.agent.Run(ctx, sessionID, plane.RunRequest{Code: code, Language: language, TimeoutS: timeoutSec})
	return res, s.reconcile(ctx, sessionID, err)
}

func (s *Service) Exec(ctx context.Context, sessionID, command string, timeoutSec int) (plane.ExecResult, error) {
	res, err := s.agent.Exec(ctx, sessionID, plane.ExecRequest{Command: command, TimeoutS: timeoutSec})
	return res, s.reconcile(ctx, sessionID, err)
}

// reconcile self-heals zombie rows: if the agent no longer has the VM (e.g. it
// restarted and lost all in-memory VMs), the DB row is still "active" but dead.
// On a not-found error we mark it destroyed so it drops out of the dashboard,
// and return a clear message instead of the raw agent error.
func (s *Service) reconcile(ctx context.Context, sessionID string, err error) error {
	if err == nil || !strings.Contains(err.Error(), "not found") {
		return err
	}
	s.db.UpdateSandboxState(ctx, sessionID, "destroyed")
	return fmt.Errorf("this sandbox is no longer running (its VM was reclaimed) — create a new one")
}

func (s *Service) Pause(ctx context.Context, sessionID string) error {
	if err := s.agent.Pause(ctx, sessionID); err != nil {
		return err
	}
	return s.transition(ctx, sessionID, "paused", "sandbox paused")
}

func (s *Service) Resume(ctx context.Context, sessionID string) error {
	if err := s.agent.Resume(ctx, sessionID); err != nil {
		return err
	}
	return s.transition(ctx, sessionID, "active", "sandbox resumed")
}

func (s *Service) Destroy(ctx context.Context, sessionID string) error {
	// A VM the agent no longer knows about is already gone — that's success for
	// destroy, so mark the row destroyed rather than stranding it.
	if err := s.agent.Destroy(ctx, sessionID); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return s.transition(ctx, sessionID, "destroyed", "sandbox destroyed")
}

// GetSession reads the sandbox row; destroyed sandboxes read as gone.
func (s *Service) GetSession(ctx context.Context, id string) (*plane.SessionInfo, bool) {
	ref, ok, err := s.db.GetSandbox(ctx, id)
	if err != nil || !ok || ref.State == "destroyed" {
		return nil, false
	}
	state := plane.StateActive
	if ref.State == "paused" {
		state = plane.StatePaused
	}
	return &plane.SessionInfo{
		ID:           ref.ID,
		UserID:       ref.UserID,
		BillingModel: ref.BillingModel,
		VCPUs:        ref.VCPUs,
		MemoryMB:     ref.MemoryMB,
		DiskGB:       ref.DiskGB,
		Internet:     ref.Internet,
		CreatedAt:    ref.Created,
		LastUsed:     ref.LastUsed,
		State:        state,
	}, true
}

// transition updates the sandbox row's state synchronously (so the dashboard
// reads the new state immediately) and appends a timeline log entry. The agent
// call already happened in the caller.
func (s *Service) transition(ctx context.Context, sessionID, state, logMsg string) error {
	ref, ok, err := s.db.GetSandbox(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("look up session %s: %w", sessionID, err)
	}
	if !ok || ref.State == "destroyed" {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.db.UpdateSandboxState(ctx, sessionID, state)
	if ref.UserID != "" {
		s.db.InsertSandboxLog(ctx, platform.SandboxLog{
			SandboxID: sessionID,
			UserID:    ref.UserID,
			Stream:    "system",
			Level:     "info",
			Content:   logMsg,
		})
	}
	return nil
}
