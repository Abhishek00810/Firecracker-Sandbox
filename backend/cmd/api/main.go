package main

import (
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	"backend/internal/handler"
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/queue"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"
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
		slog.Error("Failed to create socket directory", "err", err)
		os.Exit(1)
	}

	vmManager := firecracker.NewFirecrackerManager(socketDir, assetsPath)
	firecrackerExec := firecracker.NewFirecrackerExecutor(vmManager)

	config := firecracker.VMConfig{
		VCPUCount:  2,
		MemSizeMiB: 256,
		Timeout:    30 * time.Second,
		KernelPath: filepath.Join(assetsPath, "kernel/vmlinux"),
		RootfsPath: filepath.Join(assetsPath, "rootfs/rootfs-alpine.ext4"),
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off init=/usr/local/bin/guest-agent",
	}
	cgroupCfg := cgroup.Config{
		CPUQuotaUS:  100_000,
		CPUPeriodUS: 100_000,
	}
	pool := firecracker.NewVMPool(3, config, vmManager, cgroupCfg)
	slog.Info("Firecracker executor initialized successfully")
	firecrackerExec.Pool = pool
	jobQueue := queue.NewJobQueue(firecrackerExec, 10)
	jobQueue.Start()

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", handler.ExecuteHandler(jobQueue))

	metricsHandler := func(w http.ResponseWriter, r *http.Request) {
		snap := metrics.GetSnapshot()
		avail, inUse := pool.Stats()
		snap.VMPoolAvailable = avail
		snap.VMPoolInUse = inUse
		snap.QueueDepth = jobQueue.Depth()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(snap)
	}
	http.HandleFunc("/metrics", metricsHandler)

	port := ":8080"
	slog.Info("Server is running", "port", port)

	if err := http.ListenAndServe(port, middleware.Logging(http.DefaultServeMux)); err != nil {
		slog.Error("Error serving API", "err", err)
		os.Exit(1)
	}
}
