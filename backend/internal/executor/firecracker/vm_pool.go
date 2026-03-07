package firecracker

import (
	"backend/internal/cgroup"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type VMPool struct {
	mu           sync.Mutex
	vms          chan *PooledVM       // queue of available VMs
	vmMap        map[string]*PooledVM // ALL VMs
	config       VMConfig
	manager      VMManager
	size         int
	cgroupConfig cgroup.Config
	template     *SnapshotTemplate // nil = cold boot
}

type PooledVM struct {
	VM           *MicroVM
	RequestCount int
	LastUsed     time.Time
	InUse        bool
	Cgroup       *cgroup.Cgroup
}

func NewVMPool(n int, cfg VMConfig, mgr VMManager, cgroupConfig cgroup.Config) *VMPool {
	return NewVMPoolWithSnapshot(n, cfg, mgr, cgroupConfig, nil)
}

func NewVMPoolWithSnapshot(n int, cfg VMConfig, mgr VMManager, cgroupConfig cgroup.Config, tmpl *SnapshotTemplate) *VMPool {
	pool := &VMPool{
		vms:          make(chan *PooledVM, n),
		vmMap:        make(map[string]*PooledVM),
		size:         n,
		config:       cfg,
		manager:      mgr,
		cgroupConfig: cgroupConfig,
		template:     tmpl,
	}

	for i := 0; i < n; i++ {
		if err := pool.addVM(); err != nil {
			slog.Error("failed to add VM to pool", "index", i, "err", err)
		}
	}

	return pool
}

func (p *VMPool) addVM() error {
	ctx := context.Background()
	var vm *MicroVM
	var err error

	if p.template != nil {
		vm, err = p.manager.LoadFromSnapshot(ctx, p.config, p.template)
		if err != nil {
			slog.Warn("snapshot restore failed, falling back to cold boot", "err", err)
			vm, err = p.manager.Create(ctx, p.config)
			if err != nil {
				return err
			}
			if err = p.manager.Boot(ctx, vm.ID); err != nil {
				return err
			}
		}
	} else {
		vm, err = p.manager.Create(ctx, p.config)
		if err != nil {
			slog.Debug("VM creation failed", "err", err)
			return err
		}
		if err = p.manager.Boot(ctx, vm.ID); err != nil {
			slog.Debug("boot failed", "err", err)
			return err
		}
	}

	// Poll until the vsock handshake actually succeeds.
	// Checking file existence is not enough — for snapshot-restored VMs the
	// file appears immediately after rename but the guest agent's vsock socket
	// may not be ready yet (virtio-vsock transport resets on snapshot restore,
	// and the guest agent needs to reinitialize its listener).
	vsockReady := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if NewVsockClient(vm.VsockPath).Ping() {
			vsockReady = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !vsockReady {
		return fmt.Errorf("vsock never became ready for VM %s", vm.ID)
	}

	var cg *cgroup.Cgroup
	if vm.Process != nil && vm.Process.Process != nil {
		cg, err = cgroup.New("default", vm.ID, p.cgroupConfig)
		if err != nil {
			slog.Warn("failed to create cgroup", "vm_id", vm.ID, "err", err)
		} else if err = cg.AddPID(vm.Process.Process.Pid); err != nil {
			slog.Warn("failed to add pid to cgroup", "vm_id", vm.ID, "err", err)
			cg.Destroy()
			cg = nil
		}
	}

	pooledVM := &PooledVM{
		VM:           vm,
		RequestCount: 0,
		LastUsed:     time.Now(),
		InUse:        false,
		Cgroup:       cg,
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
	// Destroy the used VM — its rootfs may have been modified during execution.
	// Never return a dirty VM to the pool.
	go func() {
		ctx := context.Background()

		p.mu.Lock()
		delete(p.vmMap, vm.VM.ID)
		p.mu.Unlock()

		if err := p.manager.Destroy(ctx, vm.VM.ID); err != nil {
			slog.Error("failed to destroy VM", "vm_id", vm.VM.ID, "err", err)
		}
		if vm.Cgroup != nil {
			if err := vm.Cgroup.Destroy(); err != nil {
				slog.Error("failed to destroy cgroup", "vm_id", vm.VM.ID, "err", err)
			}
		}

		// Replenish pool — uses snapshot restore if template is set, cold boot otherwise
		if err := p.addVM(); err != nil {
			slog.Error("failed to replenish pool after releasing VM", "vm_id", vm.VM.ID, "err", err)
		}
	}()
}

func (p *VMPool) Shutdown() {
	ctx := context.Background()

	p.mu.Lock()
	vms := make([]*PooledVM, 0, len(p.vmMap))
	for _, vm := range p.vmMap {
		vms = append(vms, vm)
	}
	p.mu.Unlock()

	for _, vm := range vms {
		if err := p.manager.Destroy(ctx, vm.VM.ID); err != nil {
			slog.Error("shutdown: failed to destroy VM", "vm_id", vm.VM.ID, "err", err)
		}
		if vm.Cgroup != nil {
			if err := vm.Cgroup.Destroy(); err != nil {
				slog.Error("shutdown: failed to destroy cgroup", "vm_id", vm.VM.ID, "err", err)
			}
		}
	}
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
