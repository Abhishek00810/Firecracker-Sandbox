package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

func TestExecuteCallsWorkerHTTPSAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if r.URL.Path != "/worker/sandbox/sb-123/run" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if token := r.Header.Get("X-Worker-Token"); token != "worker-secret" {
			t.Errorf("worker token = %q", token)
		}
		var req workers.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if req.Code != "print('ok')" || req.Language != "python" || req.TimeoutS != 30 {
			t.Errorf("request = %#v", req)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workers.ExecuteResult{Stdout: "ok\n", ExitCode: 0, TerminationReason: "success"})
	}))
	defer server.Close()

	client := New(server.Client(), "worker-secret")
	result, err := client.Run(context.Background(), workers.Endpoint{
		WorkerID: "worker-host-01",
		BaseURL:  server.URL,
	}, "sb-123", workers.RunRequest{Code: "print('ok')", Language: "python", TimeoutS: 30})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Stdout != "ok\n" || result.ExitCode != 0 {
		t.Fatalf("Run() = %#v", result)
	}
}

func TestExecuteReturnsStructuredWorkerError(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "operation_failed", "error": "session not found"})
	}))
	defer server.Close()

	client := New(server.Client(), "worker-secret")
	_, err := client.Run(context.Background(), workers.Endpoint{WorkerID: "worker-host-01", BaseURL: server.URL}, "missing", workers.RunRequest{Code: "1", Language: "python"})
	if !errors.Is(err, workers.ErrWorkerRequest) {
		t.Fatalf("Run() error = %v, want ErrWorkerRequest", err)
	}
}

func TestCreateCallsWorkerAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/worker/sandbox" {
			t.Errorf("Create hit %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Worker-Token") != "secret" {
			t.Errorf("token = %q", r.Header.Get("X-Worker-Token"))
		}
		var req workers.CreateRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.VCPUs != 2 || req.MemoryMB != 512 {
			t.Errorf("create req = %#v", req)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(workers.CreateResponse{SandboxID: "sb-9", State: "active", VCPUs: 2, MemoryMB: 512, DiskGB: 10})
	}))
	defer server.Close()

	res, err := New(server.Client(), "secret").Create(context.Background(),
		workers.Endpoint{WorkerID: "w1", BaseURL: server.URL},
		workers.CreateRequest{UserID: "u1", VCPUs: 2, MemoryMB: 512, DiskGB: 10})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if res.SandboxID != "sb-9" || res.State != "active" {
		t.Fatalf("Create() = %#v", res)
	}
}

func TestPauseCallsWorkerAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/worker/sandbox/sb-1/pause" {
			t.Errorf("Pause hit %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	if err := New(server.Client(), "secret").Pause(context.Background(),
		workers.Endpoint{WorkerID: "w1", BaseURL: server.URL}, "sb-1"); err != nil {
		t.Fatalf("Pause() error = %v", err)
	}
}

func TestDestroyCallsWorkerAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/worker/sandbox/sb-1" {
			t.Errorf("Destroy hit %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := New(server.Client(), "secret").Destroy(context.Background(),
		workers.Endpoint{WorkerID: "w1", BaseURL: server.URL}, "sb-1"); err != nil {
		t.Fatalf("Destroy() error = %v", err)
	}
}

func TestCapacityCallsWorkerAPI(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/worker/capacity" {
			t.Errorf("Capacity hit %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(workers.Capacity{FreeSlots: 5, MaxSlots: 10})
	}))
	defer server.Close()

	capacity, err := New(server.Client(), "secret").Capacity(context.Background(),
		workers.Endpoint{WorkerID: "w1", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("Capacity() error = %v", err)
	}
	if capacity.FreeSlots != 5 || capacity.MaxSlots != 10 {
		t.Fatalf("Capacity() = %#v", capacity)
	}
}
