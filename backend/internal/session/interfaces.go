package session

import (
	"context"

	"backend/internal/executor"
)

type Service interface {
	Create(ctx context.Context, tier string) (*Session, error)
	Execute(ctx context.Context, sessionID, code, language string) (executor.ExecutionResult, error)
	Destroy(ctx context.Context, sessionID string) error
	GetSession(id string) (*Session, bool)
}
