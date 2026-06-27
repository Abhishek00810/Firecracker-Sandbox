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

// resolveDuration resolves a timeout (per-execution, idle, or lifetime) from an optional
// caller-supplied value in seconds: nil/<=0 → def; otherwise the request clamped to cap.
func resolveDuration(requestedSec *int, def, cap time.Duration) time.Duration {
	if requestedSec == nil || *requestedSec <= 0 {
		return def
	}
	d := time.Duration(*requestedSec) * time.Second
	if d > cap {
		return cap
	}
	return d
}
