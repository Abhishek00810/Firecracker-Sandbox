package session

import (
	"context"
	"time"

	"backend/internal/executor"
	"backend/internal/terminal"
)

type Service interface {
	Create(ctx context.Context, userID, billingModel string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*Session, error)
	CreateWithID(ctx context.Context, sandboxID, userID, billingModel string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*Session, error)
	Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (executor.ExecutionResult, error)
	Exec(ctx context.Context, sessionID, command string, timeoutSec int) (executor.ExecutionResult, error)
	OpenTerminal(ctx context.Context, sessionID, terminalID, shell string, columns, rows uint16) error
	CloseTerminal(ctx context.Context, sessionID, terminalID string) error
	AttachTerminal(ctx context.Context, sessionID, terminalID string, stream terminal.Stream) error
	Pause(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Destroy(ctx context.Context, sessionID string) error
	GetSession(id string) (*Session, bool)
	Stats() map[string]int
}
