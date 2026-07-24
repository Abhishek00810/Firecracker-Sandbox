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
	"backend/internal/vmsize"

	"github.com/google/uuid"
)

// pauseTTL is how long a paused session's snapshot is retained before it's hard-deleted.
const pauseTTL = 7 * 24 * time.Hour

// StateHook is called on every session lifecycle transition — manual (endpoint) AND
// automatic (reaper idle-pause, on-demand resume, TTL destroy) — so an external store
// (the sandboxes DB) can stay in sync. state is "active" | "paused" | "destroyed".
type StateHook func(sessionID, userID, state string)

// MeterSample is one accrual interval's RAW resource-time for a sandbox (no cost).
type MeterSample struct {
	UserID        string
	SandboxID     string
	BillingModel  string
	VCPUSeconds   float64
	RAMGBSeconds  float64
	DiskGBSeconds float64
}

// MeterHook receives raw usage deltas from the accrual ticker to persist (usage_meters).
type MeterHook func(MeterSample)

type Manager struct {
	store         *Store
	onState       StateHook
	onMeter       MeterHook
	vmManager     *firecracker.FireCrackerManager
	template      *firecracker.SnapshotTemplate
	idleTimeout   time.Duration
	maxLifetime   time.Duration
	vmConfig      firecracker.VMConfig
	sizePools     map[string]*firecracker.VMPool           // keyed by vmsize.Key — one pool per resource shape
	sizeTemplates map[string]*firecracker.SnapshotTemplate // keyed by vmsize.Key — per-size baked device names (for pause/resume)
	pauseDir      string                                   // disk-backed dir for per-session pause snapshots (NOT /dev/shm)
	manifestPath  string                                   // recovery manifest of paused sessions
}

func NewManager(
	vmManager *firecracker.FireCrackerManager,
	template *firecracker.SnapshotTemplate,
	vmConfig firecracker.VMConfig,
	maxSessions int,
	idleTimeout time.Duration,
	maxLifetime time.Duration,
	sizePools map[string]*firecracker.VMPool,
	sizeTemplates map[string]*firecracker.SnapshotTemplate,
	onState StateHook,
	onMeter MeterHook,
) *Manager {
	m := &Manager{
		store:         NewStore(maxSessions),
		onState:       onState,
		onMeter:       onMeter,
		vmManager:     vmManager,
		template:      template,
		vmConfig:      vmConfig,
		idleTimeout:   idleTimeout,
		maxLifetime:   maxLifetime,
		sizePools:     sizePools,
		sizeTemplates: sizeTemplates,
		pauseDir:      filepath.Join(vmManager.SocketDir, "pause"),
		manifestPath:  filepath.Join(vmManager.SocketDir, "paused-sessions.json"),
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
	m.cleanupOrphanedDisks()
	go m.reaper()
	if onMeter != nil {
		go m.meterTicker()
	}
	return m
}

// meterTicker accrues RAW resource-time for every live session once a minute and hands each
// delta to onMeter (which persists it to usage_meters). Compute (vCPU-s + RAM GB-s) accrues
// while ACTIVE; disk (GB-s) accrues while the sandbox exists (active OR paused). No cost is
// computed here; pricing is applied later at read time via pricing_rates.
func (m *Manager) meterTicker() {
	const interval = time.Minute
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	last := time.Now()
	for range ticker.C {
		now := time.Now()
		elapsed := now.Sub(last).Seconds()
		last = now
		if elapsed <= 0 {
			continue
		}
		for _, sess := range m.store.All() {
			if sess.UserID == "" {
				continue // can't attribute — skip (avoids orphaned meter rows)
			}
			sample := MeterSample{
				UserID:        sess.UserID,
				SandboxID:     sess.ID,
				BillingModel:  sess.BillingModel,
				DiskGBSeconds: float64(sess.DiskGB) * elapsed, // disk billed while alive (active + paused)
			}
			if sess.State == StateActive {
				sample.VCPUSeconds = float64(sess.VCPUs) * elapsed
				sample.RAMGBSeconds = (float64(sess.MemoryMB) / 1024.0) * elapsed
			}
			m.onMeter(sample)
		}
	}
}

// notifyState fires the state hook (if set) so the DB reflects a transition. Best-effort:
// the hook itself is expected to be async and non-blocking.
func (m *Manager) notifyState(sess *Session, state string) {
	if m.onState != nil && sess != nil {
		m.onState(sess.ID, sess.UserID, state)
	}
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

// cleanupOrphanedDisks removes leaked per-VM writable disks and stale pause
// snapshots left behind by ungraceful VM/worker death — e.g. a `systemctl restart`
// kills running VMs without ever calling teardown, so their writable-*.ext4 (and
// any half-written pause snapshot) are never removed. Run ONCE at startup, AFTER
// recoverPaused: at that point no live/pool VMs exist yet, so the only files worth
// keeping are those owned by a recovered PAUSED session. Anything else is an orphan.
// Without this, orphans accumulate until the disk fills and pause snapshots fail
// with ENOSPC.
func (m *Manager) cleanupOrphanedDisks() {
	// Keep-set: writable disks + pause dirs we must NOT delete.
	keepDisk := make(map[string]bool)
	keepPause := make(map[string]bool)
	// (a) The template GOLDEN writable disks — every pool restore reflink-clones
	// from these, so deleting them breaks all restores (create + resume). They
	// live as writable-*.ext4 too, but belong to no session.
	if m.template != nil && m.template.WritableDiskPath != "" {
		keepDisk[m.template.WritableDiskPath] = true
	}
	for _, t := range m.sizeTemplates {
		if t != nil && t.WritableDiskPath != "" {
			keepDisk[t.WritableDiskPath] = true
		}
	}
	// (b) Disks + pause snapshots owned by recovered paused sessions.
	for _, s := range m.store.All() {
		if s.State != StatePaused {
			continue
		}
		if s.WritableDiskPath != "" {
			keepDisk[s.WritableDiskPath] = true
		}
		keepPause[s.ID] = true
	}

	// 1. Orphaned per-VM writable disks in the socket dir.
	removedDisks := 0
	writables, _ := filepath.Glob(filepath.Join(m.vmManager.SocketDir, "writable-*.ext4"))
	for _, p := range writables {
		if keepDisk[p] {
			continue
		}
		if err := os.Remove(p); err == nil {
			removedDisks++
		}
	}

	// 2. Stale pause snapshot dirs (pause/<session-id>) with no matching paused session.
	removedPauses := 0
	if entries, err := os.ReadDir(m.pauseDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || keepPause[e.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(m.pauseDir, e.Name())); err == nil {
				removedPauses++
			}
		}
	}

	if removedDisks > 0 || removedPauses > 0 {
		slog.Info("cleaned orphaned VM disks + stale pause snapshots at startup",
			"writable_disks_removed", removedDisks,
			"pause_snapshots_removed", removedPauses,
			"kept_paused_sessions", len(keepPause))
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
func (m *Manager) Create(ctx context.Context, userID, billingModel string, env map[string]string, vcpus, memoryMB, diskGB int, internet bool, idleTimeout, maxLifetime time.Duration) (*Session, error) {
	t0 := time.Now()

	pool := m.sizePools[vmsize.Key(vcpus, memoryMB, diskGB)]
	if pool == nil {
		return nil, fmt.Errorf("no VM pool for size %dvcpu/%dMB/%dGB", vcpus, memoryMB, diskGB)
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
		ID:           uuid.New().String(),
		UserID:       userID,
		VM:           pvm.VM,
		Cgroup:       pvm.Cgroup,
		PooledVM:     pvm,
		Pool:         pool,
		BillingModel: billingModel,
		VCPUs:        vcpus,
		MemoryMB:     memoryMB,
		DiskGB:       diskGB,
		Env:          env,
		Internet:     internet,
		IdleTimeout:  idleTimeout,
		MaxLifetime:  maxLifetime,
		CreatedAt:    time.Now(),
		LastUsed:     time.Now(),
		State:        StateActive,
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

	slog.Info("session created", "session_id", sess.ID, "vm_id", pvm.VM.ID, "billing_model", billingModel, "warm", warm, "ms", time.Since(t0).Milliseconds())
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
		m.notifyState(sess, "destroyed")
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

	m.notifyState(sess, "destroyed")
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
	// those. Each size has its own template with its own baked device names, so use the
	// session's OWN size template (not the default). Persisted in the manifest for recovery.
	vmID := sess.VM.ID
	tmpl := m.sizeTemplates[vmsize.Key(sess.VCPUs, sess.MemoryMB, sess.DiskGB)]
	if tmpl == nil {
		tmpl = m.template
	}
	sess.VsockPathAtPause = tmpl.VsockPath
	sess.TapNameAtPause = tmpl.TapName
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
	m.notifyState(sess, "paused")
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

	vm, err := m.vmManager.ResumeFromSnapshot(ctx, cfg, tmpl)
	if err != nil {
		return fmt.Errorf("resume from snapshot: %w", err)
	}
	msRestore := time.Since(t0).Milliseconds()

	if err := m.vmManager.SetSlotEgress(vm.Slot, sess.Internet); err != nil {
		m.vmManager.Destroy(ctx, vm.ID)
		return fmt.Errorf("re-apply network policy on resume: %w", err)
	}

	// Recreate the cgroup for the new VM process, sized from the session's own resources
	// (matches the pool's per-size session cgroup on the original create).
	sz := vmsize.Size{VCPUs: sess.VCPUs, MemoryMB: sess.MemoryMB, DiskGB: sess.DiskGB}
	cgCfg := cgroup.Config{
		CPUQuotaUS:  sz.CgroupCPUQuotaUS(),
		CPUPeriodUS: vmsize.CgroupCPUPeriodUS,
		MemMaxBytes: sz.CgroupMemMaxBytes(),
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
	msPreConnect := time.Since(t0).Milliseconds()

	// Open a fresh persistent connection — this is the ONE vsock connection the restored VM
	// allows, reused for every subsequent exec/run (its CONNECT waits for the agent to accept).
	vsockClient := firecracker.NewVsockClient(vm.VsockPath)
	conn, err := vsockClient.Connect()
	msConnect := time.Since(t0).Milliseconds()
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
	msReset := time.Since(t0).Milliseconds()
	if len(sess.Env) > 0 {
		if err := vsockClient.SetEnvOnConn(conn, sess.Env); err != nil {
			slog.Warn("re-inject env on resume failed", "session_id", sessionID, "err", err)
		}
	}
	msSetEnv := time.Since(t0).Milliseconds()

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
	m.notifyState(sess, "active")
	// Per-phase timing (cumulative ms from t0) to locate slow resumes, esp. cold restore
	// after a process restart. connect_ms - preconnect_ms is the vsock CONNECT wait.
	slog.Info("session resumed", "session_id", sessionID, "vm_id", vm.ID,
		"ms", time.Since(t0).Milliseconds(),
		"restore_ms", msRestore, "preconnect_ms", msPreConnect,
		"connect_ms", msConnect, "reset_ms", msReset, "setenv_ms", msSetEnv)
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
				// Idle auto-pause is disabled: snapshot-resume is broken (vsock
				// rebind fails), so a paused sandbox is unrecoverable. Sessions
				// stay active until their hard max lifetime, then are destroyed.
				if sess.MaxLifetime > 0 && now.Sub(sess.CreatedAt) > sess.MaxLifetime {
					slog.Info("session max lifetime reached, destroying", "session_id", sess.ID)
					if err := m.Destroy(context.Background(), sess.ID); err != nil {
						slog.Error("reaper destroy failed", "session_id", sess.ID, "err", err)
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
	for _, p := range m.sizePools {
		if p != nil {
			p.Shutdown()
		}
	}
}
