// Worker (host agent): owns Firecracker + the execution engine, exposed over a
// private HTTP API for the control plane to dispatch to. No DB, no user auth —
// the control plane handles those; the worker just runs sandboxes on this host.
package main

import (
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	"backend/internal/orchestrator"
	"backend/internal/plane"
	"backend/internal/session"
	"backend/internal/vmsize"
	"backend/internal/worker"
	"backend/internal/workerplane/host"
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func setupLogger() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if os.Getenv("LOG_FORMAT") == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func startOrchestratorRegistration(ctx context.Context, slotCount int) {
	baseURL := os.Getenv("ORCHESTRATOR_URL")
	if baseURL == "" {
		slog.Warn("ORCHESTRATOR_URL not set; worker registration and heartbeat are disabled")
		return
	}
	registration := orchestrator.WorkerRegistration{
		ID:                  os.Getenv("WORKER_ID"),
		Endpoint:            os.Getenv("WORKER_ADVERTISE_URL"),
		Pool:                os.Getenv("WORKER_POOL"),
		AllocatableVCPUs:    envInt("WORKER_ALLOCATABLE_VCPUS", 0),
		AllocatableMemoryMB: envInt("WORKER_ALLOCATABLE_MEMORY_MB", 0),
		AllocatableDiskGB:   envInt("WORKER_ALLOCATABLE_DISK_GB", 0),
		MaxSandboxes:        envInt("WORKER_MAX_SESSIONS", slotCount),
	}
	if registration.ID == "" ||
		registration.Endpoint == "" ||
		registration.AllocatableVCPUs <= 0 ||
		registration.AllocatableMemoryMB <= 0 ||
		registration.AllocatableDiskGB <= 0 {
		slog.Error(
			"worker registration disabled because identity or allocatable capacity is missing",
			"worker_id", registration.ID,
			"endpoint", registration.Endpoint,
		)
		return
	}

	client := orchestrator.NewClient(baseURL, os.Getenv("WORKER_TOKEN"))
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		registered := false
		for {
			if !registered {
				if err := client.RegisterWorker(ctx, registration); err != nil {
					slog.Warn("worker registration failed", "worker_id", registration.ID, "err", err)
				} else {
					registered = true
					slog.Info("worker registered", "worker_id", registration.ID, "endpoint", registration.Endpoint)
				}
			} else if err := client.Heartbeat(ctx, registration.ID); err != nil {
				registered = false
				slog.Warn("worker heartbeat failed; registration will be retried", "worker_id", registration.ID, "err", err)
			}

			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// createTemplateWithRetry wraps CreateTemplate with bounded retries. Template VM
// warm-up occasionally fails because the guest-agent vsock isn't ready in time —
// especially during the startup burst, when several size templates warm up back
// to back and the previous template VM's kernel-side teardown (netns / vsock CID /
// TAP) is still settling. A short settle delay plus a retry turns that transient
// flake into a reliably warm template, so a size never silently drops to cold-boot
// (which disables pause/resume for that size). This mirrors the resume-path vsock
// handshake retry (2103f8f); the host has ample spare capacity, so the retry cost
// is negligible on the rare flaky boot.
func createTemplateWithRetry(mgr *firecracker.FireCrackerManager, cfg firecracker.VMConfig, snapDir string, attempts int) (*firecracker.SnapshotTemplate, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			time.Sleep(2 * time.Second) // let the previous template VM's teardown settle
		}
		tmpl, err := mgr.CreateTemplate(context.Background(), cfg, snapDir)
		if err == nil {
			if i > 0 {
				slog.Info("template creation succeeded on retry", "attempt", i+1, "snap_dir", snapDir)
			}
			return tmpl, nil
		}
		lastErr = err
		slog.Warn("template creation failed, retrying", "attempt", i+1, "attempts", attempts, "snap_dir", snapDir, "err", err)
	}
	return nil, lastErr
}

func main() {
	setupLogger()

	// Shared host bring-up (unpack+validate assets, provision networking/user,
	// cgroup init, Firecracker manager). Identical to before — just extracted so
	// the template-builder can reuse the exact same host readiness.
	h, err := host.Init()
	if err != nil {
		slog.Error("worker startup failed", "err", err)
		os.Exit(1)
	}
	cfg := h.Config
	vmManager := h.VMManager
	slotCount := h.SlotCount

	baseCfg := firecracker.VMConfig{
		VCPUCount:  1,
		MemSizeMiB: 256,
		DiskGB:     10,
		Timeout:    30 * time.Second,
		KernelPath: cfg.KernelPath,
		RootfsPath: cfg.RootfsPath,
		InitrdPath: cfg.InitrdPath,
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off random.trust_cpu=on rng_core.default_quality=1024",
	}

	// Templates: download a prebuilt release from object storage (TEMPLATE_SOURCE=
	// prebuilt — restore-only) or build them locally at startup (default). Fail closed
	// on resolution failure — production must not silently cold-boot.
	sizeTemplates, err := resolveTemplates(context.Background(), cfg, vmManager, baseCfg)
	if err != nil {
		slog.Error("template resolution failed", "err", err)
		os.Exit(1)
	}
	defaultTemplate := sizeTemplates[vmsize.Default().Key()]

	// One session pool per size (vmsize.Sizes is the single source of truth). cgroup
	// limits are derived from the size, matching the monolith.
	sizePools := make(map[string]*firecracker.VMPool, len(vmsize.Sizes))
	for _, sz := range vmsize.Sizes {
		szCfg := baseCfg
		szCfg.VCPUCount = sz.VCPUs
		szCfg.MemSizeMiB = sz.MemoryMB
		szCfg.DiskGB = sz.DiskGB
		szCgroup := cgroup.Config{
			CPUQuotaUS:  sz.CgroupCPUQuotaUS(),
			CPUPeriodUS: vmsize.CgroupCPUPeriodUS,
			MemMaxBytes: sz.CgroupMemMaxBytes(),
		}
		sizePools[sz.Key()] = firecracker.NewVMPoolWithSnapshot(0, slotCount, szCfg, vmManager, szCgroup, sizeTemplates[sz.Key()], false, false)
	}
	slog.Info("worker execution engine initialized", "sizes", len(sizePools), "template_source", env("TEMPLATE_SOURCE", "build"))

	var onState session.StateHook
	if orchestratorURL, workerID := os.Getenv("ORCHESTRATOR_URL"), os.Getenv("WORKER_ID"); orchestratorURL != "" && workerID != "" {
		orchestrationClient := orchestrator.NewClient(orchestratorURL, os.Getenv("WORKER_TOKEN"))
		onState = func(sandboxID, _ string, state string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return orchestrationClient.ReportWorkerState(ctx, workerID, sandboxID, state)
		}
	}

	// The worker never accesses the DB. Lifecycle events go to the orchestrator;
	// metering remains owned outside the worker.
	sessionMgr := session.NewManager(
		vmManager,
		defaultTemplate,
		baseCfg,
		envInt("WORKER_MAX_SESSIONS", slotCount),
		5*time.Minute, // default idle timeout (per-session values from the request override)
		24*time.Hour,  // default max lifetime
		sizePools,
		sizeTemplates,
		onState,
		nil, // onMeter — control plane meters
	)

	bind := os.Getenv("WORKER_BIND")
	if bind == "" {
		bind = plane.DefaultAgentAddr
	}
	token := os.Getenv("WORKER_TOKEN")
	if token == "" {
		slog.Warn("WORKER_TOKEN not set — worker API is UNAUTHENTICATED (dev only)")
	}
	workerServer := worker.NewServer(sessionMgr, token, slotCount)
	srv := &http.Server{Addr: bind, Handler: workerServer.Handler()}
	listener, err := net.Listen("tcp", bind)
	if err != nil {
		slog.Error("worker listen failed", "addr", bind, "err", err)
		os.Exit(1)
	}

	go func() {
		slog.Info("worker listening", "addr", bind)
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("worker server failed", "err", err)
			os.Exit(1)
		}
	}()

	registrationCtx, stopRegistration := context.WithCancel(context.Background())
	startOrchestratorRegistration(registrationCtx, slotCount)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("worker shutting down")

	// Close every admission path before snapshotting. The local gate handles a
	// placement request already in flight while the durable orchestrator flag
	// removes this worker from subsequent placement queries.
	workerServer.BeginDrain()
	stopRegistration()
	if orchestratorURL, workerID := os.Getenv("ORCHESTRATOR_URL"), os.Getenv("WORKER_ID"); orchestratorURL != "" && workerID != "" {
		drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := orchestrator.NewClient(orchestratorURL, os.Getenv("WORKER_TOKEN")).
			SetWorkerDraining(drainCtx, workerID, true)
		drainCancel()
		if err != nil {
			slog.Error("mark worker draining failed", "worker_id", workerID, "err", err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := srv.Shutdown(ctx); err != nil {
		slog.Error("worker HTTP shutdown failed", "err", err)
	}
	cancel()

	pauseTimeout := time.Duration(envInt("SHUTDOWN_GRACE_PERIOD_SECONDS", 300)) * time.Second
	pauseCtx, pauseCancel := context.WithTimeout(context.Background(), pauseTimeout)
	if err := sessionMgr.PauseAllActive(
		pauseCtx,
		envInt("SHUTDOWN_PAUSE_CONCURRENCY", 4),
	); err != nil {
		slog.Error("worker drained with unpaused sessions", "err", err)
	}
	pauseCancel()

	sessionMgr.Shutdown(context.Background())
}
