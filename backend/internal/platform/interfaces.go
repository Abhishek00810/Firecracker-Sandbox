package platform

import "context"

type KeyResolver interface {
	ResolveKey(keyHash string) (KeyRecord, error)
}

type UsageLogger interface {
	InsertUsageLog(ctx context.Context, log UsageLog)
	UpsertSandbox(ctx context.Context, sb Sandbox)
	UpdateSandboxState(ctx context.Context, id, state string)
	InsertSandboxLog(ctx context.Context, l SandboxLog)
	InsertSandboxRun(ctx context.Context, run SandboxRun)
	ListSandboxes(ctx context.Context, userID string) ([]SandboxListItem, error)
}

type Service interface {
	KeyResolver
	UsageLogger
}
