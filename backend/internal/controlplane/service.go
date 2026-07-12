// Package controlplane holds the control-plane side of the split architecture.
//
// Service is a session.Service implementation with no local VMs: the control
// plane owns auth, Postgres, and the API surface, while actual execution will
// be dispatched to remote host agents. Until agents exist, lifecycle calls
// (create/pause/resume/destroy) manage state only, and Execute/Exec return a
// clear "no agent" error.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"backend/internal/executor"
	"backend/internal/platform"
	"backend/internal/session"

	"github.com/google/uuid"
)

// ErrNoAgent is returned for execution paths until host agents are implemented.
var ErrNoAgent = errors.New("no agent host available: execution is not yet supported on the control plane")

// StateHook mirrors the session manager's hook: it syncs every lifecycle
// transition to the sandboxes table so the dashboard stays truthful.
type StateHook func(sessionID, userID, state string)

type Service struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
	hook     StateHook
}

var _ session.Service = (*Service)(nil)

func NewService(hook StateHook) *Service {
	if hook == nil {
		hook = func(string, string, string) {}
	}
	return &Service{sessions: make(map[string]*session.Session), hook: hook}
}

// Hydrate seeds the in-memory store from the sandboxes table so ownership
// checks and pause/resume/destroy keep working across control-plane restarts.
func (s *Service) Hydrate(ctx context.Context, pc *platform.Client) error {
	refs, err := pc.ListSandboxesByState(ctx, []string{"active", "paused"})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ref := range refs {
		state := session.StateActive
		if ref.State == "paused" {
			state = session.StatePaused
		}
		s.sessions[ref.ID] = &session.Session{
			ID:        ref.ID,
			UserID:    ref.UserID,
			Tier:      ref.Tier,
			VCPUs:     ref.VCPUs,
			MemoryMB:  ref.MemoryMB,
			DiskGB:    ref.DiskGB,
			Internet:  ref.Internet,
			CreatedAt: ref.Created,
			LastUsed:  ref.LastUsed,
			State:     state,
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, userID, tier string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*session.Session, error) {
	now := time.Now()
	sess := &session.Session{
		ID:          uuid.NewString(),
		UserID:      userID,
		Tier:        tier,
		VCPUs:       vcpus,
		MemoryMB:    memoryMB,
		DiskGB:      diskGB,
		Env:         env,
		Internet:    internet,
		IdleTimeout: idleTimeout,
		MaxLifetime: maxLifetime,
		CreatedAt:   now,
		LastUsed:    now,
		State:       session.StateActive,
	}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	// No hook call: the handler upserts the sandbox row on create.
	return sess, nil
}

func (s *Service) Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (executor.ExecutionResult, error) {
	return executor.ExecutionResult{}, ErrNoAgent
}

func (s *Service) Exec(ctx context.Context, sessionID, command string, timeoutSec int) (executor.ExecutionResult, error) {
	return executor.ExecutionResult{}, ErrNoAgent
}

func (s *Service) Pause(ctx context.Context, sessionID string) error {
	sess, err := s.transition(sessionID, session.StatePaused)
	if err != nil {
		return err
	}
	s.hook(sess.ID, sess.UserID, "paused")
	return nil
}

func (s *Service) Resume(ctx context.Context, sessionID string) error {
	sess, err := s.transition(sessionID, session.StateActive)
	if err != nil {
		return err
	}
	s.hook(sess.ID, sess.UserID, "active")
	return nil
}

func (s *Service) Destroy(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
	}
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	s.hook(sess.ID, sess.UserID, "destroyed")
	return nil
}

func (s *Service) GetSession(id string) (*session.Session, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *Service) transition(sessionID string, state session.SessionState) (*session.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}
	sess.State = state
	sess.LastUsed = time.Now()
	if state == session.StatePaused {
		sess.PausedAt = time.Now()
	}
	return sess, nil
}
