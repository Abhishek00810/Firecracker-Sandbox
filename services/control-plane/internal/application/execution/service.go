package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

var (
	ErrInvalidRequest  = errors.New("invalid execution request")
	ErrSandboxNotFound = errors.New("sandbox not found")
)

type Allocation struct {
	SandboxID string
	TenantID  string
	WorkerID  string
}

type SandboxStore interface {
	GetAllocation(ctx context.Context, tenantID, sandboxID string) (Allocation, error)
}

type Service struct {
	store    SandboxStore
	registry workers.Registry
	client   workers.Runner
}

type Command struct {
	TenantID  string
	SandboxID string
	Code      string
	Language  string
	TimeoutS  int
}

func NewService(store SandboxStore, registry workers.Registry, client workers.Runner) *Service {
	return &Service{store: store, registry: registry, client: client}
}

func (s *Service) Execute(ctx context.Context, cmd Command) (workers.ExecuteResult, error) {
	if strings.TrimSpace(cmd.TenantID) == "" || strings.TrimSpace(cmd.SandboxID) == "" || strings.TrimSpace(cmd.Code) == "" || strings.TrimSpace(cmd.Language) == "" {
		return workers.ExecuteResult{}, ErrInvalidRequest
	}
	if cmd.TimeoutS < 0 {
		return workers.ExecuteResult{}, fmt.Errorf("%w: timeout must be non-negative", ErrInvalidRequest)
	}

	allocation, err := s.store.GetAllocation(ctx, cmd.TenantID, cmd.SandboxID)
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	if allocation.WorkerID == "" {
		return workers.ExecuteResult{}, fmt.Errorf("%w: sandbox %s has no worker assignment", ErrSandboxNotFound, cmd.SandboxID)
	}
	endpoint, err := s.registry.GetEndpoint(ctx, allocation.WorkerID)
	if err != nil {
		return workers.ExecuteResult{}, err
	}
	return s.client.Run(ctx, endpoint, cmd.SandboxID, workers.RunRequest{
		Code:     cmd.Code,
		Language: cmd.Language,
		TimeoutS: cmd.TimeoutS,
	})
}
