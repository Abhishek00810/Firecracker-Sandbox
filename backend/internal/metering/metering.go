package metering

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const Route = "/internal/usage/meters"

// Sample is one idempotent, minute-bucketed raw usage observation. Pricing and
// balance deduction deliberately happen outside this ingestion boundary.
type Sample struct {
	WorkerID      string  `json:"worker_id"`
	SandboxID     string  `json:"sandbox_id"`
	Bucket        string  `json:"bucket"`
	VCPUSeconds   float64 `json:"vcpu_seconds"`
	RAMGBSeconds  float64 `json:"ram_gb_seconds"`
	DiskGBSeconds float64 `json:"disk_gb_seconds"`
}

type Batch struct {
	Samples []Sample `json:"samples"`
}

type Store interface {
	AccrueUsageMeters(context.Context, []Sample) error
}

func Handler(store Store, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if token == "" || !validBearer(r.Header.Get("Authorization"), token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
		var batch Batch
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&batch); err != nil {
			http.Error(w, "invalid usage batch", http.StatusBadRequest)
			return
		}
		if err := ensureEOF(decoder); err != nil {
			http.Error(w, "invalid usage batch", http.StatusBadRequest)
			return
		}
		if len(batch.Samples) == 0 || len(batch.Samples) > 1000 {
			http.Error(w, "usage batch must contain 1 to 1000 samples", http.StatusBadRequest)
			return
		}
		for _, sample := range batch.Samples {
			if err := validate(sample); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		if err := store.AccrueUsageMeters(r.Context(), batch.Samples); err != nil {
			http.Error(w, "failed to persist usage batch", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func validate(sample Sample) error {
	if strings.TrimSpace(sample.WorkerID) == "" || strings.TrimSpace(sample.SandboxID) == "" {
		return errors.New("worker_id and sandbox_id are required")
	}
	bucket, err := time.Parse(time.RFC3339, sample.Bucket)
	if err != nil || !bucket.Equal(bucket.UTC().Truncate(time.Minute)) {
		return errors.New("bucket must be a UTC RFC3339 minute boundary")
	}
	if sample.VCPUSeconds < 0 || sample.RAMGBSeconds < 0 || sample.DiskGBSeconds < 0 {
		return errors.New("usage values cannot be negative")
	}
	return nil
}

func validBearer(header, token string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if len(provided) != len(token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(token)) == 1
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected trailing JSON")
		}
		return fmt.Errorf("read trailing JSON: %w", err)
	}
	return nil
}
