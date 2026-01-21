package firecracker

import (
	"context"
	"log"
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
			log.Printf("Failed to add VM %d to pool: %v", i, err)
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
		log.Printf("DEBUG: VM creation failed: %v", err)
		return err
	}

	err = p.manager.Boot(ctx, vm.ID)
	if err != nil {
		log.Printf("DEBUG: Boot failed: %v", err)
		return err
	}
	time.Sleep(8 * time.Second)

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
