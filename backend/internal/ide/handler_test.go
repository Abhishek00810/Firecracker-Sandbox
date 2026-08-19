package ide

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"backend/internal/plane"
)

type fakeManager struct {
	started string
	stopped string
}

func (f *fakeManager) Start(_ context.Context, sandboxID string) (Instance, error) {
	f.started = sandboxID
	return Instance{Port: 3001, State: "ready"}, nil
}

func (f *fakeManager) Status(_ context.Context, _ string) (Instance, error) {
	return Instance{Port: 3001, State: "ready"}, nil
}

func (f *fakeManager) Stop(_ context.Context, sandboxID string) error {
	f.stopped = sandboxID
	return nil
}

func TestWorkerHandlerRequiresInternalTokenAndStartsIDE(t *testing.T) {
	manager := &fakeManager{}
	mux := http.NewServeMux()
	mux.Handle(plane.RouteSandboxIDE, NewWorkerHandler(manager, "worker-secret"))

	unauthorized := httptest.NewRequest(http.MethodPost, "/worker/sandbox/sandbox-1/ide", nil)
	unauthorizedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorizedResponse.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/worker/sandbox/sandbox-1/ide", nil)
	request.Header.Set(plane.AuthHeader, "worker-secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK || manager.started != "sandbox-1" {
		t.Fatalf("status=%d started=%q body=%s", response.Code, manager.started, response.Body.String())
	}
	var instance Instance
	if err := json.NewDecoder(response.Body).Decode(&instance); err != nil {
		t.Fatal(err)
	}
	if instance.Port != 3001 || instance.State != "ready" {
		t.Fatalf("instance=%+v", instance)
	}
}
