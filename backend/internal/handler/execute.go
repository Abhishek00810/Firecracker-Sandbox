package handler

import (
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"backend/internal/tierconfig"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)


func ExecuteHandler(freeQueue *queue.JobQueue, premiumQueue *queue.JobQueue, freeLimiter *ratelimit.TenantLimiter, premiumLimiter *ratelimit.TenantLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.RequestIDFromContext(r.Context())

		tierName := r.Header.Get("X-Tenant-Tier")
		if tierName == "" {
			tierName = tierconfig.Free
		}
		tc := tierconfig.Get(tierName)

		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "anonymous"
		}
		limiter := freeLimiter
		if tierName == tierconfig.Pro {
			limiter = premiumLimiter
		}

		if !limiter.GetLimiter(tenantID).Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		jobQueue := freeQueue
		if tierName == tierconfig.Pro {
			jobQueue = premiumQueue
		}

		var req ExecuteRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		resultCh, err := jobQueue.Submit(r.Context(), req.Code, req.Language)
		if err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}

		metrics.RecordExecutionStart()

		start := time.Now()
		result := <-resultCh
		duration := time.Since(start).Seconds()

		var errType metrics.ErrorType
		switch {
		case result.Result.TerminationReason == "timeout":
			errType = metrics.ErrorTimeout
			slog.Warn("execution timed out", "duration_s", duration, "request_id", requestID)
		case result.Result.TerminationReason == "oom_kill":
			errType = metrics.ErrorOOM
		case result.Result.TerminationReason == "runtime_error":
			errType = metrics.ErrorRuntime
		case result.Err != nil:
			errType = metrics.ErrorSystem
			slog.Error("execution system error", "err", result.Err, "request_id", requestID)
		case result.Result.ExitCode == 137 && result.Result.TerminationReason != "oom_kill":
			errType = metrics.ErrorOOM
			slog.Warn("unexpected exit code 137", "request_id", requestID)
		default:
			errType = metrics.ErrorNone
		}

		metrics.RecordExecutionEnd(duration, errType)

		output := &ExecutionOutput{
			Stdout:            result.Result.Stdout,
			Stderr:            result.Result.Stderr,
			ExitCode:          int(result.Result.ExitCode),
			TerminationReason: result.Result.TerminationReason,
			DurationMs:        duration * 1000,
			GuestDurationMs:   result.Result.GuestDuration * 1000,
		}

		resp := ExecuteResponse{
			Status:    "success",
			RequestID: requestID,
			Result:    output,
			Usage: &UsageInfo{
				ExecutionTimeMs: duration * 1000,
				QueueWaitMs:     0, // no separate queue wait tracking yet
				TimeoutLimitMs:  int(tc.MaxExecTimeout.Milliseconds()),
			},
			Tenant: &TenantContext{
				TenantID: tenantID,
				Tier:     tierName,
			},
		}

		if result.Err != nil {
			resp.Status = "error"
			resp.Error = &ErrorDetail{
				Code:    "system_error",
				Message: result.Err.Error(),
			}
		} else if result.Result.ExitCode != 0 {
			resp.Status = "error"
			resp.Error = &ErrorDetail{
				Code:    "execution_failed",
				Message: fmt.Sprintf("process exited with code %d", result.Result.ExitCode),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
