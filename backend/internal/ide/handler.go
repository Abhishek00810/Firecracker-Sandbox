package ide

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"backend/internal/plane"
)

func NewWorkerHandler(manager Manager, workerToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if manager == nil || !validWorkerToken(r.Header.Get(plane.AuthHeader), workerToken) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or missing worker token")
			return
		}
		sandboxID := strings.TrimSpace(r.PathValue("sandboxID"))
		if sandboxID == "" {
			writeError(w, http.StatusBadRequest, "bad_request", "sandbox id is required")
			return
		}

		switch r.Method {
		case http.MethodPost:
			instance, err := manager.Start(r.Context(), sandboxID)
			if err != nil {
				writeError(w, http.StatusBadGateway, "ide_start_failed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, instance)
		case http.MethodGet:
			instance, err := manager.Status(r.Context(), sandboxID)
			if err != nil {
				writeError(w, http.StatusBadGateway, "ide_status_failed", err.Error())
				return
			}
			writeJSON(w, http.StatusOK, instance)
		case http.MethodDelete:
			if err := manager.Stop(r.Context(), sandboxID); err != nil {
				writeError(w, http.StatusBadGateway, "ide_stop_failed", err.Error())
				return
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Allow", "GET, POST, DELETE")
			writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "use GET, POST, or DELETE")
		}
	})
}

func validWorkerToken(provided, expected string) bool {
	if expected == "" {
		return true
	}
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, plane.ErrorResponse{Code: code, Error: message})
}
