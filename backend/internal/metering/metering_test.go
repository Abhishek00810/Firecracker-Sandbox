package metering

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type recordingStore struct {
	mu      sync.Mutex
	samples []Sample
}

type handlerTransport struct {
	handler http.Handler
}

func (t handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	t.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

func (s *recordingStore) AccrueUsageMeters(_ context.Context, samples []Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = append(s.samples, samples...)
	return nil
}

func validSample() Sample {
	return Sample{
		WorkerID:      "worker-1",
		SandboxID:     "sandbox-1",
		Bucket:        "2026-07-31T12:00:00Z",
		VCPUSeconds:   60,
		RAMGBSeconds:  15,
		DiskGBSeconds: 600,
	}
}

func TestHandlerRequiresWorkerToken(t *testing.T) {
	store := &recordingStore{}
	request := httptest.NewRequest(http.MethodPost, Route, nil)
	response := httptest.NewRecorder()

	Handler(store, "secret").ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestClientSubmitsValidBatch(t *testing.T) {
	store := &recordingStore{}
	client := NewClient("http://control-plane", "secret")
	client.http.Transport = handlerTransport{handler: Handler(store, "secret")}

	if err := client.Submit(context.Background(), []Sample{validSample()}); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.samples) != 1 || store.samples[0].SandboxID != "sandbox-1" {
		t.Fatalf("stored samples = %#v", store.samples)
	}
}

func TestHandlerRejectsNonMinuteBucket(t *testing.T) {
	store := &recordingStore{}
	client := NewClient("http://control-plane", "secret")
	client.http.Transport = handlerTransport{handler: Handler(store, "secret")}

	sample := validSample()
	sample.Bucket = time.Date(2026, 7, 31, 12, 0, 1, 0, time.UTC).Format(time.RFC3339)
	err := client.Submit(context.Background(), []Sample{sample})
	if err == nil {
		t.Fatal("Submit() error = nil, want validation error")
	}
}
