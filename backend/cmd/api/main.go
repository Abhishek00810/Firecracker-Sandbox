package main

import (
	"backend/internal/cgroup"
	"backend/internal/config"
	"backend/internal/executor/firecracker"
	"backend/internal/handler"
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/platform"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"backend/internal/session"
	"backend/internal/tierconfig"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"golang.org/x/time/rate"
)

type HealthResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

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

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := HealthResponse{
		Status:  "ok",
		Message: "Server is healthy and is rocking!!!",
	}
	json.NewEncoder(w).Encode(resp)
}

func main() {
	setupLogger()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("startup validation failed", "err", err)
		os.Exit(1)
	}
	for _, warning := range cfg.Warnings {
		slog.Warn("startup warning", "message", warning)
	}

	platformClient := platform.NewClient(cfg.SupabaseURL, cfg.ServiceRoleKey)

	if err := cgroup.Init(); err != nil {
		slog.Warn("cgroup init failed, limits will not be enforced", "err", err)
	}

	freeTc := tierconfig.Tiers[tierconfig.PAYG]
	proTc := tierconfig.Tiers[tierconfig.PAYG]

	// Network slots are pre-created by server.sh (SLOT_COUNT). The Go slot pool MUST
	// match that count exactly: handing out a slot with no backing netns makes the
	// restore fail and fall back to cold boot. server.sh exports SLOT_COUNT; default 50.
	slotCount := 50
	if v := os.Getenv("SLOT_COUNT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			slotCount = n
		} else {
			slog.Warn("invalid SLOT_COUNT, using default", "value", v, "default", slotCount)
		}
	}

	// Admission control: bound concurrent VM provisioning so a burst can't stampede
	// the host CPU and trip the Firecracker socket-wait timeout. Defaults to the host
	// CPU count; override with MAX_CONCURRENT_PROVISIONS.
	maxProvisions := runtime.NumCPU()
	if v := os.Getenv("MAX_CONCURRENT_PROVISIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxProvisions = n
		} else {
			slog.Warn("invalid MAX_CONCURRENT_PROVISIONS, using default", "value", v, "default", maxProvisions)
		}
	}
	// Of the total budget, reserve a slice for Pro only so a Free burst can never
	// starve paying users. Default = a quarter of the budget (min 1); override with
	// PRO_RESERVED_PROVISIONS. Clamped inside the manager so Free always keeps >=1.
	proReserved := maxProvisions / 4
	if proReserved < 1 {
		proReserved = 1
	}
	if v := os.Getenv("PRO_RESERVED_PROVISIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			proReserved = n
		} else {
			slog.Warn("invalid PRO_RESERVED_PROVISIONS, using default", "value", v, "default", proReserved)
		}
	}
	slog.Info("provisioning concurrency limit", "max_concurrent_provisions", maxProvisions, "pro_reserved", proReserved)

	if cfg.FCRunUID > 0 {
		slog.Info("firecracker privilege drop enabled", "uid", cfg.FCRunUID, "gid", cfg.FCRunGID)
	}
	vmManager := firecracker.NewFirecrackerManager(cfg.SocketDir, cfg.AssetsPath, cfg.FirecrackerBinary, slotCount, maxProvisions, proReserved, cfg.FCRunUID, cfg.FCRunGID)

	config := firecracker.VMConfig{
		VCPUCount:  1, // 1 vCPU: halves thread pressure per VM → ~2x concurrency before the starvation cliff (test)
		MemSizeMiB: 256,
		DiskGB:     10, // per-VM writable disk (/dev/vdb) backing the overlay upper; sized at template build, decouples writable capacity from RAM
		Timeout:    30 * time.Second,
		KernelPath: cfg.KernelPath,
		RootfsPath: cfg.RootfsPath,
		InitrdPath: cfg.InitrdPath,
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off random.trust_cpu=on rng_core.default_quality=1024",
	}

	freeCgroupCfg := cgroup.Config{
		CPUQuotaUS:  50_000, // 0.5 core
		CPUPeriodUS: 100_000,
		MemMaxBytes: 256 * 1024 * 1024, // 256MB
	}
	premiumCgroupCfg := cgroup.Config{
		CPUQuotaUS:  200_000, // 2 cores
		CPUPeriodUS: 100_000,
		MemMaxBytes: 512 * 1024 * 1024, // 512MB
	}

	freeSessionCgroupCfg := cgroup.Config{
		CPUQuotaUS:  100_000, // 1 core
		CPUPeriodUS: 100_000,
		MemMaxBytes: 512 * 1024 * 1024, // 512MB
	}
	premiumSessionCgroupCfg := cgroup.Config{
		CPUQuotaUS:  400_000, // 4 cores
		CPUPeriodUS: 100_000,
		MemMaxBytes: 1024 * 1024 * 1024, // 1GB
	}

	// Create a snapshot template once at startup: boot one VM, warm up kernels,
	// freeze it. All pool VMs and sessions restore from this snapshot in ~100ms.
	var template *firecracker.SnapshotTemplate
	if tmpl, err := vmManager.CreateTemplate(context.Background(), config, cfg.SnapshotDir); err != nil {
		slog.Warn("snapshot template creation failed, falling back to cold boot", "err", err)
	} else {
		template = tmpl
		slog.Info("snapshot template ready", "snap", tmpl.SnapPath)
	}

	// Pro pools carry Pro=true so provisioning uses the reserved/priority path.
	proConfig := config
	proConfig.Pro = true

	freePool := firecracker.NewVMPoolWithSnapshot(0, freeTc.MaxPoolSize, config, vmManager, freeCgroupCfg, template, false, false)
	premiumPool := firecracker.NewVMPoolWithSnapshot(0, proTc.MaxPoolSize, proConfig, vmManager, premiumCgroupCfg, template, false, false)
	freeSessionPool := firecracker.NewVMPoolWithSnapshot(0, freeTc.MaxPoolSize, config, vmManager, freeSessionCgroupCfg, template, false, false)
	proSessionPool := firecracker.NewVMPoolWithSnapshot(0, proTc.MaxPoolSize, proConfig, vmManager, premiumSessionCgroupCfg, template, false, false)
	slog.Info("VM pools initialized")

	// Sync every session transition — manual AND automatic (idle-pause, on-demand resume,
	// TTL destroy) — to the sandboxes DB so the dashboard state is always truthful.
	sessionStateHook := func(sessionID, userID, state string) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			platformClient.UpdateSandboxState(ctx, sessionID, state)
			msg := map[string]string{"paused": "sandbox paused", "active": "sandbox resumed", "destroyed": "sandbox destroyed"}[state]
			if msg != "" && userID != "" {
				platformClient.InsertSandboxLog(ctx, platform.SandboxLog{SandboxID: sessionID, UserID: userID, Stream: "system", Level: "info", Content: msg})
			}
		}()
	}

	// Raw resource-time metering: the manager's ticker hands us per-minute deltas; we write
	// them as raw units to usage_meters (no cost — pricing is applied later via tier_rates).
	sessionMeterHook := func(s session.MeterSample) {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			platformClient.InsertUsageMeter(ctx, platform.UsageMeter{
				UserID:        s.UserID,
				SandboxID:     s.SandboxID,
				Tier:          s.Tier,
				Bucket:        time.Now().UTC().Format(time.RFC3339),
				VCPUSeconds:   s.VCPUSeconds,
				RAMGBSeconds:  s.RAMGBSeconds,
				DiskGBSeconds: s.DiskGBSeconds,
			})
		}()
	}

	sessionMgr := session.NewManager(
		vmManager,
		template,
		config,
		freeSessionCgroupCfg,
		premiumSessionCgroupCfg,
		proTc.MaxSessions,
		proTc.SessionIdleTimeout,
		proTc.SessionMaxLifetime,
		freeSessionPool,
		proSessionPool,
		sessionStateHook,
		sessionMeterHook,
	)

	// Startup reconciliation: on restart, active sessions are lost (only paused ones are
	// recovered from the manifest). Any sandbox the DB still marks active/paused that isn't
	// in the live store is a ghost — mark it destroyed so the DB, dashboard, and meter match
	// reality. NOTE: single-host assumption; multi-host must scope this by host_id.
	func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		refs, err := platformClient.ListSandboxesByState(ctx, []string{"active", "paused"})
		if err != nil {
			slog.Warn("startup reconcile: list sandboxes failed", "err", err)
			return
		}
		ghosts, synced := 0, 0
		for _, ref := range refs {
			sess, ok := sessionMgr.GetSession(ref.ID)
			if !ok {
				// Not in the live store → the VM is gone (active session lost on restart, or
				// paused snapshot missing). Mark destroyed.
				platformClient.UpdateSandboxState(ctx, ref.ID, "destroyed")
				ghosts++
				continue
			}
			// In the store but the DB state disagrees (e.g. shutdown pause's async write was
			// lost) → make the DB match the store's actual state.
			if actual := string(sess.State); actual != ref.State {
				platformClient.UpdateSandboxState(ctx, ref.ID, actual)
				synced++
			}
		}
		slog.Info("startup reconcile complete", "db_active_paused", len(refs), "ghosts_destroyed", ghosts, "state_synced", synced)
	}()

	freeExec := firecracker.NewFirecrackerExecutor(vmManager)
	freeExec.Pool = freePool
	freeQueue := queue.NewJobQueue(freeExec, freeTc.MaxPoolSize)
	freeQueue.Start()

	premiumExec := firecracker.NewFirecrackerExecutor(vmManager)
	premiumExec.Pool = premiumPool
	premiumQueue := queue.NewJobQueue(premiumExec, proTc.MaxPoolSize)
	premiumQueue.Start()

	freeLimiter := ratelimit.NewTenantLimiter(rate.Limit(freeTc.RateLimit), freeTc.RateBurst)
	premiumLimiter := ratelimit.NewTenantLimiter(rate.Limit(proTc.RateLimit), proTc.RateBurst)

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(freeQueue, premiumQueue, freeLimiter, premiumLimiter, platformClient))
	http.HandleFunc("/session", handler.SessionHandler(sessionMgr, platformClient))
	http.HandleFunc("/session/", handler.SessionHandler(sessionMgr, platformClient))

	metricsHandler := func(w http.ResponseWriter, r *http.Request) {
		snap := metrics.GetSnapshot()
		freeAvail, freeInUse := freePool.Stats()
		premAvail, premInUse := premiumPool.Stats()
		snap.FreePoolAvailable = freeAvail
		snap.FreePoolInUse = freeInUse
		snap.ProPoolAvailable = premAvail
		snap.ProPoolInUse = premInUse
		snap.FreeQueueDepth = freeQueue.Depth()
		snap.ProQueueDepth = premiumQueue.Depth()
		snap.VMPoolAvailable = freeAvail + premAvail
		snap.VMPoolInUse = freeInUse + premInUse
		snap.QueueDepth = snap.FreeQueueDepth + snap.ProQueueDepth
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}

	http.HandleFunc("/metrics", metricsHandler)

	port := ":" + cfg.Port
	slog.Info("server is running", "port", port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Middleware chain: Logging → Auth → mux
	chain := middleware.Logging(middleware.Auth(platformClient, cfg.SupabaseURL)(http.DefaultServeMux))

	// Configured server with Slowloris-safe timeouts. ReadHeaderTimeout caps how long a
	// client may take to send request headers — this is the Slowloris defense and is safe
	// for any workload length (it limits transmission, not processing). WriteTimeout is
	// deliberately left unset (0): long executions are bounded per-request via
	// context.WithTimeout in the handlers, so a blanket response deadline here would kill
	// legitimate long-running jobs.
	srv := &http.Server{
		Addr:              port,
		Handler:           chain,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("error serving API", "err", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down, cleaning up VMs")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http server shutdown error", "err", err)
	}

	// Graceful pause: snapshot every active session to disk before teardown so a deploy
	// or restart preserves user state (recovered from the manifest on next start) instead
	// of destroying it. Bounded by systemd's TimeoutStopSec — ensure that is generous.
	pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 50*time.Second)
	sessionMgr.PauseAllActive(pauseCtx)
	pauseCancel()

	freePool.Shutdown()
	premiumPool.Shutdown()
	sessionMgr.Shutdown(context.Background())
	slog.Info("shutdown complete")
}
