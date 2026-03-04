package handler

import (
	"backend/internal/executor"
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type ExecuteRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
}

type ExecuteResponse struct {
	Output executor.ExecutionResult `json:"output"`
	Error  string                   `json:"error,omitempty"`
	Status string                   `json:"status"`
}

func ExecuteHandler(freeQueue *queue.JobQueue, premiumQueue *queue.JobQueue, freeLimiter *ratelimit.TenantLimiter, premiumLimiter *ratelimit.TenantLimiter) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.RequestIDFromContext(r.Context())

		tier := queue.TierFree
		if r.Header.Get("X-Tenant-Tier") == "premium" {
			tier = queue.TierPremium
		}

		tenantID := r.Header.Get("X-Tenant-ID")
		if tenantID == "" {
			tenantID = "anonymous"
		}
		limiter := freeLimiter
		if tier == queue.TierPremium {
			limiter = premiumLimiter
		}

		if !limiter.GetLimiter(tenantID).Allow() {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		jobQueue := freeQueue
		if tier == queue.TierPremium {
			jobQueue = premiumQueue
		}

		var req ExecuteRequest
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		resultCh, err := jobQueue.Submit(r.Context(), req.Code, req.Language, tier)
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

		resp := ExecuteResponse{
			Output: result.Result,
			Status: "success",
		}

		if result.Err != nil {
			resp.Error = result.Err.Error()
			resp.Status = "error"
		} else if result.Result.ExitCode != 0 {
			resp.Status = "error"
			resp.Error = fmt.Sprintf("Execution failed with exit code %d", result.Result.ExitCode)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}
}
