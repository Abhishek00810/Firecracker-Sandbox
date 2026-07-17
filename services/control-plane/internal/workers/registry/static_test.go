package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers"
)

func TestStaticRegistryResolvesConfiguredWorker(t *testing.T) {
	registry, err := NewStatic("worker-host-01", "https://10.0.1.20:9000/")
	if err != nil {
		t.Fatalf("NewStatic() error = %v", err)
	}

	endpoint, err := registry.GetEndpoint(context.Background(), "worker-host-01")
	if err != nil {
		t.Fatalf("GetEndpoint() error = %v", err)
	}
	if endpoint.WorkerID != "worker-host-01" || endpoint.BaseURL != "https://10.0.1.20:9000" {
		t.Fatalf("GetEndpoint() = %#v", endpoint)
	}
}

func TestStaticRegistryRejectsUnknownWorker(t *testing.T) {
	registry, err := NewStatic("worker-host-01", "https://10.0.1.20:9000")
	if err != nil {
		t.Fatalf("NewStatic() error = %v", err)
	}

	_, err = registry.GetEndpoint(context.Background(), "worker-host-02")
	if !errors.Is(err, workers.ErrWorkerNotFound) {
		t.Fatalf("GetEndpoint() error = %v, want ErrWorkerNotFound", err)
	}
}

func TestStaticRegistryAllowsPlainHTTPOnlyOnLoopback(t *testing.T) {
	if _, err := NewStatic("worker-local", "http://127.0.0.1:9000"); err != nil {
		t.Fatalf("loopback HTTP rejected: %v", err)
	}
	if _, err := NewStatic("worker-remote", "http://10.0.1.20:9000"); err == nil {
		t.Fatal("remote HTTP URL accepted")
	}
}
