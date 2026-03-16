package main

import (
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	"backend/internal/handler"
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/queue"
	"backend/internal/ratelimit"
	"backend/internal/session"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
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
	if err := cgroup.Init(); err != nil {
		slog.Warn("cgroup init failed, limits will not be enforced", "err", err)
	}

	socketDir := filepath.Join(os.TempDir(), "fc-sockets")
	assetsPath := os.Getenv("ASSETS_PATH")
	if assetsPath == "" {
		assetsPath = "/app/assets"
	}

	if err := os.MkdirAll(socketDir, 0755); err != nil {
		slog.Error("failed to create socket directory", "err", err)
		os.Exit(1)
	}

	vmManager := firecracker.NewFirecrackerManager(socketDir, assetsPath)

	config := firecracker.VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    30 * time.Second,
		KernelPath: filepath.Join(assetsPath, "kernel/vmlinux"),
		RootfsPath: filepath.Join(assetsPath, "rootfs/rootfs-alpine.ext4"),
		InitrdPath: filepath.Join(assetsPath, "initramfs.cpio.gz"),
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off",
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
	snapDir := "/dev/shm/fc-snapshots"
	var template *firecracker.SnapshotTemplate
	if tmpl, err := vmManager.CreateTemplate(context.Background(), config, snapDir); err != nil {
		slog.Warn("snapshot template creation failed, falling back to cold boot", "err", err)
	} else {
		template = tmpl
		slog.Info("snapshot template ready", "snap", tmpl.SnapPath)
	}

	freePool := firecracker.NewVMPoolWithSnapshot(3, config, vmManager, freeCgroupCfg, template)
	premiumPool := firecracker.NewVMPoolWithSnapshot(3, config, vmManager, premiumCgroupCfg, template)
	slog.Info("VM pools initialized")

	sessionMgr := session.NewManager(
		vmManager,
		template,
		config,
		freeSessionCgroupCfg,
		premiumSessionCgroupCfg,
		50,
		15*time.Minute,
	)

	freeExec := firecracker.NewFirecrackerExecutor(vmManager)
	freeExec.Pool = freePool
	freeQueue := queue.NewJobQueue(freeExec, 5)
	freeQueue.Start()

	premiumExec := firecracker.NewFirecrackerExecutor(vmManager)
	premiumExec.Pool = premiumPool
	premiumQueue := queue.NewJobQueue(premiumExec, 10)
	premiumQueue.Start()

	freeLimiter := ratelimit.NewTenantLimiter(rate.Limit(2), 5)      // 2 req/sec, burst 5
	premiumLimiter := ratelimit.NewTenantLimiter(rate.Limit(10), 20) // 10 req/sec, burst 20

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(freeQueue, premiumQueue, freeLimiter, premiumLimiter))
	http.HandleFunc("/session", handler.SessionHandler(sessionMgr))
	http.HandleFunc("/session/", handler.SessionHandler(sessionMgr))

	metricsHandler := func(w http.ResponseWriter, r *http.Request) {
		snap := metrics.GetSnapshot()
		freeAvail, freeInUse := freePool.Stats()
		premAvail, premInUse := premiumPool.Stats()
		snap.VMPoolAvailable = freeAvail + premAvail
		snap.VMPoolInUse = freeInUse + premInUse
		snap.QueueDepth = freeQueue.Depth() + premiumQueue.Depth()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}

	http.HandleFunc("/metrics", metricsHandler)

	port := ":8080"
	slog.Info("server is running", "port", port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := http.ListenAndServe(port, middleware.Logging(http.DefaultServeMux)); err != nil {
			slog.Error("error serving API", "err", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down, cleaning up VMs")
	freePool.Shutdown()
	premiumPool.Shutdown()
	slog.Info("shutdown complete")
}
