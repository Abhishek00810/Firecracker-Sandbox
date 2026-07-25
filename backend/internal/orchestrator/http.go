package orchestrator

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

const AuthHeader = "X-Orchestrator-Token"

type HTTPServer struct {
	service *Service
	token   string
}

func NewHTTPServer(service *Service, token string) *HTTPServer {
	return &HTTPServer{service: service, token: token}
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("PUT /internal/workers/{workerID}", s.authed(s.registerWorker))
	mux.HandleFunc("POST /internal/workers/{workerID}/heartbeat", s.authed(s.heartbeat))
	mux.HandleFunc("POST /internal/placements", s.authed(s.place))
	mux.HandleFunc("GET /internal/placements/{sandboxID}", s.authed(s.getPlacement))
	mux.HandleFunc("DELETE /internal/placements/{sandboxID}", s.authed(s.releasePlacement))
	return mux
}

func (s *HTTPServer) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || r.Header.Get(AuthHeader) != s.token {
			writeHTTPError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing orchestrator token")
			return
		}
		next(w, r)
	}
}

func (s *HTTPServer) health(w http.ResponseWriter, _ *http.Request) {
	writeHTTPJSON(w, http.StatusOK, map[string]string{"status": "ok", "role": "orchestrator"})
}

func (s *HTTPServer) registerWorker(w http.ResponseWriter, r *http.Request) {
	var registration WorkerRegistration
	if err := decodeHTTPJSON(w, r, &registration); err != nil {
		return
	}
	pathID := r.PathValue("workerID")
	if registration.ID != "" && registration.ID != pathID {
		writeHTTPError(w, http.StatusBadRequest, "worker_id_mismatch", "body worker id does not match request path")
		return
	}
	registration.ID = pathID
	if err := s.service.RegisterWorker(r.Context(), registration); err != nil {
		writeServiceError(w, err)
		return
	}
	writeHTTPJSON(w, http.StatusOK, map[string]string{"status": "registered", "worker_id": registration.ID})
}

func (s *HTTPServer) heartbeat(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Heartbeat(r.Context(), r.PathValue("workerID")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *HTTPServer) place(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SandboxID string `json:"sandbox_id"`
		PlacementRequest
	}
	if err := decodeHTTPJSON(w, r, &request); err != nil {
		return
	}
	placement, err := s.service.Place(r.Context(), request.SandboxID, request.PlacementRequest)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeHTTPJSON(w, http.StatusOK, placement)
}

func (s *HTTPServer) getPlacement(w http.ResponseWriter, r *http.Request) {
	placement, ok, err := s.service.Placement(r.Context(), r.PathValue("sandboxID"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !ok {
		writeHTTPError(w, http.StatusNotFound, "placement_not_found", "sandbox has no placement")
		return
	}
	writeHTTPJSON(w, http.StatusOK, placement)
}

func (s *HTTPServer) releasePlacement(w http.ResponseWriter, r *http.Request) {
	if err := s.service.Release(r.Context(), r.PathValue("sandboxID")); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeHTTPJSON(w http.ResponseWriter, r *http.Request, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return err
	}
	return nil
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNoCapacity):
		writeHTTPError(w, http.StatusServiceUnavailable, "no_capacity", err.Error())
	case errors.Is(err, ErrWorkerNotFound):
		writeHTTPError(w, http.StatusNotFound, "worker_not_found", err.Error())
	case errors.Is(err, ErrSandboxNotFound):
		writeHTTPError(w, http.StatusNotFound, "sandbox_not_found", err.Error())
	default:
		status := http.StatusInternalServerError
		if strings.Contains(err.Error(), "invalid") ||
			strings.Contains(err.Error(), "required") ||
			strings.Contains(err.Error(), "must be") {
			status = http.StatusBadRequest
		}
		writeHTTPError(w, status, "orchestration_failed", err.Error())
	}
}

func writeHTTPError(w http.ResponseWriter, status int, code, message string) {
	writeHTTPJSON(w, status, map[string]string{"code": code, "error": message})
}

func writeHTTPJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
