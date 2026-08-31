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

func TestHTTPServerAssignsAndPreservesRequestID(t *testing.T) {
	server := NewHTTPServer(NewService(&fakeStore{}, time.Minute), "control-secret", "worker-secret")

	generatedRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	generatedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(generatedResponse, generatedRequest)
	if generatedResponse.Header().Get(RequestIDHeader) == "" {
		t.Fatal("expected generated request id")
	}

	forwardedRequest := httptest.NewRequest(http.MethodGet, "/health", nil)
	forwardedRequest.Header.Set(RequestIDHeader, "request-123")
	forwardedResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(forwardedResponse, forwardedRequest)
	if got := forwardedResponse.Header().Get(RequestIDHeader); got != "request-123" {
		t.Fatalf("request id=%q want request-123", got)
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

func TestHTTPServerRecordsWorkerCapacityHeartbeat(t *testing.T) {
	store := &fakeStore{}
	server := NewHTTPServer(NewService(store, time.Minute), "control-secret", "worker-secret")
	req := httptest.NewRequest(
		http.MethodPost,
		"/internal/workers/worker-1/heartbeat",
		bytes.NewBufferString(`{
			"reserved_vcpus":7,
			"reserved_memory_mb":896,
			"reserved_disk_gb":12,
			"reserved_sandboxes":7
		}`),
	)
	req.Header.Set(AuthHeader, "worker-secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if store.heartbeatID != "worker-1" ||
		store.heartbeat.ReservedVCPUs != 7 ||
		store.heartbeat.ReservedSandboxes != 7 {
		t.Fatalf("heartbeat id=%q capacity=%+v", store.heartbeatID, store.heartbeat)
	}
}

func TestHTTPServerMarksWorkerDraining(t *testing.T) {
	store := &fakeStore{}
	server := NewHTTPServer(NewService(store, time.Minute), "control-secret", "worker-secret")
	req := httptest.NewRequest(
		http.MethodPost,
		"/internal/workers/worker-1/draining",
		bytes.NewBufferString(`{"draining":true}`),
	)
	req.Header.Set(AuthHeader, "worker-secret")
	res := httptest.NewRecorder()

	server.Handler().ServeHTTP(res, req)

	if res.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	if store.drainingID != "worker-1" || !store.draining {
		t.Fatalf("worker=%q draining=%v", store.drainingID, store.draining)
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
