package handler

import (
	"backend/internal/platform"
	"context"
	"time"
)

func insertUsageLogAsync(logger platform.UsageLogger, log platform.UsageLog) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		logger.InsertUsageLog(ctx, log)
	}()
}

func truncate(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	return s[:maxBytes]
}
