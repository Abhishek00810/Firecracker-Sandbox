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

	freeTc := tierconfig.Tiers[tierconfig.Free]
	proTc := tierconfig.Tiers[tierconfig.Pro]

	vmManager := firecracker.NewFirecrackerManager(cfg.SocketDir, cfg.AssetsPath, cfg.FirecrackerBinary)

	config := firecracker.VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
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

	freePool := firecracker.NewVMPoolWithSnapshot(freeTc.PoolSize, config, vmManager, freeCgroupCfg, template, false, false)
	premiumPool := firecracker.NewVMPoolWithSnapshot(proTc.PoolSize, config, vmManager, premiumCgroupCfg, template, false, true)
	freeSessionPool := firecracker.NewVMPoolWithSnapshot(freeTc.PoolSize, config, vmManager, freeSessionCgroupCfg, template, false, false)
	proSessionPool := firecracker.NewVMPoolWithSnapshot(proTc.PoolSize, config, vmManager, premiumSessionCgroupCfg, template, true, false)
	slog.Info("VM pools initialized")

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
	)

	freeExec := firecracker.NewFirecrackerExecutor(vmManager)
	freeExec.Pool = freePool
	freeQueue := queue.NewJobQueue(freeExec, freeTc.Workers)
	freeQueue.Start()

	premiumExec := firecracker.NewFirecrackerExecutor(vmManager)
	premiumExec.Pool = premiumPool
	premiumQueue := queue.NewJobQueue(premiumExec, proTc.Workers)
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
	chain := middleware.Logging(middleware.Auth(platformClient)(http.DefaultServeMux))

	go func() {
		if err := http.ListenAndServe(port, chain); err != nil {
			slog.Error("error serving API", "err", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down, cleaning up VMs")
	freePool.Shutdown()
	premiumPool.Shutdown()
	sessionMgr.Shutdown(context.Background())
	slog.Info("shutdown complete")
}
