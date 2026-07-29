package platform

import "context"

type KeyResolver interface {
	ResolveKey(keyHash string) (KeyRecord, error)
}

type SessionResolver interface {
	ResolveSession(token string) (SessionRecord, error)
}

type UsageLogger interface {
	InsertUsageLog(ctx context.Context, log UsageLog)
	UpsertSandbox(ctx context.Context, sb Sandbox)
	UpdateSandboxDetails(ctx context.Context, sb Sandbox) error
	UpdateSandboxState(ctx context.Context, id, state string)
	InsertSandboxLog(ctx context.Context, l SandboxLog)
	InsertSandboxRun(ctx context.Context, run SandboxRun)
	ListSandboxes(ctx context.Context, userID string) ([]SandboxListItem, error)
	// BillSandboxRuntime debits the owner's balance for unbilled wall-clock time.
	BillSandboxRuntime(ctx context.Context, sandboxID string, ratePerSec float64)
}

type Service interface {
	KeyResolver
	SessionResolver
	UsageLogger
}
