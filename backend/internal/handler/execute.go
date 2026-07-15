package handler

import (
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/platform"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

func ExecuteHandler(jobQueue *queue.JobQueue, limiter *ratelimit.TenantLimiter, usageLogger platform.UsageLogger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		requestID := middleware.RequestIDFromContext(r.Context())

		auth, ok := middleware.AuthFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		if !limiter.GetLimiter(auth.TenantID).Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		var req ExecuteRequest
		err := decodeJSON(w, r, &req)
		if err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		if req.Code == "" || req.Language == "" {
			http.Error(w, "code and language are required", http.StatusBadRequest)
			return
		}

		execTimeout := resolveDuration(req.Timeout, auth.Config.DefaultExecTimeout, auth.Config.MaxExecTimeout)
		execCtx, cancel := context.WithTimeout(r.Context(), execTimeout)
		defer cancel()

		resultCh, err := jobQueue.Submit(execCtx, req.Code, req.Language)
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

		insertUsageLogAsync(usageLogger, platform.UsageLog{
			APIKeyID:      auth.APIKeyID,
			UserID:        auth.TenantID,
			ExecutionType: "execute",
			Language:      req.Language,
			DurationMs:    int(duration * 1000),
			ExitCode:      int(result.Result.ExitCode),
			CostUSD:       duration * auth.Billing.ExecutionRateUSDPerSec,
			Stdout:        truncate(result.Result.Stdout, 64*1024),
			Stderr:        truncate(result.Result.Stderr, 64*1024),
		})

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
				TimeoutLimitMs:  int(execTimeout.Milliseconds()),
			},
			Tenant: &TenantContext{
				TenantID:     auth.TenantID,
				BillingModel: auth.Billing.Model,
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
		if result.Err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		json.NewEncoder(w).Encode(resp)
	}
}
