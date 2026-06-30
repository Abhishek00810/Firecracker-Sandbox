package firecracker

import (
	"backend/internal/cgroup"
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

type VMPool struct {
	mu                 sync.Mutex
	vms                chan *PooledVM       // queue of available VMs
	vmMap              map[string]*PooledVM // ALL VMs
	config             VMConfig
	manager            VMManager
	minSize            int
	maxSize            int
	pendingAdds        atomic.Int32
	cgroupConfig       cgroup.Config
	template           *SnapshotTemplate // nil = cold boot
	warmPythonStateful bool
	warmNodeBridge     bool
	stop               chan struct{} // closed by Shutdown to stop the reaper
}

// Idle reaper tuning. Pools run minSize=0, so any VM sitting available in the channel
// is fair game once it's been idle past idleTTL — this reclaims VMs that were provisioned
// for a request that timed out before claiming them (otherwise they leak procs+slots).
const (
	reapInterval = 5 * time.Second
	idleTTL      = 15 * time.Second
)

type PooledVM struct {
	VM           *MicroVM
	RequestCount int
	LastUsed     time.Time
	InUse        bool
	Cgroup       *cgroup.Cgroup
}

func NewVMPool(minSize, maxSize int, cfg VMConfig, mgr VMManager, cgroupConfig cgroup.Config) *VMPool {
	return NewVMPoolWithSnapshot(minSize, maxSize, cfg, mgr, cgroupConfig, nil, false, false)
}

func NewVMPoolWithSnapshot(minSize, maxSize int, cfg VMConfig, mgr VMManager, cgroupConfig cgroup.Config, tmpl *SnapshotTemplate,
	warmPythonStateful bool, warmNodeBridge bool) *VMPool {
	pool := &VMPool{
		vms:                make(chan *PooledVM, maxSize),
		vmMap:              make(map[string]*PooledVM),
		minSize:            minSize,
		maxSize:            maxSize,
		config:             cfg,
		manager:            mgr,
		cgroupConfig:       cgroupConfig,
		template:           tmpl,
		warmPythonStateful: warmPythonStateful,
		warmNodeBridge:     warmNodeBridge,
		stop:               make(chan struct{}),
	}

	var wg sync.WaitGroup
	for i := 0; i < minSize; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if err := pool.addVM(); err != nil {
				slog.Error("failed to add VM to pool", "index", idx, "err", err)
			}
		}(i)
	}
	wg.Wait()

	go pool.reaper()

	return pool
}

func (p *VMPool) addVM() error {
	start := time.Now()
	lastPhase := start
	ctx := context.Background()
	var vm *MicroVM
	var err error
	restoreMode := "cold_boot"
	if p.template != nil {
		restoreMode = "snapshot_restore"
		vm, err = p.manager.LoadFromSnapshot(ctx, p.config, p.template)
		if err != nil {
			slog.Warn("snapshot restore failed, falling back to cold boot", "err", err)
			restoreMode = "snapshot_fallback_cold_boot"
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
	restoreAcquireMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

	// Poll until the vsock handshake actually succeeds.
	// Checking file existence is not enough — for snapshot-restored VMs the
	// file appears immediately after rename but the guest agent's vsock socket
	// may not be ready yet (virtio-vsock transport resets on snapshot restore,
	// and the guest agent needs to reinitialize its listener).
	vsockReady := false
	vsockAttempts := 0
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		vsockAttempts++
		ok, stage, pingElapsed := NewVsockClient(vm.VsockPath).Ping()
		if ok {
			vsockReady = true
			break
		}
		// The first handful of misses are expected (virtio-vsock device is still
		// coming back after resume). Only warn if it's taking abnormally long.
		if time.Since(lastPhase) > 200*time.Millisecond {
			slog.Warn("vsock ping still failing",
				"vm_id", vm.ID,
				"attempt", vsockAttempts,
				"failed_at", stage,
				"ping_ms", pingElapsed.Milliseconds(),
				"elapsed_ms", time.Since(lastPhase).Milliseconds(),
			)
		}
		time.Sleep(5 * time.Millisecond) // tight poll: catch vsock readiness ~asap after resume (floor is the virtio-vsock device coming back)
	}
	if !vsockReady {
		return fmt.Errorf("vsock never became ready for VM %s", vm.ID)
	}
	postRestoreVsockMs := time.Since(lastPhase).Milliseconds()
	if vsockAttempts > 1 {
		slog.Warn("vsock took multiple attempts to become ready", "vm_id", vm.ID, "attempts", vsockAttempts, "ms", postRestoreVsockMs)
	}
	lastPhase = time.Now()

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
	cgroupSetupMs := time.Since(lastPhase).Milliseconds()
	lastPhase = time.Now()

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

	slog.Info("pool vm added",
		"vm_id", vm.ID,
		"mode", restoreMode,
		"restore_acquire_ms", restoreAcquireMs,
		"post_restore_vsock_ms", postRestoreVsockMs,
		"cgroup_setup_ms", cgroupSetupMs,
		"pool_insert_ms", time.Since(lastPhase).Milliseconds(),
		"duration_ms", time.Since(start).Milliseconds(),
		"pool_available", len(p.vms),
	)

	return nil
}

// tryScaleUp spawns one addVM goroutine if the pool has headroom under maxSize.
func (p *VMPool) tryScaleUp() {
	p.mu.Lock()
	total := len(p.vmMap) + int(p.pendingAdds.Load())
	if total >= p.maxSize {
		p.mu.Unlock()
		slog.Warn("pool at capacity, cannot scale up", "total", total, "max", p.maxSize)
		return
	}
	p.pendingAdds.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.pendingAdds.Add(-1)
		if err := p.addVM(); err != nil {
			slog.Error("on-demand VM restore failed", "err", err)
		}
	}()
}

func (p *VMPool) Acquire(ctx context.Context) (*PooledVM, bool, error) {
	start := time.Now()

	// Non-blocking check first — if a warm VM is ready, return instantly.
	select {
	case vm := <-p.vms:
		p.mu.Lock()
		vm.InUse = true
		vm.LastUsed = time.Now()
		p.mu.Unlock()
		slog.Info("pool acquire succeeded (warm)",
			"vm_id", vm.VM.ID,
			"wait_ms", time.Since(start).Milliseconds(),
			"pool_available", len(p.vms),
		)
		return vm, true, nil
	default:
	}

	// Pool empty — kick off a restore and wait exactly as long as it takes.
	p.tryScaleUp()
	select {
	case vm := <-p.vms:
		p.mu.Lock()
		vm.InUse = true
		vm.LastUsed = time.Now()
		p.mu.Unlock()
		slog.Info("pool acquire succeeded (restored)",
			"vm_id", vm.VM.ID,
			"wait_ms", time.Since(start).Milliseconds(),
			"pool_available", len(p.vms),
		)
		return vm, false, nil
	case <-ctx.Done():
		slog.Warn("pool acquire cancelled",
			"wait_ms", time.Since(start).Milliseconds(),
			"pool_available", len(p.vms),
		)
		return nil, false, fmt.Errorf("context cancelled waiting for VM: %w", ctx.Err())
	}
}

func (p *VMPool) Release(vm *PooledVM) {
	// Destroy the used VM — its rootfs may have been modified during execution.
	// Never return a dirty VM to the pool.
	go func() {
		start := time.Now()
		ctx := context.Background()

		p.mu.Lock()
		delete(p.vmMap, vm.VM.ID)
		remaining := len(p.vmMap)
		p.mu.Unlock()

		if err := p.manager.Destroy(ctx, vm.VM.ID); err != nil {
			slog.Error("failed to destroy VM", "vm_id", vm.VM.ID, "err", err)
		}
		if vm.Cgroup != nil {
			if err := vm.Cgroup.Destroy(); err != nil {
				slog.Error("failed to destroy cgroup", "vm_id", vm.VM.ID, "err", err)
			}
		}

		// Only replenish if the pool has dropped below the warm minimum.
		// Burst VMs (above minSize) are intentionally not replaced — the pool
		// shrinks back to steady state naturally after traffic subsides.
		if remaining < p.minSize {
			if err := p.addVM(); err != nil {
				slog.Error("failed to replenish pool after releasing VM", "vm_id", vm.VM.ID, "err", err)
				return
			}
			slog.Info("pool release replenished",
				"vm_id", vm.VM.ID,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		} else {
			slog.Info("pool release complete, no replenish needed",
				"vm_id", vm.VM.ID,
				"remaining", remaining,
				"min_size", p.minSize,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		}
	}()
}

// Forget removes a VM from the pool's accounting WITHOUT destroying it. Used by
// auto-pause: the session takes ownership of teardown (snapshot + keep-disk), so the
// pool must stop tracking the VM and free the capacity slot it occupied under maxSize.
func (p *VMPool) Forget(vmID string) {
	p.mu.Lock()
	delete(p.vmMap, vmID)
	p.mu.Unlock()
}

// reaper periodically reclaims VMs left sitting idle in the pool — primarily ones
// that were provisioned for a request whose Acquire timed out before claiming them.
// Without this they hold a Firecracker proc + network slot + cgroup indefinitely.
func (p *VMPool) reaper() {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.reapIdle()
		}
	}
}

func (p *VMPool) reapIdle() {
	// Inspect each currently-available VM once. Destroy those idle past idleTTL while
	// the pool is above minSize; requeue the rest.
	for range len(p.vms) {
		select {
		case vm := <-p.vms:
			p.mu.Lock()
			total := len(p.vmMap)
			p.mu.Unlock()
			if total > p.minSize && time.Since(vm.LastUsed) > idleTTL {
				slog.Info("reaper: destroying idle VM", "vm_id", vm.VM.ID, "idle_s", time.Since(vm.LastUsed).Seconds())
				p.destroyPooled(vm)
				continue
			}
			select {
			case p.vms <- vm:
			default:
				p.destroyPooled(vm) // channel full (shouldn't happen) — don't block
			}
		default:
			return
		}
	}
}

// destroyPooled tears a VM fully down: remove from the map, destroy the VM (releases
// its network slot), and destroy its cgroup. Used by the reaper and Shutdown.
func (p *VMPool) destroyPooled(vm *PooledVM) {
	ctx := context.Background()
	p.mu.Lock()
	delete(p.vmMap, vm.VM.ID)
	p.mu.Unlock()
	if err := p.manager.Destroy(ctx, vm.VM.ID); err != nil {
		slog.Error("reaper: failed to destroy idle VM", "vm_id", vm.VM.ID, "err", err)
	}
	if vm.Cgroup != nil {
		if err := vm.Cgroup.Destroy(); err != nil {
			slog.Error("reaper: failed to destroy cgroup", "vm_id", vm.VM.ID, "err", err)
		}
	}
}

func (p *VMPool) Shutdown() {
	close(p.stop)
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
