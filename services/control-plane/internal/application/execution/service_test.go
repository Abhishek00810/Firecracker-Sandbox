package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

type fakeStore struct {
	allocation Allocation
	err        error
	tenantID   string
	sandboxID  string
}

func (s *fakeStore) GetAllocation(_ context.Context, tenantID, sandboxID string) (Allocation, error) {
	s.tenantID = tenantID
	s.sandboxID = sandboxID
	return s.allocation, s.err
}

type fakeRegistry struct {
	endpoint workers.Endpoint
	err      error
	workerID string
}

func (r *fakeRegistry) GetEndpoint(_ context.Context, workerID string) (workers.Endpoint, error) {
	r.workerID = workerID
	return r.endpoint, r.err
}

type fakeClient struct {
	result    workers.ExecuteResult
	err       error
	endpoint  workers.Endpoint
	sandboxID string
	req       workers.RunRequest
}

func (c *fakeClient) Run(_ context.Context, endpoint workers.Endpoint, sandboxID string, req workers.RunRequest) (workers.ExecuteResult, error) {
	c.endpoint = endpoint
	c.sandboxID = sandboxID
	c.req = req
	return c.result, c.err
}

func TestExecuteRoutesSandboxToAssignedWorker(t *testing.T) {
	store := &fakeStore{allocation: Allocation{SandboxID: "sb-123", TenantID: "tenant-1", WorkerID: "worker-host-01"}}
	registry := &fakeRegistry{endpoint: workers.Endpoint{WorkerID: "worker-host-01", BaseURL: "https://10.0.1.20:9000"}}
	client := &fakeClient{result: workers.ExecuteResult{Stdout: "ok\n", ExitCode: 0}}
	service := NewService(store, registry, client)

	result, err := service.Execute(context.Background(), Command{
		TenantID: "tenant-1", SandboxID: "sb-123", Code: "print('ok')", Language: "python", TimeoutS: 30,
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Stdout != "ok\n" {
		t.Fatalf("Execute() = %#v", result)
	}
	if store.tenantID != "tenant-1" || store.sandboxID != "sb-123" {
		t.Fatalf("store lookup = tenant %q sandbox %q", store.tenantID, store.sandboxID)
	}
	if registry.workerID != "worker-host-01" {
		t.Fatalf("registry worker id = %q", registry.workerID)
	}
	if client.endpoint.WorkerID != "worker-host-01" || client.sandboxID != "sb-123" {
		t.Fatalf("client call = endpoint %#v sandbox %q", client.endpoint, client.sandboxID)
	}
	if client.req.Code != "print('ok')" || client.req.Language != "python" || client.req.TimeoutS != 30 {
		t.Fatalf("client request = %#v", client.req)
	}
}

func TestExecuteRejectsInvalidRequestBeforeLookup(t *testing.T) {
	store := &fakeStore{}
	service := NewService(store, &fakeRegistry{}, &fakeClient{})

	_, err := service.Execute(context.Background(), Command{TenantID: "tenant-1", SandboxID: "sb-123"})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Execute() error = %v, want ErrInvalidRequest", err)
	}
	if store.sandboxID != "" {
		t.Fatal("store called for invalid request")
	}
}

func TestExecuteRejectsSandboxWithoutWorkerAssignment(t *testing.T) {
	service := NewService(&fakeStore{allocation: Allocation{SandboxID: "sb-123", TenantID: "tenant-1"}}, &fakeRegistry{}, &fakeClient{})

	_, err := service.Execute(context.Background(), Command{TenantID: "tenant-1", SandboxID: "sb-123", Code: "1", Language: "python"})
	if !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("Execute() error = %v, want ErrSandboxNotFound", err)
	}
}
