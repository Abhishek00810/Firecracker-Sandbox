package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/middleware"
	"backend/internal/plane"
	"backend/internal/preview"
)

type CreatePreviewRequest struct {
	ExpiresInSeconds int `json:"expires_in_seconds"`
}

type CreatePreviewResponse struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func CreatePreviewHandler(sandboxes plane.Service, signer *preview.Signer, previewDomain string) http.HandlerFunc {
	previewDomain = strings.TrimSuffix(strings.TrimSpace(previewDomain), ".")
	return func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		auth, ok := middleware.AuthFromContext(r.Context())
		if !ok {
			writeTerminalError(w, http.StatusUnauthorized, "unauthorized", "authentication is required", requestID)
			return
		}
		sandboxID := r.PathValue("sandboxID")
		sandbox, ok := sandboxes.GetSession(r.Context(), sandboxID)
		if !ok || sandbox.UserID != auth.TenantID {
			writeTerminalError(w, http.StatusNotFound, "sandbox_not_found", "sandbox not found", requestID)
			return
		}
		if sandbox.State != plane.StateActive {
			writeTerminalError(w, http.StatusConflict, "sandbox_not_active", "sandbox must be active before creating a preview", requestID)
			return
		}
		port, err := preview.ParsePort(r.PathValue("port"))
		if err != nil {
			writeTerminalError(w, http.StatusBadRequest, "invalid_preview_port", err.Error(), requestID)
			return
		}
		request := CreatePreviewRequest{ExpiresInSeconds: int(preview.DefaultTTL.Seconds())}
		if r.Body != nil && r.ContentLength != 0 {
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				writeTerminalError(w, http.StatusBadRequest, "invalid_preview_options", "invalid preview options", requestID)
				return
			}
		}
		ttl := time.Duration(request.ExpiresInSeconds) * time.Second
		token, expiresAt, err := signer.Sign(sandboxID, auth.TenantID, port, ttl)
		if err != nil {
			writeTerminalError(w, http.StatusBadRequest, "invalid_preview_options", err.Error(), requestID)
			return
		}
		host := strconv.Itoa(int(port)) + "-" + sandboxID + "." + previewDomain
		previewURL := url.URL{Scheme: "https", Host: host, Path: "/", RawQuery: "_renderops_token=" + url.QueryEscape(token)}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(CreatePreviewResponse{URL: previewURL.String(), ExpiresAt: expiresAt})
	}
}
