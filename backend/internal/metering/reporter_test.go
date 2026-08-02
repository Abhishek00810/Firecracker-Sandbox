package metering

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestReporterFlushWaitsForRecordedSamples(t *testing.T) {
	received := make(chan Batch, 1)
	client := NewClient("http://control-plane.internal", "token")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var batch Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			return nil, err
		}
		received <- batch
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Body:       io.NopCloser(bytes.NewReader(nil)),
			Header:     make(http.Header),
		}, nil
	})

	reporter := NewReporter(client, "worker-1")
	reporter.Record(Sample{SandboxID: "sandbox-1", Bucket: time.Now().UTC().Format(time.RFC3339)})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := reporter.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	batch := <-received
	if len(batch.Samples) != 1 {
		t.Fatalf("got %d samples, want 1", len(batch.Samples))
	}
	if batch.Samples[0].WorkerID != "worker-1" {
		t.Fatalf("worker id = %q, want worker-1", batch.Samples[0].WorkerID)
	}

	if err := reporter.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
