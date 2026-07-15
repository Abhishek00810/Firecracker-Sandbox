// Control plane: auth + Postgres + the public REST API, with no local VMs.
// First step of the distributed split — /session lifecycle works end-to-end
// against the DB (so the SvelteKit dashboard is fully functional), while
// /execute and /session/:id/{run,exec} return a clear "no agent" error until
// remote host agents are implemented.
package main

import (
	"backend/internal/config"
	"backend/internal/controlplane"
	"backend/internal/handler"
	"backend/internal/metrics"
	"backend/internal/middleware"
	"backend/internal/platform"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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

// executeUnavailable replaces the one-shot /execute path until host agents
// can run jobs. 503 so SDKs treat it as retryable-later, not a client error.
func executeUnavailable(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	json.NewEncoder(w).Encode(handler.APIError{
		Status:    "error",
		Code:      "no_agent_available",
		Message:   "execution is not yet available on the control plane; host agents are not implemented",
		RequestID: middleware.RequestIDFromContext(r.Context()),
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

	// Same hook contract as the session manager in cmd/api: every lifecycle
	// transition is synced to the sandboxes table + timeline log.
	stateHook := func(sessionID, userID, state string) {
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

	svc := controlplane.NewService(stateHook)
	{
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := svc.Hydrate(ctx, platformClient); err != nil {
			slog.Warn("sandbox hydration from DB failed; existing sandboxes won't be controllable until recreated", "err", err)
		}
		cancel()
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/execute", executeUnavailable)
	http.HandleFunc("/session", handler.SessionHandler(svc, platformClient))
	http.HandleFunc("/session/", handler.SessionHandler(svc, platformClient))
	http.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(metrics.GetSnapshot())
	})

	port := ":" + cfg.Port
	slog.Info("control plane is running", "port", port)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	chain := middleware.Logging(middleware.Auth(platformClient, cfg.SupabaseURL, cfg.SupabaseJWTSecret, executionPolicy, billingConfig)(http.DefaultServeMux))

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
