package worker

import (
	"fmt"
	"math"
	"sync"

	"backend/internal/plane"
)

type reservation struct {
	vcpus    int
	memoryMB int
	diskGB   int
	paused   bool
}

// Admission owns the worker's authoritative, atomic capacity accounting.
// Postgres receives snapshots of this state through worker heartbeats.
type Admission struct {
	mu sync.Mutex

	allocatableVCPUs    int
	allocatableMemoryMB int
	allocatableDiskGB   int
	maxSandboxes        int
	reservations        map[string]reservation
}

func NewAdmission(vcpus, memoryMB, diskGB, maxSandboxes int, cpuRatio, memoryRatio float64) *Admission {
	if cpuRatio < 1 {
		cpuRatio = 1
	}
	if memoryRatio < 1 {
		memoryRatio = 1
	}
	return &Admission{
		allocatableVCPUs:    int(math.Floor(float64(vcpus) * cpuRatio)),
		allocatableMemoryMB: int(math.Floor(float64(memoryMB) * memoryRatio)),
		allocatableDiskGB:   diskGB,
		maxSandboxes:        maxSandboxes,
		reservations:        make(map[string]reservation),
	}
}

func (a *Admission) ReserveCreate(req plane.CreateRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if existing, ok := a.reservations[req.SandboxID]; ok {
		if existing.vcpus == req.VCPUs && existing.memoryMB == req.MemoryMB && existing.diskGB == req.DiskGB {
			return nil
		}
		return fmt.Errorf("sandbox %s already has a different reservation", req.SandboxID)
	}
	if req.VCPUs <= 0 || req.MemoryMB <= 0 || req.DiskGB <= 0 {
		return fmt.Errorf("sandbox resources must be positive")
	}
	capacity := a.capacityLocked()
	if capacity.FreeSlots < 1 ||
		capacity.AllocatableVCPUs-capacity.ReservedVCPUs < req.VCPUs ||
		capacity.AllocatableMemoryMB-capacity.ReservedMemoryMB < req.MemoryMB ||
		capacity.AllocatableDiskGB-capacity.ReservedDiskGB < req.DiskGB {
		return plane.ErrNoCapacity
	}
	a.reservations[req.SandboxID] = reservation{
		vcpus: req.VCPUs, memoryMB: req.MemoryMB, diskGB: req.DiskGB,
	}
	return nil
}

func (a *Admission) Restore(sandboxID string, vcpus, memoryMB, diskGB int, paused bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reservations[sandboxID] = reservation{
		vcpus: vcpus, memoryMB: memoryMB, diskGB: diskGB, paused: paused,
	}
}

func (a *Admission) ReserveResume(sandboxID string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	item, ok := a.reservations[sandboxID]
	if !ok {
		return fmt.Errorf("sandbox %s has no local reservation", sandboxID)
	}
	if !item.paused {
		return nil
	}
	capacity := a.capacityLocked()
	if capacity.AllocatableVCPUs-capacity.ReservedVCPUs < item.vcpus ||
		capacity.AllocatableMemoryMB-capacity.ReservedMemoryMB < item.memoryMB {
		return plane.ErrNoCapacity
	}
	item.paused = false
	a.reservations[sandboxID] = item
	return nil
}

func (a *Admission) MarkPaused(sandboxID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if item, ok := a.reservations[sandboxID]; ok {
		item.paused = true
		a.reservations[sandboxID] = item
	}
}

func (a *Admission) MarkActive(sandboxID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if item, ok := a.reservations[sandboxID]; ok {
		item.paused = false
		a.reservations[sandboxID] = item
	}
}

func (a *Admission) Release(sandboxID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.reservations, sandboxID)
}

func (a *Admission) Capacity() plane.Capacity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.capacityLocked()
}

func (a *Admission) capacityLocked() plane.Capacity {
	capacity := plane.Capacity{
		MaxSlots:            a.maxSandboxes,
		AllocatableVCPUs:    a.allocatableVCPUs,
		AllocatableMemoryMB: a.allocatableMemoryMB,
		AllocatableDiskGB:   a.allocatableDiskGB,
		ReservedSandboxes:   len(a.reservations),
	}
	for _, item := range a.reservations {
		capacity.ReservedDiskGB += item.diskGB
		if !item.paused {
			capacity.ReservedVCPUs += item.vcpus
			capacity.ReservedMemoryMB += item.memoryMB
		}
	}
	capacity.FreeSlots = capacity.MaxSlots - capacity.ReservedSandboxes
	if capacity.FreeSlots < 0 {
		capacity.FreeSlots = 0
	}
	return capacity
}
