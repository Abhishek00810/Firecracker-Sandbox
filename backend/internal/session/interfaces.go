package session

import (
	"context"

	"backend/internal/executor"
)

type Service interface {
	Create(ctx context.Context, tier string, env map[string]string) (*Session, error)
	Execute(ctx context.Context, sessionID, code, language string) (executor.ExecutionResult, error)
	Exec(ctx context.Context, sessionID, command string, timeoutSec int) (executor.ExecutionResult, error)
	Destroy(ctx context.Context, sessionID string) error
	GetSession(id string) (*Session, bool)
}
