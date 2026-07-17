package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/adapters/postgres"
	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/api/httpapi"
	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/config"
	"github.com/renderops-ai/renderops-sandbox/services/control-plane/internal/workers/registry"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid control-plane configuration", "err", err)
		os.Exit(1)
	}

	_, err = registry.NewStatic(cfg.WorkerID, cfg.WorkerURL)
	if err != nil {
		slog.Error("invalid worker registry configuration", "err", err)
		os.Exit(1)
	}

	// Connect the sandbox store (SandboxStore port). Execution routes that use it
	// are gated until auth + create/allocation land; connecting here fails fast on
	// a bad DATABASE_URL so a misconfigured control plane never starts.
	store, err := postgres.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer store.Close()
	slog.Info("database connected")

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpapi.NewRouter(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		slog.Info("control plane listening", "addr", srv.Addr, "worker_id", cfg.WorkerID)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("control plane server failed", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		slog.Warn("control plane shutdown failed", "err", err)
	}
}
