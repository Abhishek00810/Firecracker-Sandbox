package handler

import (
	"backend/internal/platform"
	"context"
	"time"
)

func insertUsageLogAsync(logger platform.UsageLogger, log platform.UsageLog) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		logger.InsertUsageLog(ctx, log)
	}()
}

func upsertSandboxAsync(w platform.UsageLogger, sb platform.Sandbox) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		w.UpsertSandbox(ctx, sb)
	}()
}

func updateSandboxStateAsync(w platform.UsageLogger, id, state string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		w.UpdateSandboxState(ctx, id, state)
	}()
}

// recordRunAsync writes the run-history summary (sandbox_runs) and its output lines
// (sandbox_logs, tagged with the run id) so the dashboard can list runs and, per run,
// show its stdout/stderr. Best-effort, off the request path.
func recordRunAsync(w platform.UsageLogger, run platform.SandboxRun, stdout, stderr string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		run.Command = truncate(run.Command, 8*1024)
		w.InsertSandboxRun(ctx, run)
		if stdout != "" {
			w.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: run.SandboxID, RunID: run.ID, UserID: run.UserID, Stream: "stdout", Language: run.Language, Content: truncate(stdout, 64*1024)})
		}
		if stderr != "" {
			w.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: run.SandboxID, RunID: run.ID, UserID: run.UserID, Stream: "stderr", Language: run.Language, Content: truncate(stderr, 64*1024)})
		}
	}()
}

// writeSystemEventAsync records a lifecycle event (paused/resumed/destroyed) on the
// system stream so the dashboard can show a per-sandbox timeline. Best-effort.
func writeSystemEventAsync(w platform.UsageLogger, sandboxID, userID, message string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		w.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: sandboxID, UserID: userID, Stream: "system", Content: message})
	}()
}

// runStatus classifies an execution for the run-history table.
func runStatus(exitCode int, terminationReason string) string {
	if terminationReason == "timeout" {
		return "timeout"
	}
	if exitCode != 0 {
		return "error"
	}
	return "ok"
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
