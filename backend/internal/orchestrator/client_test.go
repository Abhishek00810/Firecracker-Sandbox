package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"backend/internal/plane"
)

func TestClientPreservesNoCapacityError(t *testing.T) {
	client := NewClient("http://orchestrator.internal:8090", "secret")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		payload := []byte(`{"code":"no_capacity","error":"no healthy worker has sufficient capacity"}`)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	})

	_, err := client.Provision(context.Background(), ProvisionRequest{
		CreateRequest: plane.CreateRequest{SandboxID: "sandbox-1"},
	})
	if !errors.Is(err, ErrNoCapacity) {
		t.Fatalf("error=%v want ErrNoCapacity", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestClientSendsProvisionRequestWithToken(t *testing.T) {
	client := NewClient("http://orchestrator.internal:8090", "secret")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/sandboxes" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get(AuthHeader) != "secret" {
			t.Fatalf("missing orchestrator token")
		}
		var request ProvisionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.SandboxID != "sandbox-1" {
			t.Fatalf("sandbox id=%q", request.SandboxID)
		}
		payload, err := json.Marshal(Placement{
			SandboxID: "sandbox-1",
			WorkerID:  "worker-1",
			Endpoint:  "http://worker-1.internal:9876",
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Body:       io.NopCloser(bytes.NewReader(payload)),
			Header:     make(http.Header),
		}, nil
	})

	placement, err := client.Provision(context.Background(), ProvisionRequest{
		CreateRequest: plane.CreateRequest{SandboxID: "sandbox-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if placement.WorkerID != "worker-1" {
		t.Fatalf("worker id=%q", placement.WorkerID)
	}
}

func TestClientReportsWorkerLifecycleState(t *testing.T) {
	client := NewClient("http://orchestrator.internal:8090", "secret")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/internal/workers/worker-1/sandboxes/sandbox-1/state" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["state"] != "destroyed" {
			t.Fatalf("state=%q", request["state"])
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	if err := client.ReportWorkerState(
		context.Background(),
		"worker-1",
		"sandbox-1",
		"destroyed",
	); err != nil {
		t.Fatal(err)
	}
}

func TestClientMarksWorkerDraining(t *testing.T) {
	client := NewClient("http://orchestrator.internal:8090", "secret")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/internal/workers/worker-1/draining" {
			t.Fatalf("request=%s %s", r.Method, r.URL.Path)
		}
		var request map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if !request["draining"] {
			t.Fatal("expected draining=true")
		}
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	if err := client.SetWorkerDraining(context.Background(), "worker-1", true); err != nil {
		t.Fatal(err)
	}
}
