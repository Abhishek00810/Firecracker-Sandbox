package orchestrator

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPServerRequiresToken(t *testing.T) {
	server := NewHTTPServer(NewService(&fakeStore{}, time.Minute), "secret")
	req := httptest.NewRequest(http.MethodPost, "/internal/workers/worker-1/heartbeat", nil)
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=%d", res.Code, http.StatusUnauthorized)
	}
}

func TestHTTPServerRegistersWorker(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, time.Minute)
	service.now = func() time.Time {
		return time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC)
	}
	server := NewHTTPServer(service, "secret")
	body := []byte(`{
		"endpoint":"http://worker-1.internal:9876",
		"pool":"general",
		"allocatable_vcpus":96,
		"allocatable_memory_mb":180000,
		"allocatable_disk_gb":1800,
		"max_sandboxes":1200
	}`)
	req := httptest.NewRequest(http.MethodPut, "/internal/workers/worker-1", bytes.NewReader(body))
	req.Header.Set(AuthHeader, "secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if store.registration.ID != "worker-1" || store.registration.Pool != "general" {
		t.Fatalf("unexpected registration: %+v", store.registration)
	}
}

func TestHTTPServerMapsNoCapacity(t *testing.T) {
	store := &fakeStore{err: ErrNoCapacity}
	server := NewHTTPServer(NewService(store, time.Minute), "secret")
	body := []byte(`{"sandbox_id":"sandbox-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/placements", bytes.NewReader(body))
	req.Header.Set(AuthHeader, "secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}
