package handler_test

import (
	"backend/internal/executor"
	"backend/internal/handler"
	"backend/internal/middleware"
	"backend/internal/platform"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"backend/internal/session"
	"backend/internal/tierconfig"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestExecuteHandlerRequiresAuth(t *testing.T) {
	server := newTestServer(t, testDeps{})

	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewBufferString(`{"code":"print(1)","language":"python"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestExecuteHandlerSuccess(t *testing.T) {
	server := newTestServer(t, testDeps{
		executor: fakeExecutor{
			result: executor.ExecutionResult{
				Stdout:            "2\n",
				ExitCode:          0,
				TerminationReason: "success",
				GuestDuration:     0.012,
			},
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewBufferString(`{"code":"print(1+1)","language":"python"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handler.ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "success" {
		t.Fatalf("expected success status, got %q", resp.Status)
	}
	if resp.Result == nil || resp.Result.Stdout != "2\n" {
		t.Fatalf("unexpected result payload: %+v", resp.Result)
	}
	if resp.Tenant == nil || resp.Tenant.Tier != tierconfig.PAYG {
		t.Fatalf("unexpected tenant payload: %+v", resp.Tenant)
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Fatal("expected X-Request-ID header to be set")
	}
}

func TestExecuteHandlerSystemErrorReturnsServiceUnavailable(t *testing.T) {
	server := newTestServer(t, testDeps{
		executor: fakeExecutor{
			err: errors.New("failed to acquire VM"),
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewBufferString(`{"code":"print(1+1)","language":"python"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for system error, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp handler.ExecuteResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Status != "error" {
		t.Fatalf("expected error status, got %q", resp.Status)
	}
	if resp.Error == nil || resp.Error.Code != "system_error" {
		t.Fatalf("expected system_error payload, got %+v", resp.Error)
	}
}

func TestExecuteUsageLogDoesNotUseRequestCancellationContext(t *testing.T) {
	resolver := &fakePlatformService{
		record: platform.KeyRecord{
			ID:               "key-1",
			UserID:           "tenant-1",
			Tier:             tierconfig.PAYG,
			IsActive:         true,
			FreeUSDRemaining: 10,
		},
		logCh:    make(chan error, 1),
		logDelay: 20 * time.Millisecond,
	}
	server := newTestServer(t, testDeps{resolver: resolver})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/execute", bytes.NewBufferString(`{"code":"print(1+1)","language":"python"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-key")
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)
	cancel()

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	select {
	case err := <-resolver.logCh:
		if err != nil {
			t.Fatalf("usage log used cancelled request context: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for usage log")
	}
}

func TestSessionLifecycle(t *testing.T) {
	svc := newFakeSessionService()
	server := newTestServer(t, testDeps{
		resolver: &fakePlatformService{
			record: platform.KeyRecord{
				ID:               "key-1",
				UserID:           "tenant-1",
				Tier:             tierconfig.PAYG,
				IsActive:         true,
				FreeUSDRemaining: 10,
			},
		},
		sessionSvc: svc,
	})

	createReq := httptest.NewRequest(http.MethodPost, "/session", nil)
	createReq.Header.Set("Authorization", "Bearer pro-key")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var createResp handler.CreateSessionResponse
	if err := json.NewDecoder(createRec.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if createResp.Session == nil || createResp.Session.SessionID == "" {
		t.Fatalf("unexpected create response: %+v", createResp)
	}
	sessionID := createResp.Session.SessionID

	runReq := httptest.NewRequest(http.MethodPost, "/session/"+sessionID+"/run", bytes.NewBufferString(`{"code":"print(x)","language":"python"}`))
	runReq.Header.Set("Content-Type", "application/json")
	runReq.Header.Set("Authorization", "Bearer pro-key")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)

	if runRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from run, got %d: %s", runRec.Code, runRec.Body.String())
	}

	var runResp handler.SessionExecuteResponse
	if err := json.NewDecoder(runRec.Body).Decode(&runResp); err != nil {
		t.Fatalf("decode run response: %v", err)
	}
	if runResp.Result == nil || runResp.Result.Stdout != "session output\n" {
		t.Fatalf("unexpected session run response: %+v", runResp.Result)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/session/"+sessionID, nil)
	getReq.Header.Set("Authorization", "Bearer pro-key")
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200 from get, got %d: %s", getRec.Code, getRec.Body.String())
	}

	delReq := httptest.NewRequest(http.MethodDelete, "/session/"+sessionID, nil)
	delReq.Header.Set("Authorization", "Bearer pro-key")
	delRec := httptest.NewRecorder()
	server.ServeHTTP(delRec, delReq)

	if delRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 from delete, got %d: %s", delRec.Code, delRec.Body.String())
	}
}

type testDeps struct {
	executor   fakeExecutor
	resolver   *fakePlatformService
	sessionSvc *fakeSessionService
}

func newTestServer(t *testing.T, deps testDeps) http.Handler {
	t.Helper()

	resolver := deps.resolver
	if resolver == nil || resolver.record.ID == "" {
		resolver = &fakePlatformService{
			record: platform.KeyRecord{
				ID:               "key-1",
				UserID:           "tenant-1",
				Tier:             tierconfig.PAYG,
				IsActive:         true,
				FreeUSDRemaining: 10,
			},
		}
	}

	exec := deps.executor
	if exec.result.TerminationReason == "" && exec.err == nil {
		exec = fakeExecutor{
			result: executor.ExecutionResult{
				Stdout:            "ok\n",
				ExitCode:          0,
				TerminationReason: "success",
				GuestDuration:     0.005,
			},
		}
	}

	sessionSvc := deps.sessionSvc
	if sessionSvc == nil {
		sessionSvc = newFakeSessionService()
	}

	freeQueue := queue.NewJobQueue(exec, 1)
	freeQueue.Start()
	proQueue := queue.NewJobQueue(exec, 1)
	proQueue.Start()

	freeLimiter := ratelimit.NewTenantLimiter(rate.Limit(1000), 1000)
	proLimiter := ratelimit.NewTenantLimiter(rate.Limit(1000), 1000)

	mux := http.NewServeMux()
	mux.HandleFunc("/execute", handler.ExecuteHandler(freeQueue, proQueue, freeLimiter, proLimiter, resolver))
	mux.HandleFunc("/session", handler.SessionHandler(sessionSvc, resolver))
	mux.HandleFunc("/session/", handler.SessionHandler(sessionSvc, resolver))

	return middleware.Logging(middleware.Auth(resolver)(mux))
}

type fakeExecutor struct {
	result executor.ExecutionResult
	err    error
}

func (f fakeExecutor) Execute(ctx context.Context, code string, language string) (executor.ExecutionResult, error) {
	return f.result, f.err
}

type fakePlatformService struct {
	record   platform.KeyRecord
	mu       sync.Mutex
	logs     []platform.UsageLog
	logCh    chan error
	logDelay time.Duration
}

func (f *fakePlatformService) ResolveKey(keyHash string) (platform.KeyRecord, error) {
	if f.record.ID == "" {
		return platform.KeyRecord{}, fmt.Errorf("not found")
	}
	return f.record, nil
}

func (f *fakePlatformService) InsertUsageLog(ctx context.Context, log platform.UsageLog) {
	if f.logDelay > 0 {
		time.Sleep(f.logDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logs = append(f.logs, log)
	if f.logCh != nil {
		f.logCh <- ctx.Err()
	}
}

type fakeSessionService struct {
	mu       sync.Mutex
	sessions map[string]*session.Session
}

func newFakeSessionService() *fakeSessionService {
	return &fakeSessionService{sessions: map[string]*session.Session{}}
}

func (f *fakeSessionService) Exec(ctx context.Context, sessionID, command string, timeoutSec int) (executor.ExecutionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return executor.ExecutionResult{}, fmt.Errorf("session %s not found", sessionID)
	}
	return executor.ExecutionResult{
		Stdout:            "exec output\n",
		ExitCode:          0,
		TerminationReason: "success",
	}, nil
}

func (f *fakeSessionService) Create(ctx context.Context, tier string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*session.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	id := fmt.Sprintf("sess-%d", len(f.sessions)+1)
	sess := &session.Session{
		ID:        id,
		Tier:      tier,
		CreatedAt: time.Now().UTC(),
		LastUsed:  time.Now().UTC(),
	}
	f.sessions[id] = sess
	return sess, nil
}

func (f *fakeSessionService) Execute(ctx context.Context, sessionID, code, language string) (executor.ExecutionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	sess, ok := f.sessions[sessionID]
	if !ok {
		return executor.ExecutionResult{}, fmt.Errorf("session %s not found", sessionID)
	}
	sess.LastUsed = time.Now().UTC()
	sess.RunCount++
	sess.TotalExecutionMs += 7
	exitCode := 0
	sess.LastExitCode = &exitCode
	return executor.ExecutionResult{
		Stdout:            "session output\n",
		ExitCode:          0,
		TerminationReason: "success",
		GuestDuration:     0.007,
	}, nil
}

func (f *fakeSessionService) Destroy(ctx context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[sessionID]; !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	delete(f.sessions, sessionID)
	return nil
}

func (f *fakeSessionService) GetSession(id string) (*session.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	return sess, ok
}
