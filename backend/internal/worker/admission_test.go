package worker

import (
	"errors"
	"sync"
	"testing"

	"backend/internal/plane"
)

func TestAdmissionAtomicallyRejectsBeyondCapacity(t *testing.T) {
	admission := NewAdmission(2, 1024, 10, 10, 1, 1)
	requests := []plane.CreateRequest{
		{SandboxID: "a", VCPUs: 1, MemoryMB: 512, DiskGB: 5},
		{SandboxID: "b", VCPUs: 1, MemoryMB: 512, DiskGB: 5},
		{SandboxID: "c", VCPUs: 1, MemoryMB: 512, DiskGB: 5},
	}

	var wg sync.WaitGroup
	results := make(chan error, len(requests))
	for _, request := range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- admission.ReserveCreate(request)
		}()
	}
	wg.Wait()
	close(results)

	accepted := 0
	rejected := 0
	for err := range results {
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, plane.ErrNoCapacity):
			rejected++
		default:
			t.Fatalf("unexpected admission error: %v", err)
		}
	}
	if accepted != 2 || rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d", accepted, rejected)
	}
}

func TestAdmissionPauseRetainsDiskAndReleasesCompute(t *testing.T) {
	admission := NewAdmission(2, 1024, 10, 10, 1, 1)
	request := plane.CreateRequest{SandboxID: "sandbox-1", VCPUs: 2, MemoryMB: 1024, DiskGB: 10}
	if err := admission.ReserveCreate(request); err != nil {
		t.Fatal(err)
	}

	admission.MarkPaused(request.SandboxID)
	capacity := admission.Capacity()
	if capacity.ReservedVCPUs != 0 || capacity.ReservedMemoryMB != 0 {
		t.Fatalf("paused compute still reserved: %+v", capacity)
	}
	if capacity.ReservedDiskGB != 10 || capacity.ReservedSandboxes != 1 {
		t.Fatalf("paused durable resources were released: %+v", capacity)
	}

	if err := admission.ReserveResume(request.SandboxID); err != nil {
		t.Fatal(err)
	}
	capacity = admission.Capacity()
	if capacity.ReservedVCPUs != 2 || capacity.ReservedMemoryMB != 1024 {
		t.Fatalf("resume did not restore compute reservation: %+v", capacity)
	}
}

func TestAdmissionAppliesCPUOvercommitOnly(t *testing.T) {
	admission := NewAdmission(2, 1024, 10, 10, 4, 1)
	for i := 0; i < 8; i++ {
		request := plane.CreateRequest{
			SandboxID: string(rune('a' + i)), VCPUs: 1, MemoryMB: 128, DiskGB: 1,
		}
		if err := admission.ReserveCreate(request); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if err := admission.ReserveCreate(plane.CreateRequest{
		SandboxID: "overflow", VCPUs: 1, MemoryMB: 1, DiskGB: 1,
	}); !errors.Is(err, plane.ErrNoCapacity) {
		t.Fatalf("overflow error=%v", err)
	}
}
