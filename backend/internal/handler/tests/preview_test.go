package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"backend/internal/handler"
	"backend/internal/middleware"
	"backend/internal/plane"
	"backend/internal/platform"
	"backend/internal/preview"
)

const previewSandboxID = "1f6552e4-cf25-42b1-929b-7fd35a086f1b"

func TestCreatePreviewRequiresOwnershipAndReturnsSignedURL(t *testing.T) {
	sessions := newFakeSessionService()
	sessions.sessions[previewSandboxID] = &plane.SessionInfo{ID: previewSandboxID, UserID: "tenant-1", State: plane.StateActive}
	signer, err := preview.NewSigner(preview.DeriveSigningSecret("worker-secret"))
	if err != nil {
		t.Fatal(err)
	}
	server := newPreviewTestServer(sessions, signer)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/sandboxes/"+previewSandboxID+"/ports/3000/preview",
		strings.NewReader(`{"expires_in_seconds":3600}`),
	)
	request.Header.Set("Authorization", "Bearer ro_live_test-key")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var created handler.CreatePreviewResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(created.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "3000-"+previewSandboxID+".dev-sandbox.renderops.com" {
		t.Fatalf("preview host=%q", parsed.Host)
	}
	if _, err := signer.Verify(parsed.Query().Get("_renderops_token"), previewSandboxID, 3000); err != nil {
		t.Fatalf("verify issued token: %v", err)
	}
	if created.ExpiresAt.Before(time.Now().Add(59 * time.Minute)) {
		t.Fatalf("preview expiry=%s", created.ExpiresAt)
	}
}

func TestCreatePreviewHidesForeignSandbox(t *testing.T) {
	sessions := newFakeSessionService()
	sessions.sessions["sandbox-2"] = &plane.SessionInfo{ID: "sandbox-2", UserID: "tenant-2", State: plane.StateActive}
	signer, _ := preview.NewSigner(preview.DeriveSigningSecret("worker-secret"))
	server := newPreviewTestServer(sessions, signer)
	request := httptest.NewRequest(http.MethodPost, "/v1/sandboxes/sandbox-2/ports/3000/preview", nil)
	request.Header.Set("Authorization", "Bearer ro_live_test-key")
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func newPreviewTestServer(sessions plane.Service, signer *preview.Signer) http.Handler {
	resolver := &fakePlatformService{record: platform.KeyRecord{
		ID: "key-1", UserID: "tenant-1", IsActive: true, BalanceUSD: 10,
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/sandboxes/{sandboxID}/ports/{port}/preview", handler.CreatePreviewHandler(sessions, signer, "dev-sandbox.renderops.com"))
	return middleware.Auth(resolver, testExecutionPolicy(), testBillingConfig())(mux)
}
