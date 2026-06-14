package session

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"backend/internal/cgroup"
	"backend/internal/executor"
	"backend/internal/executor/firecracker"
	"backend/internal/tierconfig"

	"github.com/google/uuid"
)

type Manager struct {
	store            *Store
	vmManager        *firecracker.FireCrackerManager
	template         *firecracker.SnapshotTemplate
	freeCgroupCfg    cgroup.Config
	premiumCgroupCfg cgroup.Config
	idleTimeout      time.Duration
	maxLifetime      time.Duration
	vmConfig         firecracker.VMConfig
	freePool         *firecracker.VMPool
	proPool          *firecracker.VMPool
}

func NewManager(
	vmManager *firecracker.FireCrackerManager,
	template *firecracker.SnapshotTemplate,
	vmConfig firecracker.VMConfig,
	freeCgroupCfg cgroup.Config,
	premiumCgroupCfg cgroup.Config,
	maxSessions int,
	idleTimeout time.Duration,
	maxLifetime time.Duration,
	freePool *firecracker.VMPool,
	proPool *firecracker.VMPool,
) *Manager {
	m := &Manager{
		store:            NewStore(maxSessions),
		vmManager:        vmManager,
		template:         template,
		vmConfig:         vmConfig,
		freeCgroupCfg:    freeCgroupCfg,
		premiumCgroupCfg: premiumCgroupCfg,
		idleTimeout:      idleTimeout,
		maxLifetime:      maxLifetime,
		freePool:         freePool,
		proPool:          proPool,
	}
	go m.reaper()
	return m
}

// Create boots a VM and binds it to a new session. env vars are injected into
// the persistent shell so commands like git can access GITHUB_TOKEN etc.
func (m *Manager) Create(ctx context.Context, tier string, env map[string]string, vcpus, memoryMB, diskGB int) (*Session, error) {
	t0 := time.Now()

	pool := m.freePool
	if tier == tierconfig.Pro {
		pool = m.proPool
	}
	if pool == nil {
		return nil, fmt.Errorf("no VM pool configured for tier %s", tier)
	}

	pvm, warm, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire VM: %w", err)
	}

	sess := &Session{
		ID:        uuid.New().String(),
		VM:        pvm.VM,
		Cgroup:    pvm.Cgroup,
		PooledVM:  pvm,
		Pool:      pool,
		Tier:      tier,
		VCPUs:     vcpus,
		MemoryMB:  memoryMB,
		DiskGB:    diskGB,
		CreatedAt: time.Now(),
		LastUsed:  time.Now(),
	}
	if err := m.store.Add(sess); err != nil {
		pool.Release(pvm)
		return nil, err
	}

	// Open a persistent vsock connection for this session — reused for all exec/run calls
	vsockClient := firecracker.NewVsockClient(pvm.VM.VsockPath)
	conn, err := vsockClient.Connect()
	if err != nil {
		pool.Release(pvm)
		m.store.Delete(sess.ID)
		return nil, fmt.Errorf("vsock connect failed: %w", err)
	}
	sess.VsockConn = conn

	// Inject env vars into the VM's persistent shell if provided
	if len(env) > 0 {
		if err := vsockClient.SetEnvOnConn(conn, env); err != nil {
			slog.Warn("set_env failed", "session_id", sess.ID, "err", err)
		}
	}

	slog.Info("session created", "session_id", sess.ID, "vm_id", pvm.VM.ID, "tier", tier, "warm", warm, "ms", time.Since(t0).Milliseconds())
	return sess, nil
}

// Exec runs a shell command in the session's persistent bash process
func (m *Manager) Exec(ctx context.Context, sessionID, command string, timeoutSec int) (executor.ExecutionResult, error) {
	sess, ok := m.store.Get(sessionID)
	if !ok {
		return executor.ExecutionResult{}, fmt.Errorf("session %s not found", sessionID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.LastUsed = time.Now()

	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	vsockClient := firecracker.NewVsockClient(sess.VM.VsockPath)
	resp, err := vsockClient.ExecOnConn(sess.VsockConn, command, timeoutSec)
	if err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("exec failed: %w", err)
	}

	sess.RunCount++
	exitCode := resp.ExitCode
	sess.LastExitCode = &exitCode

	return executor.ExecutionResult{
		Stdout:            resp.Stdout,
		Stderr:            resp.Stderr,
		Duration:          resp.Duration,
		GuestDuration:     resp.Duration,
		ExitCode:          int64(resp.ExitCode),
		TerminationReason: "success",
	}, nil
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
	resp, err := vsockClient.ExecuteOnConn(sess.VsockConn, code, language, "stateful", 30)
	if err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("execution failed: %w", err)
	}

	sess.RunCount++
	sess.TotalExecutionMs += resp.Duration * 1000
	exitCode := resp.ExitCode
	sess.LastExitCode = &exitCode

	return executor.ExecutionResult{
		Stdout:            resp.Stdout,
		Stderr:            resp.Stderr,
		Duration:          resp.Duration,
		GuestDuration:     resp.Duration,
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

	if sess.VsockConn != nil {
		sess.VsockConn.Close()
	}

	if sess.PooledVM != nil && sess.Pool != nil {
		// pool handles cgroup destruction and replenishment
		sess.Pool.Release(sess.PooledVM)
	} else {
		if sess.Cgroup != nil {
			sess.Cgroup.Destroy()
		}
		if err := m.vmManager.Destroy(ctx, sess.VM.ID); err != nil {
			return fmt.Errorf("failed to destroy VM: %w", err)
		}
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
		evicted := m.store.EvictIdle(m.idleTimeout, m.maxLifetime)
		for _, sess := range evicted {
			slog.Info("session idle timeout, destroying", "session_id", sess.ID, "idle_for", time.Since(sess.LastUsed))
			if sess.VsockConn != nil {
				sess.VsockConn.Close()
			}
			if sess.PooledVM != nil && sess.Pool != nil {
				sess.Pool.Release(sess.PooledVM)
			} else {
				if sess.Cgroup != nil {
					sess.Cgroup.Destroy()
				}
				m.vmManager.Destroy(context.Background(), sess.VM.ID)
			}
		}
	}
}

func (m *Manager) Shutdown(ctx context.Context) {
	sessions := m.store.All()
	for _, sess := range sessions {
		if sess.PooledVM != nil {
			continue // pool.Shutdown handles these
		}
		if sess.Cgroup != nil {
			sess.Cgroup.Destroy()
		}
		m.vmManager.Destroy(ctx, sess.VM.ID)
	}
	if m.freePool != nil {
		m.freePool.Shutdown()
	}
	if m.proPool != nil {
		m.proPool.Shutdown()
	}
}
