package handler

import (
	"backend/internal/middleware"
	"backend/internal/session"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// SessionHandler routes all /session and /session/:id/* requests
//
//	POST   /session          → create session
//	POST   /session/:id/run  → run code in session
//	DELETE /session/:id      → destroy session
//	GET    /session/:id      → session info
func SessionHandler(mgr *session.Manager) http.HandlerFunc {
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

			sess, err := mgr.Create(r.Context(), auth.Config.Name)
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
			if req.Code == "" || req.Language == "" {
				http.Error(w, "code and language are required", http.StatusBadRequest)
				return
			}

			start := time.Now()
			result, err := mgr.Execute(r.Context(), sessionID, req.Code, req.Language)
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
					RunCount:  0,
				},
			}

			if result.ExitCode != 0 {
				runResp.Status = "error"
				runResp.Error = &ErrorDetail{
					Code:    "execution_failed",
					Message: fmt.Sprintf("process exited with code %d", result.ExitCode),
				}
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(runResp)

		// DELETE /session/:id — destroy session
		case r.Method == http.MethodDelete && path != "":
			sessionID := path

			if err := mgr.Destroy(r.Context(), sessionID); err != nil {
				slog.Error("session destroy failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

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
					State:     "active",
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
					RunCount:         0,
					TotalExecutionMs: 0,
					LastExitCode:     nil,
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
