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

func upsertSandboxAsync(w platform.UsageLogger, sb platform.Sandbox) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.UpsertSandbox(ctx, sb)
	}()
}

func updateSandboxStateAsync(w platform.UsageLogger, id, state string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		w.UpdateSandboxState(ctx, id, state)
	}()
}

// writeSandboxLogsAsync appends a sandbox's execution output (stdout/stderr) to
// sandbox_logs so the dashboard's log viewer has data. Best-effort, off the request path.
func writeSandboxLogsAsync(w platform.UsageLogger, sandboxID, userID, language, stdout, stderr string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stdout != "" {
			w.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: sandboxID, UserID: userID, Stream: "stdout", Language: language, Content: truncate(stdout, 64*1024)})
		}
		if stderr != "" {
			w.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: sandboxID, UserID: userID, Stream: "stderr", Language: language, Content: truncate(stderr, 64*1024)})
		}
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
