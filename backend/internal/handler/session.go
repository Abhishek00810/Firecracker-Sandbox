package handler

import (
	"backend/internal/session"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type CreateSessionResponse struct {
	SessionID string `json:"session_id"`
	Tier      string `json:"tier"`
	ExpiresIn string `json:"expires_in"`
}

type SessionInfoResponse struct {
	SessionID string    `json:"session_id"`
	Tier      string    `json:"tier"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
}

type RunInSessionRequest struct {
	Code     string `json:"code"`
	Language string `json:"language"`
}

// SessionHandler routes all /session and /session/:id/* requests
//
//	POST   /session          → create session
//	POST   /session/:id/run  → run code in session
//	DELETE /session/:id      → destroy session
//	GET    /session/:id      → session info
func SessionHandler(mgr *session.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// strip leading /session prefix, trim slashes
		// e.g. /session          → ""
		//      /session/abc/run  → "abc/run"
		//      /session/abc      → "abc"
		path := strings.TrimPrefix(r.URL.Path, "/session")
		path = strings.Trim(path, "/")

		switch {

		// POST /session — create new session
		case r.Method == http.MethodPost && path == "":
			tier := "premium"

			sess, err := mgr.Create(r.Context(), tier)
			if err != nil {
				slog.Error("failed to create session", "err", err)
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(CreateSessionResponse{
				SessionID: sess.ID,
				Tier:      sess.Tier,
				ExpiresIn: "15m idle timeout",
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

			result, err := mgr.Execute(r.Context(), sessionID, req.Code, req.Language)
			if err != nil {
				slog.Error("session execute failed", "session_id", sessionID, "err", err)
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)

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

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(SessionInfoResponse{
				SessionID: sess.ID,
				Tier:      sess.Tier,
				CreatedAt: sess.CreatedAt,
				LastUsed:  sess.LastUsed,
			})

		default:
			http.NotFound(w, r)
		}
	}
}
