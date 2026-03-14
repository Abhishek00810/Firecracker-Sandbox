package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/cgroup"
	"backend/internal/executor"
	"backend/internal/executor/firecracker"

	"github.com/google/uuid"
)

type Manager struct {
	store            *Store
	vmManager        *firecracker.FireCrackerManager
	template         *firecracker.SnapshotTemplate
	freeCgroupCfg    cgroup.Config
	premiumCgroupCfg cgroup.Config
	idleTimeout      time.Duration
	vmConfig         firecracker.VMConfig
}

func NewManager(
	vmManager *firecracker.FireCrackerManager,
	template *firecracker.SnapshotTemplate,
	vmConfig firecracker.VMConfig,
	freeCgroupCfg cgroup.Config,
	premiumCgroupCfg cgroup.Config,
	maxSessions int,
	idleTimeout time.Duration,
) *Manager {
	m := &Manager{
		store:            NewStore(maxSessions),
		vmManager:        vmManager,
		template:         template,
		vmConfig:         vmConfig,
		freeCgroupCfg:    freeCgroupCfg,
		premiumCgroupCfg: premiumCgroupCfg,
		idleTimeout:      idleTimeout,
	}
	go m.reaper()
	return m
}

// Create boots a VM and binds it to a new session
func (m *Manager) Create(ctx context.Context, tier string) (*Session, error) {
	var vm *firecracker.MicroVM
	var err error

	if m.template != nil {
		vm, err = m.vmManager.LoadFromSnapshot(ctx, m.vmConfig, m.template)
		if err != nil {
			slog.Warn("session: snapshot restore failed, falling back to cold boot", "err", err)
			vm, err = coldBoot(ctx, m.vmManager, m.vmConfig)
		}
	} else {
		vm, err = coldBoot(ctx, m.vmManager, m.vmConfig)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create session VM: %w", err)
	}

	// Wait until the guest agent is actually accepting connections.
	// Same issue as the pool: after snapshot restore the vsock file exists
	// immediately but the virtio-vsock transport needs time to reconnect.
	vsockClient := firecracker.NewVsockClient(vm.VsockPath)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if vsockClient.Ping() {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Warm up Python kernel so the first sess.run() is fast.
	// Execute (not Ping) leaves the guest cleanly back at Accept after completion.
	if _, err := vsockClient.Execute("pass", "python", 30); err != nil {
		slog.Warn("session: python warmup failed", "vm_id", vm.ID, "err", err)
	}

	// pick cgroup config and tenant path based on tier
	tenantID := "session-free"
	cgroupCfg := m.freeCgroupCfg
	if tier == "premium" {
		tenantID = "session-premium"
		cgroupCfg = m.premiumCgroupCfg
	}

	var cg *cgroup.Cgroup
	if vm.Process != nil && vm.Process.Process != nil {
		cg, err = cgroup.New(tenantID, vm.ID, cgroupCfg)
		if err != nil {
			slog.Warn("session: cgroup creation failed", "vm_id", vm.ID, "err", err)
		} else if err = cg.AddPID(vm.Process.Process.Pid); err != nil {
			slog.Warn("session: cgroup add pid failed", "vm_id", vm.ID, "err", err)
			cg.Destroy()
			cg = nil
		}
	}

	sess := &Session{
		ID:        uuid.New().String(),
		VM:        vm,
		Cgroup:    cg,
		Tier:      tier,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}

	if err := m.store.Add(sess); err != nil {
		m.vmManager.Destroy(ctx, vm.ID)
		return nil, err
	}

	slog.Info("session created", "session_id", sess.ID, "vm_id", vm.ID, "tier", tier)
	return sess, nil
}

// Execute runs code inside an existing session's VM
func (m *Manager) Execute(ctx context.Context, sessionID, code, language string) (executor.ExecutionResult, error) {
	sess, ok := m.store.Get(sessionID)
	if !ok {
		return executor.ExecutionResult{}, fmt.Errorf("session %s not found", sessionID)
	}

	// serialize concurrent calls on the same session
	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.LastUsed = time.Now()

	vsockClient := firecracker.NewVsockClient(sess.VM.VsockPath)
	resp, err := vsockClient.Execute(code, language, 30)
	if err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("execution failed: %w", err)
	}

	output := resp.Stdout
	if resp.Stderr != "" {
		output += "\n" + resp.Stderr
	}

	return executor.ExecutionResult{
		Output:            output,
		Duration:          resp.Duration,
		ExitCode:          int64(resp.ExitCode),
		TerminationReason: "success",
	}, nil
}

// Destroy tears down a session and its VM
func (m *Manager) Destroy(ctx context.Context, sessionID string) error {
	sess, ok := m.store.Delete(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if sess.Cgroup != nil {
		sess.Cgroup.Destroy()
	}

	if err := m.vmManager.Destroy(ctx, sess.VM.ID); err != nil {
		return fmt.Errorf("failed to destroy VM: %w", err)
	}

	slog.Info("session destroyed", "session_id", sessionID, "vm_id", sess.VM.ID)
	return nil
}

func (m *Manager) GetSession(id string) (*Session, bool) {
	return m.store.Get(id)
}

func (m *Manager) Stats() map[string]int {
	return map[string]int{"active_sessions": m.store.Count()}
}

// reaper runs every minute and evicts idle sessions
func (m *Manager) reaper() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		evicted := m.store.EvictIdle(m.idleTimeout)
		for _, sess := range evicted {
			slog.Info("session idle timeout, destroying", "session_id", sess.ID, "idle_for", time.Since(sess.LastUsed))
			if sess.Cgroup != nil {
				sess.Cgroup.Destroy()
			}
			m.vmManager.Destroy(context.Background(), sess.VM.ID)
		}
	}
}

func coldBoot(ctx context.Context, mgr *firecracker.FireCrackerManager, cfg firecracker.VMConfig) (*firecracker.MicroVM, error) {
	vm, err := mgr.Create(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := mgr.Boot(ctx, vm.ID); err != nil {
		return nil, err
	}
	return vm, nil
}

func (m *Manager) Shutdown(ctx context.Context) {
	sessions := m.store.All()
	for _, sess := range sessions {
		if sess.Cgroup != nil {
			sess.Cgroup.Destroy()
		}
		m.vmManager.Destroy(ctx, sess.VM.ID)
	}
}
