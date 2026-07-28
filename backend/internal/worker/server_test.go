package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backend/internal/executor"
	"backend/internal/session"
)

type fakeSessionService struct {
	createdID string
	session   *session.Session
}

func (f *fakeSessionService) Create(context.Context, string, string, map[string]string, int, int, int, bool, time.Duration, time.Duration) (*session.Session, error) {
	panic("worker must use CreateWithID")
}

func (f *fakeSessionService) CreateWithID(_ context.Context, sandboxID, userID, billingModel string, _ map[string]string, vcpus, memoryMB, diskGB int, internet bool, _, _ time.Duration) (*session.Session, error) {
	f.createdID = sandboxID
	f.session = &session.Session{
		ID:           sandboxID,
		UserID:       userID,
		BillingModel: billingModel,
		VCPUs:        vcpus,
		MemoryMB:     memoryMB,
		DiskGB:       diskGB,
		Internet:     internet,
		State:        session.StateActive,
	}
	return f.session, nil
}

func (f *fakeSessionService) Execute(context.Context, string, string, string, int) (executor.ExecutionResult, error) {
	return executor.ExecutionResult{}, nil
}

func (f *fakeSessionService) Exec(context.Context, string, string, int) (executor.ExecutionResult, error) {
	return executor.ExecutionResult{}, nil
}

func (f *fakeSessionService) Pause(context.Context, string) error   { return nil }
func (f *fakeSessionService) Resume(context.Context, string) error  { return nil }
func (f *fakeSessionService) Destroy(context.Context, string) error { return nil }
func (f *fakeSessionService) GetSession(string) (*session.Session, bool) {
	return f.session, f.session != nil
}

func TestCreateUsesControlPlaneSandboxID(t *testing.T) {
	service := &fakeSessionService{}
	server := NewServer(service, "worker-secret", 10)
	body, err := json.Marshal(map[string]any{
		"sandbox_id":    "fdd27ac2-c80c-4d31-ad4a-4b94834f17a8",
		"user_id":       "user-1",
		"billing_model": "payg",
		"vcpus":         2,
		"memory_mb":     1024,
		"disk_gb":       10,
		"internet":      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/worker/sandbox", bytes.NewReader(body))
	request.Header.Set("X-Worker-Token", "worker-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.createdID != "fdd27ac2-c80c-4d31-ad4a-4b94834f17a8" {
		t.Fatalf("worker created id %q", service.createdID)
	}
}

func TestCreateRequiresSandboxID(t *testing.T) {
	server := NewServer(&fakeSessionService{}, "worker-secret", 10)
	request := httptest.NewRequest(http.MethodPost, "/worker/sandbox", bytes.NewBufferString(`{"user_id":"user-1"}`))
	request.Header.Set("X-Worker-Token", "worker-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDrainingWorkerRejectsNewSandboxes(t *testing.T) {
	service := &fakeSessionService{}
	server := NewServer(service, "worker-secret", 10)
	server.BeginDrain()

	request := httptest.NewRequest(
		http.MethodPost,
		"/worker/sandbox",
		bytes.NewBufferString(`{"sandbox_id":"sandbox-1"}`),
	)
	request.Header.Set("X-Worker-Token", "worker-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.createdID != "" {
		t.Fatalf("draining worker created sandbox %q", service.createdID)
	}
}
