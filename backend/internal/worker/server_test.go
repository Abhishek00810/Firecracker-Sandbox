package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"backend/internal/executor"
	"backend/internal/plane"
	"backend/internal/session"
	"backend/internal/terminal"
)

type fakeSessionService struct {
	createdID      string
	session        *session.Session
	openedSandbox  string
	openedTerminal string
	closedTerminal string
	attachedInput  terminal.Frame
	attach         func(terminal.Stream) error
}

func (f *fakeSessionService) ResolvePortTarget(string) (int, error) { return 7, nil }

type fixedDialer struct{ address string }

func (d fixedDialer) DialContext(ctx context.Context, _ int, _ string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.address)
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

func (f *fakeSessionService) OpenTerminal(_ context.Context, sandboxID, terminalID, _ string, _, _ uint16) error {
	f.openedSandbox = sandboxID
	f.openedTerminal = terminalID
	return nil
}

func (f *fakeSessionService) CloseTerminal(_ context.Context, _, terminalID string) error {
	f.closedTerminal = terminalID
	return nil
}

func (f *fakeSessionService) AttachTerminal(_ context.Context, _, _ string, stream terminal.Stream) error {
	if f.attach != nil {
		return f.attach(stream)
	}
	return nil
}

func (f *fakeSessionService) Pause(context.Context, string) error   { return nil }
func (f *fakeSessionService) Resume(context.Context, string) error  { return nil }
func (f *fakeSessionService) Destroy(context.Context, string) error { return nil }
func (f *fakeSessionService) GetSession(string) (*session.Session, bool) {
	return f.session, f.session != nil
}
func (f *fakeSessionService) Stats() map[string]int {
	count := 0
	if f.session != nil {
		count = 1
	}
	return map[string]int{"active_sessions": count, "total_sessions": count}
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

func TestCapacityReportsLiveFreeSlots(t *testing.T) {
	service := &fakeSessionService{session: &session.Session{ID: "sandbox-1"}}
	server := NewServer(service, "worker-secret", 10)
	request := httptest.NewRequest(http.MethodGet, "/worker/capacity", nil)
	request.Header.Set("X-Worker-Token", "worker-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var capacity struct {
		FreeSlots int `json:"free_slots"`
		MaxSlots  int `json:"max_slots"`
	}
	if err := json.NewDecoder(response.Body).Decode(&capacity); err != nil {
		t.Fatal(err)
	}
	if capacity.FreeSlots != 9 || capacity.MaxSlots != 10 {
		t.Fatalf("capacity=%+v", capacity)
	}
}

func TestPortProxyForwardsThroughResolvedSandboxSlot(t *testing.T) {
	guest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/app/assets.js?q=1" {
			t.Fatalf("guest request URI=%q", r.URL.RequestURI())
		}
		if r.Host != "3000-sandbox.example.com" {
			t.Fatalf("guest host=%q", r.Host)
		}
		_, _ = w.Write([]byte("guest response"))
	}))
	defer guest.Close()

	server := NewServer(&fakeSessionService{}, "worker-secret", 10)
	server.dialer = fixedDialer{address: strings.TrimPrefix(guest.URL, "http://")}
	request := httptest.NewRequest(http.MethodGet, "/worker/sandbox/sandbox-1/ports/3000/app/assets.js?q=1", nil)
	request.Header.Set("X-Worker-Token", "worker-secret")
	request.Header.Set("X-Forwarded-Host", "3000-sandbox.example.com")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "guest response" {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestCreateRejectsBeforeSessionWhenLocalCapacityIsFull(t *testing.T) {
	service := &fakeSessionService{}
	admission := NewAdmission(1, 128, 1, 1, 1, 1)
	if err := admission.ReserveCreate(plane.CreateRequest{
		SandboxID: "existing", VCPUs: 1, MemoryMB: 128, DiskGB: 1,
	}); err != nil {
		t.Fatal(err)
	}
	server := NewServerWithAdmission(service, "worker-secret", admission)
	request := httptest.NewRequest(
		http.MethodPost,
		"/worker/sandbox",
		bytes.NewBufferString(`{
			"sandbox_id":"rejected",
			"vcpus":1,
			"memory_mb":128,
			"disk_gb":1
		}`),
	)
	request.Header.Set("X-Worker-Token", "worker-secret")
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if service.createdID != "" {
		t.Fatalf("session create was called for %q", service.createdID)
	}
}
