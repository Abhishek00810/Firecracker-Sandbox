// Worker (host agent): owns Firecracker + the execution engine, exposed over a
// private HTTP API for the control plane to dispatch to. No DB, no user auth —
// the control plane handles those; the worker just runs sandboxes on this host.
package main

import (
	"backend/internal/cgroup"
	"backend/internal/config"
	"backend/internal/executor/firecracker"
	"backend/internal/session"
	"backend/internal/vmsize"
	"backend/internal/worker"
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
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

func main() {
	setupLogger()

	cfg, err := config.LoadWorker()
	if err != nil {
		slog.Error("worker startup validation failed", "err", err)
		os.Exit(1)
	}
	for _, warning := range cfg.Warnings {
		slog.Warn("startup warning", "message", warning)
	}

	if err := cgroup.Init(); err != nil {
		slog.Warn("cgroup init failed, limits will not be enforced", "err", err)
	}

	slotCount := envInt("SLOT_COUNT", 50)
	maxProvisions := envInt("MAX_CONCURRENT_PROVISIONS", runtime.NumCPU())

	vmManager := firecracker.NewFirecrackerManager(cfg.SocketDir, cfg.AssetsPath, cfg.FirecrackerBinary, slotCount, maxProvisions, cfg.FCRunUID, cfg.FCRunGID)

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

	// Default-size template once at startup; each non-default size gets its own.
	var template *firecracker.SnapshotTemplate
	if tmpl, err := vmManager.CreateTemplate(context.Background(), baseCfg, cfg.SnapshotDir); err != nil {
		slog.Warn("snapshot template creation failed, falling back to cold boot", "err", err)
	} else {
		template = tmpl
		slog.Info("snapshot template ready", "snap", tmpl.SnapPath)
	}

	// One session pool per size (vmsize.Sizes is the single source of truth). cgroup
	// limits are derived from the size, matching the monolith.
	sizePools := make(map[string]*firecracker.VMPool, len(vmsize.Sizes))
	sizeTemplates := make(map[string]*firecracker.SnapshotTemplate, len(vmsize.Sizes))
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
		szTemplate := template
		if !sz.IsDefault() {
			snapSubDir := filepath.Join(cfg.SnapshotDir, sz.Name)
			if err := os.MkdirAll(snapSubDir, 0o755); err != nil {
				slog.Warn("could not create size snapshot dir", "size", sz.Name, "err", err)
			} else if cfg.FCRunUID > 0 {
				os.Chown(snapSubDir, cfg.FCRunUID, cfg.FCRunGID)
			}
			if tmpl, err := vmManager.CreateTemplate(context.Background(), szCfg, snapSubDir); err != nil {
				slog.Warn("size template creation failed, falling back to cold boot", "size", sz.Name, "err", err)
				szTemplate = nil
			} else {
				szTemplate = tmpl
				slog.Info("size template ready", "size", sz.Name)
			}
		}
		sizePools[sz.Key()] = firecracker.NewVMPoolWithSnapshot(0, slotCount, szCfg, vmManager, szCgroup, szTemplate, false, false)
		sizeTemplates[sz.Key()] = szTemplate
	}
	slog.Info("worker execution engine initialized", "sizes", len(sizePools))

	// No DB/metering hooks on the worker — the control plane owns those.
	sessionMgr := session.NewManager(
		vmManager,
		template,
		baseCfg,
		envInt("WORKER_MAX_SESSIONS", slotCount),
		5*time.Minute, // default idle timeout (per-session values from the request override)
		24*time.Hour,  // default max lifetime
		sizePools,
		sizeTemplates,
		nil, // onState — control plane syncs DB
		nil, // onMeter — control plane meters
	)

	bind := os.Getenv("WORKER_BIND")
	if bind == "" {
		bind = "127.0.0.1:9000"
	}
	token := os.Getenv("WORKER_TOKEN")
	if token == "" {
		slog.Warn("WORKER_TOKEN not set — worker API is UNAUTHENTICATED (dev only)")
	}
	srv := &http.Server{Addr: bind, Handler: worker.NewServer(sessionMgr, token, slotCount).Handler()}

	go func() {
		slog.Info("worker listening", "addr", bind)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("worker server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("worker shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
	sessionMgr.Shutdown(context.Background())
}
