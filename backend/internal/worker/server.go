package worker

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"backend/internal/plane"
	"backend/internal/session"
)

// Server serves the worker HTTP API backed by a session.Service (the execution
// engine). The control plane calls it; users never reach it directly.
type Server struct {
	svc      session.Service
	token    string
	maxSlots int
	draining atomic.Bool
}

// NewServer builds a worker HTTP server. token is the internal shared secret the
// control plane must present (empty disables the check — dev only).
func NewServer(svc session.Service, token string, maxSlots int) *Server {
	return &Server{svc: svc, token: token, maxSlots: maxSlots}
}

// BeginDrain immediately rejects new sandbox creates while allowing lifecycle
// operations for existing sandboxes to finish during graceful shutdown.
func (s *Server) BeginDrain() {
	s.draining.Store(true)
}

// Handler returns the worker API mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(plane.RouteHealth, s.health)
	mux.HandleFunc(plane.RouteCapacity, s.authed(s.capacity))
	mux.HandleFunc(plane.RouteSandbox, s.authed(s.create))          // POST
	mux.HandleFunc(plane.RouteSandboxPrefix, s.authed(s.sandboxOp)) // /{id}/run|exec|pause|resume, DELETE /{id}
	return mux
}

// authed enforces the internal shared token before dispatching.
func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" && r.Header.Get(plane.AuthHeader) != s.token {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "invalid or missing worker token")
			return
		}
		next(w, r)
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	status := "ok"
	if s.draining.Load() {
		status = "draining"
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": status, "role": "worker"})
}

func (s *Server) capacity(w http.ResponseWriter, _ *http.Request) {
	if s.draining.Load() {
		writeJSON(w, http.StatusOK, plane.Capacity{FreeSlots: 0, MaxSlots: s.maxSlots})
		return
	}
	// Placeholder: report the configured max; live free-slot accounting comes with
	// the scheduler. Good enough for the control plane to know a worker exists.
	writeJSON(w, http.StatusOK, plane.Capacity{FreeSlots: s.maxSlots, MaxSlots: s.maxSlots})
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "use POST")
		return
	}
	if s.draining.Load() {
		writeErr(w, http.StatusServiceUnavailable, "worker_draining", "worker is draining")
		return
	}
	var req plane.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if req.SandboxID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "sandbox_id is required")
		return
	}
	sess, err := s.svc.CreateWithID(r.Context(), req.SandboxID, req.UserID, req.BillingModel, req.Env,
		req.VCPUs, req.MemoryMB, req.DiskGB, req.Internet,
		secs(req.IdleTimeoutS), secs(req.MaxLifetimeS))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, plane.CreateResponse{
		SandboxID: sess.ID, State: string(sess.State),
		VCPUs: sess.VCPUs, MemoryMB: sess.MemoryMB, DiskGB: sess.DiskGB,
	})
}

// sandboxOp routes /worker/sandbox/{id}[/op].
func (s *Server) sandboxOp(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, plane.RouteSandboxPrefix)
	parts := strings.SplitN(rest, "/", 2)
	id := parts[0]
	op := ""
	if len(parts) == 2 {
		op = parts[1]
	}
	if id == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing sandbox id")
		return
	}

	switch {
	case op == "run" && r.Method == http.MethodPost:
		var req plane.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		res, err := s.svc.Execute(r.Context(), id, req.Code, req.Language, req.TimeoutS)
		s.writeResult(w, res, err)

	case op == "exec" && r.Method == http.MethodPost:
		var req plane.ExecRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		res, err := s.svc.Exec(r.Context(), id, req.Command, req.TimeoutS)
		s.writeResult(w, res, err)

	case op == "pause" && r.Method == http.MethodPost:
		s.writeStatus(w, s.svc.Pause(r.Context(), id))

	case op == "resume" && r.Method == http.MethodPost:
		s.writeStatus(w, s.svc.Resume(r.Context(), id))

	case op == "" && r.Method == http.MethodDelete:
		if err := s.svc.Destroy(r.Context(), id); err != nil {
			s.writeStatus(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		writeErr(w, http.StatusNotFound, "not_found", "unknown worker operation")
	}
}

func (s *Server) writeResult(w http.ResponseWriter, res any, err error) {
	if err != nil {
		s.writeStatus(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeStatus maps a lifecycle error to an HTTP status (404 for missing session).
func (s *Server) writeStatus(w http.ResponseWriter, err error) {
	if err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	code := http.StatusInternalServerError
	if strings.Contains(err.Error(), "not found") {
		code = http.StatusNotFound
	}
	writeErr(w, code, "operation_failed", err.Error())
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, plane.ErrorResponse{Code: code, Error: msg})
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }
