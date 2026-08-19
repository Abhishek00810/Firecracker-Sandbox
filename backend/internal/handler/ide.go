package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/ide"
	"backend/internal/ideauth"
	"backend/internal/middleware"
	"backend/internal/orchestrator"
	"backend/internal/plane"
)

type IDEPlacementResolver interface {
	Placement(context.Context, string) (orchestrator.Placement, error)
}

type IDEWorker interface {
	StartIDE(context.Context, string, string) (plane.IDEInstance, error)
}

type CreateIDESessionResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func CreateIDESessionHandler(
	sandboxes plane.Service,
	placements IDEPlacementResolver,
	workers IDEWorker,
	signer *ideauth.Signer,
	previewDomain string,
) http.HandlerFunc {
	previewDomain = strings.TrimSuffix(strings.TrimSpace(previewDomain), ".")
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := middleware.RequestIDFromContext(r.Context())
		auth, ok := middleware.AuthFromContext(r.Context())
		if !ok {
			writeTerminalError(w, http.StatusUnauthorized, "unauthorized", "authentication is required", requestID)
			return
		}
		sandboxID := r.PathValue("sandboxID")
		sandbox, found := sandboxes.GetSession(r.Context(), sandboxID)
		if !found || sandbox.UserID != auth.TenantID {
			writeTerminalError(w, http.StatusNotFound, "sandbox_not_found", "sandbox not found", requestID)
			return
		}
		if sandbox.State != plane.StateActive {
			writeTerminalError(w, http.StatusConflict, "sandbox_not_active", "sandbox must be active before opening the IDE", requestID)
			return
		}
		placement, err := placements.Placement(r.Context(), sandboxID)
		if err != nil || placement.Endpoint == "" || placement.State != "active" {
			writeTerminalError(w, http.StatusServiceUnavailable, "placement_unavailable", "sandbox worker placement is unavailable", requestID)
			return
		}
		instance, err := workers.StartIDE(r.Context(), placement.Endpoint, sandboxID)
		if err != nil || instance.State != "ready" || instance.Port != ide.DefaultPort {
			slog.Error("worker IDE start failed", "request_id", requestID, "sandbox_id", sandboxID, "worker_id", placement.WorkerID, "err", err)
			writeTerminalError(w, http.StatusBadGateway, "ide_start_failed", "worker failed to start the IDE", requestID)
			return
		}
		token, claims, err := signer.IssueHandoff(
			sandboxID, auth.TenantID, "", "owner", instance.Port, ideauth.DefaultHandoffTTL,
		)
		if err != nil {
			writeTerminalError(w, http.StatusInternalServerError, "ide_authorization_failed", "failed to authorize the IDE", requestID)
			return
		}
		host := strconv.Itoa(int(instance.Port)) + "-" + sandboxID + "." + previewDomain
		ideURL := url.URL{Scheme: "https", Host: host, Path: "/", RawQuery: "ro_auth=" + url.QueryEscape(token)}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreateIDESessionResponse{
			URL: ideURL.String(), ExpiresAt: time.Unix(claims.ExpiresAt, 0).UTC(),
		})
	}
}
