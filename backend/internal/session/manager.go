package session

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"backend/internal/cgroup"
	"backend/internal/executor"
	"backend/internal/executor/firecracker"
	"backend/internal/tierconfig"

	"github.com/google/uuid"
)

// pauseTTL is how long a paused session's snapshot is retained before it's hard-deleted.
const pauseTTL = 7 * 24 * time.Hour

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
	pauseDir         string // disk-backed dir for per-session pause snapshots (NOT /dev/shm)
	manifestPath     string // recovery manifest of paused sessions
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
		pauseDir:         filepath.Join(vmManager.SocketDir, "pause"),
		manifestPath:     filepath.Join(vmManager.SocketDir, "paused-sessions.json"),
	}
	if err := os.MkdirAll(m.pauseDir, 0o755); err != nil {
		slog.Error("failed to create pause snapshot dir", "dir", m.pauseDir, "err", err)
	}
	// Firecracker drops to FCUid and writes the snapshot files itself, so the dir must be
	// owned by that user (the backend runs as root and creates the dir).
	if vmManager.FCUid > 0 {
		os.Chown(m.pauseDir, vmManager.FCUid, vmManager.FCGid)
	}
	m.recoverPaused()
	go m.reaper()
	return m
}

// recoverPaused rebuilds the store's paused sessions from the on-disk manifest after a
// process restart. Sessions whose snapshot/disk files survived (reboot/deploy) come back
// as paused and resume on next access; those whose files are gone (host loss) are dropped.
func (m *Manager) recoverPaused() {
	sessions, err := readManifest(m.manifestPath)
	if err != nil {
		slog.Error("failed to read paused-session manifest", "path", m.manifestPath, "err", err)
		return
	}
	recovered := 0
	for _, sess := range sessions {
		if err := m.store.Add(sess); err != nil {
			slog.Warn("could not recover paused session", "session_id", sess.ID, "err", err)
			continue
		}
		recovered++
	}
	if recovered > 0 {
		slog.Info("recovered paused sessions from manifest", "count", recovered)
		m.persistManifest()
	}
}

// persistManifest rewrites the recovery manifest from the current set of paused sessions.
func (m *Manager) persistManifest() {
	var paused []*Session
	for _, s := range m.store.All() {
		if s.State == StatePaused {
			paused = append(paused, s)
		}
	}
	if err := writeManifest(m.manifestPath, paused); err != nil {
		slog.Error("failed to persist paused-session manifest", "err", err)
	}
}

// Create boots a VM and binds it to a new session. env vars are injected into
// the persistent shell so commands like git can access GITHUB_TOKEN etc.
func (m *Manager) Create(ctx context.Context, tier string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*Session, error) {
	t0 := time.Now()

	pool := m.freePool
	if tier == tierconfig.PAYG {
		pool = m.proPool
	}
	if pool == nil {
		return nil, fmt.Errorf("no VM pool configured for tier %s", tier)
	}

	pvm, warm, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to acquire VM: %w", err)
	}

	// Apply network policy on the VM's slot. When internet=false this blocks egress
	// in the slot's netns; it also normalizes any stale rule on a reused slot. If we
	// can't enforce a requested isolation, fail rather than hand back a leaky sandbox.
	if err := m.vmManager.SetSlotEgress(pvm.VM.Slot, internet); err != nil {
		pool.Release(pvm)
		return nil, fmt.Errorf("apply network policy: %w", err)
	}

	sess := &Session{
		ID:          uuid.New().String(),
		VM:          pvm.VM,
		Cgroup:      pvm.Cgroup,
		PooledVM:    pvm,
		Pool:        pool,
		Tier:        tier,
		VCPUs:       vcpus,
		MemoryMB:    memoryMB,
		DiskGB:      diskGB,
		Env:         env,
		Internet:    internet,
		IdleTimeout: idleTimeout,
		MaxLifetime: maxLifetime,
		CreatedAt:   time.Now(),
		LastUsed:    time.Now(),
		State:       StateActive,
	}
	if err := m.store.Add(sess); err != nil {
		pool.Release(pvm)
		return nil, err
	}

	// Hold ONE persistent vsock connection for the whole session, reused for every exec/run.
	// A snapshot-restored VM accepts exactly one vsock connection, so per-connection execs
	// don't work — the persistent connection is the correct (and only) model. Pause closes
	// it (agent returns to accept, snapshot captures that state); resume opens a fresh one.
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
	// Transparently resume a paused session before running.
	if err := m.ensureActive(ctx, sessionID); err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("resume session: %w", err)
	}

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
func (m *Manager) Execute(ctx context.Context, sessionID, code, language string, timeoutSec int) (executor.ExecutionResult, error) {
	// Transparently resume a paused session before running.
	if err := m.ensureActive(ctx, sessionID); err != nil {
		return executor.ExecutionResult{}, fmt.Errorf("resume session: %w", err)
	}

	sess, ok := m.store.Get(sessionID)
	if !ok {
		return executor.ExecutionResult{}, fmt.Errorf("session %s not found", sessionID)
	}

	// serialize concurrent calls on the same session
	sess.mu.Lock()
	defer sess.mu.Unlock()

	sess.LastUsed = time.Now()

	if timeoutSec <= 0 {
		timeoutSec = 30
	}

	vsockClient := firecracker.NewVsockClient(sess.VM.VsockPath)
	resp, err := vsockClient.ExecuteOnConn(sess.VsockConn, code, language, "stateful", timeoutSec)
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

// Destroy tears down a session. For an active session this releases its VM; for a paused
// session there is no live VM, so it just deletes the on-disk snapshot + writable disk.
func (m *Manager) Destroy(ctx context.Context, sessionID string) error {
	sess, ok := m.store.Delete(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if sess.State == StatePaused {
		m.cleanupPausedFiles(sess)
		m.persistManifest()
		slog.Info("paused session destroyed", "session_id", sessionID)
		return nil
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
		if sess.VM != nil {
			if err := m.vmManager.Destroy(ctx, sess.VM.ID); err != nil {
				return fmt.Errorf("failed to destroy VM: %w", err)
			}
		}
	}

	slog.Info("session destroyed", "session_id", sessionID)
	return nil
}

// Pause snapshots an active session's memory to disk and releases its VM, network slot,
// RAM and cgroup — keeping its writable disk. The session stays in the store as paused
// and resumes transparently on next use. Idempotent: a non-active session is a no-op.
func (m *Manager) Pause(ctx context.Context, sessionID string) error {
	sess, ok := m.store.Get(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if m.template == nil {
		return fmt.Errorf("cannot pause session %s: no snapshot template (cold-boot mode)", sessionID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.State != StateActive || sess.VM == nil {
		return nil
	}

	t0 := time.Now()
	snapDir := filepath.Join(m.pauseDir, sessionID)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		return fmt.Errorf("create pause dir: %w", err)
	}
	// The Firecracker process (fcvm) writes the snapshot files, so it must own the dir.
	if m.vmManager.FCUid > 0 {
		os.Chown(snapDir, m.vmManager.FCUid, m.vmManager.FCGid)
	}
	snapPath := filepath.Join(snapDir, "snap")
	memPath := filepath.Join(snapDir, "mem")

	// Capture resume info before tearing the VM down. The session snapshot bakes the
	// TEMPLATE's device paths (Firecracker carries the vsock UDS path + TAP name forward
	// across restore — not the per-VM runtime slot names), so resume must restore against
	// those. Persisted in the manifest so recovery after a restart still has them.
	vmID := sess.VM.ID
	sess.VsockPathAtPause = m.template.VsockPath
	sess.TapNameAtPause = m.template.TapName
	sess.WritableDiskPath = sess.VM.WritableDiskPath

	// Close the persistent connection and let the guest agent fall back into accept()
	// BEFORE we freeze the VM. On a live (not-yet-frozen) VM the close propagates normally,
	// so the agent's blocked read returns and it loops back to accept() — the state a
	// snapshot restores cleanly from. (We must snapshot with the agent in accept(): a VM
	// captured mid-read can't be talked to after resume.)
	if sess.VsockConn != nil {
		sess.VsockConn.Close()
		sess.VsockConn = nil
	}
	time.Sleep(400 * time.Millisecond)

	// Snapshot: pauses the vCPUs and writes VM + RAM state to disk (NOT /dev/shm).
	if err := m.vmManager.Snapshot(ctx, vmID, snapPath, memPath); err != nil {
		return fmt.Errorf("snapshot for pause: %w", err)
	}

	// Detach from the pool so its capacity slot is freed, tear the VM down keeping the
	// writable disk, then drop the (now empty) cgroup.
	if sess.Pool != nil {
		sess.Pool.Forget(vmID)
	}
	if err := m.vmManager.TeardownKeepDisk(ctx, vmID); err != nil {
		slog.Error("pause teardown failed", "session_id", sessionID, "err", err)
	}
	if sess.Cgroup != nil {
		sess.Cgroup.Destroy()
	}

	sess.SnapPath = snapPath
	sess.MemPath = memPath
	sess.State = StatePaused
	sess.PausedAt = time.Now()
	sess.VM = nil
	sess.VsockConn = nil
	sess.PooledVM = nil
	sess.Pool = nil
	sess.Cgroup = nil

	m.persistManifest()
	slog.Info("session paused", "session_id", sessionID, "ms", time.Since(t0).Milliseconds())
	return nil
}

// Resume restores a paused session from its own snapshot, reattaching its writable disk
// in place into a fresh slot. Idempotent: an already-active session is a no-op.
func (m *Manager) Resume(ctx context.Context, sessionID string) error {
	sess, ok := m.store.Get(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	sess.mu.Lock()
	defer sess.mu.Unlock()
	if sess.State == StateActive {
		return nil
	}

	t0 := time.Now()
	tmpl := &firecracker.SnapshotTemplate{
		SnapPath:         sess.SnapPath,
		MemPath:          sess.MemPath,
		RootfsPath:       m.template.RootfsPath, // shared read-only lower
		WritableDiskPath: sess.WritableDiskPath, // the session's own disk, attached in place
		VsockPath:        sess.VsockPathAtPause,
		TapName:          sess.TapNameAtPause,
	}

	cfg := m.vmConfig
	cfg.Pro = sess.Tier == tierconfig.PAYG

	vm, err := m.vmManager.ResumeFromSnapshot(ctx, cfg, tmpl)
	if err != nil {
		return fmt.Errorf("resume from snapshot: %w", err)
	}

	if err := m.vmManager.SetSlotEgress(vm.Slot, sess.Internet); err != nil {
		m.vmManager.Destroy(ctx, vm.ID)
		return fmt.Errorf("re-apply network policy on resume: %w", err)
	}

	// Recreate the cgroup for the new VM process (matches the pool's session cgroup).
	cgCfg := m.freeCgroupCfg
	if sess.Tier == tierconfig.PAYG {
		cgCfg = m.premiumCgroupCfg
	}
	var cg *cgroup.Cgroup
	if vm.Process != nil && vm.Process.Process != nil {
		cg, err = cgroup.New("default", vm.ID, cgCfg)
		if err != nil {
			slog.Warn("failed to create cgroup on resume", "vm_id", vm.ID, "err", err)
		} else if err = cg.AddPID(vm.Process.Process.Pid); err != nil {
			slog.Warn("failed to add pid to cgroup on resume", "vm_id", vm.ID, "err", err)
			cg.Destroy()
			cg = nil
		}
	}
	// Let the guest agent re-initialize its vsock listener after the restore (accept()
	// breaks on restore → the agent rebuilds its listener) before we open the connection.
	time.Sleep(500 * time.Millisecond)

	// Open a fresh persistent connection — this is the ONE vsock connection the restored VM
	// allows, reused for every subsequent exec/run (its CONNECT waits for the agent to accept).
	vsockClient := firecracker.NewVsockClient(vm.VsockPath)
	conn, err := vsockClient.Connect()
	if err != nil {
		if cg != nil {
			cg.Destroy()
		}
		m.vmManager.Destroy(ctx, vm.ID)
		return fmt.Errorf("vsock connect on resume: %w", err)
	}

	// Reset all stateful runtimes so in-memory interpreter state is consistently cleared on
	// pause across every language (a snapshot restore can also leave ZMQ Python kernels
	// degraded). Then re-inject the session's env so config still persists across the pause.
	if err := vsockClient.ResetRuntimesOnConn(conn); err != nil {
		slog.Warn("reset_runtimes on resume failed", "session_id", sessionID, "err", err)
	}
	if len(sess.Env) > 0 {
		if err := vsockClient.SetEnvOnConn(conn, sess.Env); err != nil {
			slog.Warn("re-inject env on resume failed", "session_id", sessionID, "err", err)
		}
	}

	sess.VM = vm
	sess.Cgroup = cg
	sess.VsockConn = conn
	sess.PooledVM = nil // resumed VM is standalone; Destroy releases its slot + disk directly
	sess.Pool = nil
	sess.State = StateActive
	sess.LastUsed = time.Now()

	// The mem/snap files are consumed by the restore; the disk persists. Clean them up.
	os.Remove(sess.SnapPath)
	os.Remove(sess.MemPath)
	sess.SnapPath = ""
	sess.MemPath = ""

	m.persistManifest()
	slog.Info("session resumed", "session_id", sessionID, "vm_id", vm.ID, "ms", time.Since(t0).Milliseconds())
	return nil
}

// ensureActive resumes the session if it is paused so callers transparently get a live VM.
func (m *Manager) ensureActive(ctx context.Context, sessionID string) error {
	sess, ok := m.store.Get(sessionID)
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}
	if sess.State == StatePaused {
		return m.Resume(ctx, sessionID)
	}
	return nil
}

// cleanupPausedFiles deletes a paused session's on-disk artifacts (snapshot + writable disk).
func (m *Manager) cleanupPausedFiles(sess *Session) {
	if sess.WritableDiskPath != "" {
		os.Remove(sess.WritableDiskPath)
	}
	os.RemoveAll(filepath.Join(m.pauseDir, sess.ID))
}

// PauseAllActive snapshots every active session to disk (best-effort) so a deploy or
// restart preserves user state instead of destroying it. Called on SIGTERM.
func (m *Manager) PauseAllActive(ctx context.Context) {
	for _, sess := range m.store.All() {
		if sess.State == StateActive {
			if err := m.Pause(ctx, sess.ID); err != nil {
				slog.Error("graceful pause failed", "session_id", sess.ID, "err", err)
			}
		}
	}
}

func (m *Manager) GetSession(id string) (*Session, bool) {
	return m.store.Get(id)
}

func (m *Manager) Stats() map[string]int {
	return map[string]int{"active_sessions": m.store.Count()}
}

// reaper runs every minute: it PAUSES idle active sessions (freeing RAM/slot while
// keeping state on disk), destroys sessions past their max lifetime, and hard-deletes
// paused sessions past the retention TTL.
func (m *Manager) reaper() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		for _, sess := range m.store.All() {
			switch sess.State {
			case StateActive:
				lifetimeExpired := sess.MaxLifetime > 0 && now.Sub(sess.CreatedAt) > sess.MaxLifetime
				idleExpired := sess.IdleTimeout > 0 && now.Sub(sess.LastUsed) > sess.IdleTimeout
				switch {
				case lifetimeExpired:
					slog.Info("session max lifetime reached, destroying", "session_id", sess.ID)
					if err := m.Destroy(context.Background(), sess.ID); err != nil {
						slog.Error("reaper destroy failed", "session_id", sess.ID, "err", err)
					}
				case idleExpired:
					slog.Info("session idle, pausing", "session_id", sess.ID, "idle_for", now.Sub(sess.LastUsed))
					if err := m.Pause(context.Background(), sess.ID); err != nil {
						slog.Error("reaper pause failed", "session_id", sess.ID, "err", err)
					}
				}
			case StatePaused:
				ttlExpired := now.Sub(sess.PausedAt) > pauseTTL
				lifetimeExpired := sess.MaxLifetime > 0 && now.Sub(sess.CreatedAt) > sess.MaxLifetime
				if ttlExpired || lifetimeExpired {
					slog.Info("paused session expired, destroying", "session_id", sess.ID, "ttl_expired", ttlExpired)
					if err := m.Destroy(context.Background(), sess.ID); err != nil {
						slog.Error("reaper destroy paused failed", "session_id", sess.ID, "err", err)
					}
				}
			}
		}
	}
}

func (m *Manager) Shutdown(ctx context.Context) {
	sessions := m.store.All()
	for _, sess := range sessions {
		if sess.State == StatePaused {
			continue // already snapshotted to disk; left for recovery on next start
		}
		if sess.PooledVM != nil {
			continue // pool.Shutdown handles these
		}
		if sess.Cgroup != nil {
			sess.Cgroup.Destroy()
		}
		if sess.VM != nil {
			m.vmManager.Destroy(ctx, sess.VM.ID)
		}
	}
	if m.freePool != nil {
		m.freePool.Shutdown()
	}
	if m.proPool != nil {
		m.proPool.Shutdown()
	}
}
