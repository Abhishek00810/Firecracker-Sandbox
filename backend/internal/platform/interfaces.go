package platform

import "context"

type KeyResolver interface {
	ResolveKey(keyHash string) (KeyRecord, error)
}

type UsageLogger interface {
	InsertUsageLog(ctx context.Context, log UsageLog)
}

type Service interface {
	KeyResolver
	UsageLogger
}
