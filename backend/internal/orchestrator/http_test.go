package orchestrator

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/plane"
)

func TestHTTPServerRequiresToken(t *testing.T) {
	server := NewHTTPServer(NewService(&fakeStore{}, time.Minute), "control-secret", "worker-secret")
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
	server := NewHTTPServer(service, "control-secret", "worker-secret")
	body := []byte(`{
		"endpoint":"http://worker-1.internal:9876",
		"pool":"general",
		"allocatable_vcpus":96,
		"allocatable_memory_mb":180000,
		"allocatable_disk_gb":1800,
		"max_sandboxes":1200
	}`)
	req := httptest.NewRequest(http.MethodPut, "/internal/workers/worker-1", bytes.NewReader(body))
	req.Header.Set(AuthHeader, "worker-secret")
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
	server := NewHTTPServer(NewService(store, time.Minute), "control-secret", "worker-secret")
	body := []byte(`{"sandbox_id":"sandbox-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/placements", bytes.NewReader(body))
	req.Header.Set(AuthHeader, "control-secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
}

func TestHTTPServerProvisionsSandbox(t *testing.T) {
	store := &fakeStore{placement: Placement{
		SandboxID: "sandbox-1",
		WorkerID:  "worker-1",
		Endpoint:  "http://worker-1.internal:9876",
	}}
	worker := &fakeWorkerClient{response: plane.CreateResponse{SandboxID: "sandbox-1"}}
	service := NewService(
		store,
		time.Minute,
		WithWorkerClientFactory(func(string) WorkerClient { return worker }),
	)
	server := NewHTTPServer(service, "control-secret", "worker-secret")
	body := []byte(`{
		"sandbox_id":"sandbox-1",
		"user_id":"user-1",
		"billing_model":"payg",
		"vcpus":2,
		"memory_mb":1024,
		"disk_gb":10
	}`)
	req := httptest.NewRequest(http.MethodPost, "/internal/sandboxes", bytes.NewReader(body))
	req.Header.Set(AuthHeader, "control-secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if store.updatedState != "active" {
		t.Fatalf("state=%q want active", store.updatedState)
	}
}

func TestHTTPServerSeparatesControlAndWorkerCredentials(t *testing.T) {
	server := NewHTTPServer(NewService(&fakeStore{}, time.Minute), "control-secret", "worker-secret")

	workerRequest := httptest.NewRequest(http.MethodPost, "/internal/workers/worker-1/heartbeat", nil)
	workerRequest.Header.Set(AuthHeader, "control-secret")
	workerResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(workerResponse, workerRequest)
	if workerResponse.Code != http.StatusUnauthorized {
		t.Fatalf("worker route status=%d want=%d", workerResponse.Code, http.StatusUnauthorized)
	}

	controlRequest := httptest.NewRequest(
		http.MethodPost,
		"/internal/placements",
		bytes.NewReader([]byte(`{"sandbox_id":"sandbox-1"}`)),
	)
	controlRequest.Header.Set(AuthHeader, "worker-secret")
	controlResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(controlResponse, controlRequest)
	if controlResponse.Code != http.StatusUnauthorized {
		t.Fatalf("control route status=%d want=%d", controlResponse.Code, http.StatusUnauthorized)
	}
}
