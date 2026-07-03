package handler

import (
	"backend/internal/middleware"
	"backend/internal/platform"
	"backend/internal/session"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SessionHandler routes all /session and /session/:id/* requests
//
//	POST   /session          → create session
//	POST   /session/:id/run  → run code in session
//	DELETE /session/:id      → destroy session
//	GET    /session/:id      → session info
func SessionHandler(mgr session.Service, usageLogger platform.UsageLogger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.RequestIDFromContext(r.Context())

		auth, ok := middleware.AuthFromContext(r.Context())
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		tc := auth.Config

		// strip leading /session prefix, trim slashes
		// e.g. /session          → ""
		//      /session/abc/run  → "abc/run"
		//      /session/abc      → "abc"
		path := strings.TrimPrefix(r.URL.Path, "/session")
		path = strings.Trim(path, "/")

		switch {

		// POST /session — create new session
		case r.Method == http.MethodPost && path == "":
			if tc.MaxSessions == 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(APIError{
					Status:    "error",
					Code:      "tier_not_allowed",
					Message:   "sessions are not available on the free tier",
					RequestID: requestID,
				})
				return
			}

			var createReq CreateSessionRequest
			json.NewDecoder(r.Body).Decode(&createReq)

			size, err := resolveSize(createReq.Resources)
			if err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(APIError{
					Status:    "error",
					Code:      "invalid_resources",
					Message:   err.Error(),
					RequestID: requestID,
				})
				return
			}

			internet := true // default: egress on
			if createReq.Network != nil && createReq.Network.Internet != nil {
				internet = *createReq.Network.Internet
			}

			// Resolve per-session idle/lifetime: caller may request values; idle is capped
			// at the max lifetime, lifetime is capped at the tier ceiling. Unset → tier defaults.
			idleTimeout := resolveDuration(createReq.IdleTimeoutS, tc.SessionIdleTimeout, tc.SessionMaxLifetime)
			maxLifetime := resolveDuration(createReq.MaxLifetimeS, tc.SessionMaxLifetime, tc.SessionMaxLifetime)

			sess, err := mgr.Create(r.Context(), auth.Config.Name, createReq.Env, size.VCPUs, size.MemoryMB, size.DiskGB, internet, idleTimeout, maxLifetime)
			if err != nil {
				slog.Error("failed to create session", "err", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(CreateSessionResponse{
				Status:    "success",
				RequestID: requestID,
				Session: &SessionDetail{
					SessionID: sess.ID,
					State:     "active",
					Tier:      sess.Tier,
					CreatedAt: sess.CreatedAt.Format(time.RFC3339),
					LastUsed:  sess.LastUsed.Format(time.RFC3339),
					ExpiresAt: sess.LastUsed.Add(tc.SessionIdleTimeout).Format(time.RFC3339),
				},
				Limits: &SessionLimits{
					MaxSessions:    tc.MaxSessions,
					ActiveSessions: 0, // no per-tenant count yet
					MaxExecutionMs: int(tc.MaxExecTimeout.Milliseconds()),
					IdleTimeoutMs:  int(tc.SessionIdleTimeout.Milliseconds()),
				},
				Tenant: &TenantContext{
					TenantID: auth.TenantID,
					Tier:     auth.Config.Name,
				},
			})

			insertUsageLogAsync(usageLogger, platform.UsageLog{
				APIKeyID:      auth.APIKeyID,
				UserID:        auth.TenantID,
				ExecutionType: "session_create",
				Language:      "",
				DurationMs:    0,
				ExitCode:      0,
				CostUSD:       0.0,
			})

			// Register the sandbox in the DB (the dashboard's source of truth for listing).
			name := createReq.Name
			if name == "" {
				name = "sandbox"
			}
			diskGB := size.DiskGB
			if diskGB == 0 {
				diskGB = 10 // actual provisioned per-VM disk until per-size templates exist
			}
			upsertSandboxAsync(usageLogger, platform.Sandbox{
				ID:       sess.ID,
				UserID:   auth.TenantID,
				APIKeyID: auth.APIKeyID,
				Name:     name,
				State:    "active",
				Tier:     auth.Config.Name,
				VCPUs:    size.VCPUs,
				MemoryMB: size.MemoryMB,
				DiskGB:   diskGB,
				Internet: internet,
			})

		// POST /session/:id/run — execute code in session
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/run"):
			sessionID := strings.TrimSuffix(path, "/run")
			if sessionID == "" {
				http.Error(w, "missing session id", http.StatusBadRequest)
				return
			}

			var req RunInSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if req.Code == "" {
				http.Error(w, "code is required", http.StatusBadRequest)
				return
			}
			if req.Language == "" {
				http.Error(w, "language is required", http.StatusBadRequest)
				return
			}

			runTimeoutSec := int(resolveDuration(req.Timeout, auth.Config.DefaultExecTimeout, auth.Config.MaxExecTimeout).Seconds())

			start := time.Now()
			result, err := mgr.Execute(r.Context(), sessionID, req.Code, req.Language, runTimeoutSec)
			if err != nil {
				slog.Error("session execute failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			execDurationMs := time.Since(start).Seconds() * 1000

			sessSt, _ := mgr.GetSession(sessionID)
			sessTc := auth.Config

			output := &ExecutionOutput{
				Stdout:            result.Stdout,
				Stderr:            result.Stderr,
				ExitCode:          int(result.ExitCode),
				TerminationReason: result.TerminationReason,
				DurationMs:        execDurationMs,
				GuestDurationMs:   result.GuestDuration * 1000,
			}

			runResp := SessionExecuteResponse{
				Status:    "success",
				RequestID: requestID,
				SessionID: sessionID,
				Result:    output,
				Usage: &UsageInfo{
					ExecutionTimeMs: result.GuestDuration * 1000,
					QueueWaitMs:     0,
					TimeoutLimitMs:  int(sessTc.MaxExecTimeout.Milliseconds()),
				},
				Tenant: &TenantContext{
					TenantID: auth.TenantID,
					Tier:     auth.Config.Name,
				},
				Session: &SessionState{
					State:     "active",
					LastUsed:  sessSt.LastUsed.Format(time.RFC3339),
					ExpiresAt: sessSt.LastUsed.Add(sessTc.SessionIdleTimeout).Format(time.RFC3339),
					RunCount:  sessSt.RunCount,
				},
			}

			if result.ExitCode != 0 {
				runResp.Status = "error"
				runResp.Error = &ErrorDetail{
					Code:    "execution_failed",
					Message: fmt.Sprintf("process exited with code %d", result.ExitCode),
				}
			}

			insertUsageLogAsync(usageLogger, platform.UsageLog{
				APIKeyID:      auth.APIKeyID,
				UserID:        auth.TenantID,
				ExecutionType: "session_run",
				Language:      req.Language,
				DurationMs:    int(execDurationMs),
				ExitCode:      int(result.ExitCode),
				CostUSD:       (execDurationMs / 1000.0) * auth.RateUSDPerSec,
				Stdout:        truncate(result.Stdout, 64*1024),
				Stderr:        truncate(result.Stderr, 64*1024),
			})
			recordRunAsync(usageLogger, platform.SandboxRun{
				ID:         uuid.NewString(),
				SandboxID:  sessionID,
				UserID:     auth.TenantID,
				Kind:       "run",
				Language:   req.Language,
				Command:    req.Code,
				ExitCode:   int(result.ExitCode),
				Status:     runStatus(int(result.ExitCode), result.TerminationReason),
				DurationMs: int(execDurationMs),
				StartedAt:  start.UTC().Format(time.RFC3339Nano),
			}, result.Stdout, result.Stderr)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(runResp)

		// POST /session/:id/exec — run a shell command in the session's persistent bash
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/exec"):
			sessionID := strings.TrimSuffix(path, "/exec")
			if sessionID == "" {
				http.Error(w, "missing session id", http.StatusBadRequest)
				return
			}

			var req ExecInSessionRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			if req.Command == "" {
				http.Error(w, "command is required", http.StatusBadRequest)
				return
			}

			start := time.Now()
			result, err := mgr.Exec(r.Context(), sessionID, req.Command, req.Timeout)
			if err != nil {
				slog.Error("session exec failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			execDurationMs := time.Since(start).Seconds() * 1000

			sessSt, _ := mgr.GetSession(sessionID)
			sessTc := auth.Config

			output := &ExecutionOutput{
				Stdout:            result.Stdout,
				Stderr:            result.Stderr,
				ExitCode:          int(result.ExitCode),
				TerminationReason: result.TerminationReason,
				DurationMs:        execDurationMs,
				GuestDurationMs:   result.GuestDuration * 1000,
			}

			execResp := SessionExecuteResponse{
				Status:    "success",
				RequestID: requestID,
				SessionID: sessionID,
				Result:    output,
				Usage: &UsageInfo{
					ExecutionTimeMs: result.GuestDuration * 1000,
					QueueWaitMs:     0,
					TimeoutLimitMs:  int(sessTc.MaxExecTimeout.Milliseconds()),
				},
				Tenant: &TenantContext{
					TenantID: auth.TenantID,
					Tier:     auth.Config.Name,
				},
				Session: &SessionState{
					State:     "active",
					LastUsed:  sessSt.LastUsed.Format(time.RFC3339),
					ExpiresAt: sessSt.LastUsed.Add(sessTc.SessionIdleTimeout).Format(time.RFC3339),
					RunCount:  sessSt.RunCount,
				},
			}

			if result.ExitCode != 0 {
				execResp.Status = "error"
				execResp.Error = &ErrorDetail{
					Code:    "exec_failed",
					Message: fmt.Sprintf("command exited with code %d", result.ExitCode),
				}
			}

			insertUsageLogAsync(usageLogger, platform.UsageLog{
				APIKeyID:      auth.APIKeyID,
				UserID:        auth.TenantID,
				ExecutionType: "session_exec",
				Language:      "bash",
				DurationMs:    int(execDurationMs),
				ExitCode:      int(result.ExitCode),
				CostUSD:       (execDurationMs / 1000.0) * auth.RateUSDPerSec,
				Stdout:        truncate(result.Stdout, 64*1024),
				Stderr:        truncate(result.Stderr, 64*1024),
			})
			recordRunAsync(usageLogger, platform.SandboxRun{
				ID:         uuid.NewString(),
				SandboxID:  sessionID,
				UserID:     auth.TenantID,
				Kind:       "exec",
				Language:   "bash",
				Command:    req.Command,
				ExitCode:   int(result.ExitCode),
				Status:     runStatus(int(result.ExitCode), result.TerminationReason),
				DurationMs: int(execDurationMs),
				StartedAt:  start.UTC().Format(time.RFC3339Nano),
			}, result.Stdout, result.Stderr)

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(execResp)

			// POST /session/:id/pause — pause session (snapshot to disk, free RAM/slot)
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/pause"):
			sessionID := strings.TrimSuffix(path, "/pause")
			if sessionID == "" {
				http.Error(w, "missing session id", http.StatusBadRequest)
				return
			}
			if err := mgr.Pause(r.Context(), sessionID); err != nil {
				slog.Error("session pause failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			updateSandboxStateAsync(usageLogger, sessionID, "paused")
			writeSystemEventAsync(usageLogger, sessionID, auth.TenantID, "sandbox paused")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "success", "session_id": sessionID, "state": "paused"})

			// POST /session/:id/resume — resume a paused session
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/resume"):
			sessionID := strings.TrimSuffix(path, "/resume")
			if sessionID == "" {
				http.Error(w, "missing session id", http.StatusBadRequest)
				return
			}
			if err := mgr.Resume(r.Context(), sessionID); err != nil {
				slog.Error("session resume failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			updateSandboxStateAsync(usageLogger, sessionID, "active")
			writeSystemEventAsync(usageLogger, sessionID, auth.TenantID, "sandbox resumed")
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"status": "success", "session_id": sessionID, "state": "active"})

			// DELETE /session/:id — destroy session
		case r.Method == http.MethodDelete && path != "":
			sessionID := path

			if err := mgr.Destroy(r.Context(), sessionID); err != nil {
				slog.Error("session destroy failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			updateSandboxStateAsync(usageLogger, sessionID, "destroyed")
			writeSystemEventAsync(usageLogger, sessionID, auth.TenantID, "sandbox destroyed")

			w.WriteHeader(http.StatusNoContent)

			// GET /session/:id — session info
		case r.Method == http.MethodGet && path != "":
			sessionID := path

			sess, ok := mgr.GetSession(sessionID)
			if !ok {
				http.Error(w, "session not found", http.StatusNotFound)
				return
			}

			sessTc := auth.Config
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SessionInfoResponse{
				Status:    "success",
				RequestID: requestID,
				Session: &SessionDetail{
					SessionID: sess.ID,
					State:     string(sess.State),
					Tier:      sess.Tier,
					CreatedAt: sess.CreatedAt.Format(time.RFC3339),
					LastUsed:  sess.LastUsed.Format(time.RFC3339),
					ExpiresAt: sess.LastUsed.Add(sessTc.SessionIdleTimeout).Format(time.RFC3339),
				},
				Limits: &SessionLimits{
					MaxSessions:    sessTc.MaxSessions,
					ActiveSessions: 0, // no per-tenant count yet
					MaxExecutionMs: int(sessTc.MaxExecTimeout.Milliseconds()),
					IdleTimeoutMs:  int(sessTc.SessionIdleTimeout.Milliseconds()),
				},
				Stats: &SessionStats{
					RunCount:         sess.RunCount,
					TotalExecutionMs: sess.TotalExecutionMs,
					LastExitCode:     sess.LastExitCode,
				},
				Tenant: &TenantContext{
					TenantID: auth.TenantID,
					Tier:     sess.Tier,
				},
			})

		default:
			http.NotFound(w, r)
		}
	}
}
