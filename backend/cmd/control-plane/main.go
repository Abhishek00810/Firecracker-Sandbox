// Control plane: auth + Postgres + the public REST API, with no local VMs.
// First step of the distributed split — /session lifecycle works end-to-end
// against the DB (so the SvelteKit dashboard is fully functional), while
// /execute and /session/:id/{run,exec} return a clear "no agent" error until
// remote host agents are implemented.
package main

import (
	"backend/internal/agent"
	"backend/internal/config"
	"backend/internal/controlplane"
	"backend/internal/handler"
	"backend/internal/middleware"
	"backend/internal/platform"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"message": "control plane is healthy",
		"role":    "control-plane",
	})
}

func main() {
	setupLogger()

	cfg, err := config.LoadControlPlane()
	if err != nil {
		slog.Error("startup validation failed", "err", err)
		os.Exit(1)
	}

	platformClient, err := platform.NewClient(context.Background(), cfg.DatabaseURL)
	if err != nil {
		slog.Error("postgres initialization failed", "err", err)
		os.Exit(1)
	}
	defer platformClient.Close()

	executionPolicy, err := platformClient.LoadExecutionPolicy(context.Background())
	if err != nil {
		slog.Error("execution policy load failed", "err", err)
		os.Exit(1)
	}
	billingConfig, err := platformClient.LoadBillingConfig(context.Background())
	if err != nil {
		slog.Error("billing config load failed", "err", err)
		os.Exit(1)
	}

	// Reach the host agent: open the SSH tunnel (host/user/key in ~/.ssh/config
	// under the SSH_COMMAND alias) and target its local end. With no SSH_COMMAND,
	// talk to AgentAddr directly (control plane co-located with the agent).
	agentBase := "http://" + cfg.AgentAddr
	if cfg.SSHCommand != "" {
		remotePort := "9876"
		if _, p, ok := strings.Cut(cfg.AgentAddr, ":"); ok {
			remotePort = p
		}
		if err := agent.StartTunnel(context.Background(), cfg.SSHCommand, cfg.AgentLocalPort, remotePort); err != nil {
			slog.Error("agent tunnel failed", "err", err)
			os.Exit(1)
		}
		agentBase = "http://127.0.0.1:" + cfg.AgentLocalPort
	}
	agentClient := agent.NewClient(agentBase, cfg.WorkerToken)
	if err := agentClient.Health(context.Background()); err != nil {
		slog.Warn("agent not healthy at startup; create/run will fail until it is", "base", agentBase, "err", err)
	} else {
		slog.Info("agent reachable", "base", agentBase)
	}

	// Postgres is the source of truth for sandbox state; execution dispatches to
	// the agent over the tunnel.
	svc := controlplane.NewService(platformClient, agentClient)

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/session", handler.SessionHandler(svc, platformClient))
	http.HandleFunc("/session/", handler.SessionHandler(svc, platformClient))

	port := ":" + cfg.Port
	slog.Info("control plane is running", "port", port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	chain := middleware.Logging(middleware.Auth(platformClient, executionPolicy, billingConfig)(http.DefaultServeMux))

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
	slog.Info("shutting down control plane")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("http server shutdown error", "err", err)
	}
	slog.Info("shutdown complete")
}
