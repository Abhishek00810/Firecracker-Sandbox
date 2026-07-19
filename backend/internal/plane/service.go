package plane

import (
	"context"
	"time"
)

// Service is the sandbox lifecycle surface the API handlers program against,
// expressed purely in plane types. The control plane implements it by
// dispatching to host agents; host-side engines implement it with real VMs.
type Service interface {
	Create(ctx context.Context, userID, billingModel string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*SessionInfo, error)
	Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (ExecResult, error)
	Exec(ctx context.Context, sessionID, command string, timeoutSec int) (ExecResult, error)
	Pause(ctx context.Context, sessionID string) error
	Resume(ctx context.Context, sessionID string) error
	Destroy(ctx context.Context, sessionID string) error
	GetSession(ctx context.Context, id string) (*SessionInfo, bool)
}
