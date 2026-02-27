package firecracker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

type VMPool struct {
	mu      sync.Mutex
	vms     chan *PooledVM       // queue of the available VMs
	vmMap   map[string]*PooledVM // ALL VMs
	config  VMConfig
	manager VMManager
	size    int
}

type PooledVM struct {
	VM           *MicroVM
	RequestCount int
	LastUsed     time.Time
	InUse        bool
}

func NewVMPool(n int, cfg VMConfig, mgr VMManager) *VMPool {
	pool := &VMPool{
		vms:     make(chan *PooledVM, n),
		vmMap:   make(map[string]*PooledVM),
		size:    n,
		config:  cfg,
		manager: mgr,
	}

	for i := 0; i < n; i++ {
		if err := pool.addVM(); err != nil {
			slog.Error("Failed to add VM to pool", "index", i, "err", err)
			// Continue with remaining VMs (don't fail entire pool)
		}
	}

	return pool

}

func (p *VMPool) addVM() error {
	// create vm
	// boot vm
	ctx := context.Background()

	vm, err := p.manager.Create(ctx, p.config)

	if err != nil {
		slog.Debug("VM creation failed", "err", err)
		return err
	}

	err = p.manager.Boot(ctx, vm.ID)
	if err != nil {
		slog.Debug("Boot failed", "err", err)
		return err
	}

	// Poll for vsock socket readiness instead of a fixed sleep (up to 15s)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(vm.VsockPath); statErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if _, statErr := os.Stat(vm.VsockPath); statErr != nil {
		return fmt.Errorf("vsock socket never appeared for VM %s", vm.ID)
	}

	pooledVM := &PooledVM{
		VM:           vm,
		RequestCount: 0,
		LastUsed:     time.Now(),
		InUse:        false,
	}

	p.mu.Lock()
	p.vmMap[vm.ID] = pooledVM
	p.mu.Unlock()
	p.vms <- pooledVM

	return nil
}

func (p *VMPool) Acquire(timeout time.Duration) (*PooledVM, error) {
	select {
	case vm := <-p.vms:
		p.mu.Lock()
		vm.InUse = true
		vm.LastUsed = time.Now()
		p.mu.Unlock()
		return vm, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("timeout: no VM available")
	}
}

func (p *VMPool) Release(vm *PooledVM) {
	// Destroy the used VM — its rootfs copy may have been modified
	// by the execution. Never return a dirty VM to the pool.
	go func() {
		ctx := context.Background()

		p.mu.Lock()
		delete(p.vmMap, vm.VM.ID)
		p.mu.Unlock()

		if err := p.manager.Destroy(ctx, vm.VM.ID); err != nil {
			slog.Error("Failed to destroy VM", "vm_id", vm.VM.ID, "err", err)
		}

		// Boot a fresh replacement VM to keep the pool at capacity
		if err := p.addVM(); err != nil {
			slog.Error("Failed to replenish pool after releasing VM", "vm_id", vm.VM.ID, "err", err)
		}
	}()
}

func (p *VMPool) Stats() (available, inUse int) {
	available = len(p.vms)
	p.mu.Lock()
	inUse = len(p.vmMap) - available
	p.mu.Unlock()
	if inUse < 0 {
		inUse = 0
	}
	return
}
