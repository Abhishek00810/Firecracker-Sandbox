package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"backend/internal/middleware"
	"backend/internal/orchestrator"
	"backend/internal/preview"
	"backend/internal/previewgateway"
)

func main() {
	port := value("PORT", "8082")
	domain := strings.TrimSpace(os.Getenv("PREVIEW_DOMAIN"))
	orchestratorURL := strings.TrimSpace(os.Getenv("ORCHESTRATOR_URL"))
	orchestratorToken := strings.TrimSpace(os.Getenv("ORCHESTRATOR_TOKEN"))
	workerToken := strings.TrimSpace(os.Getenv("WORKER_TOKEN"))
	if domain == "" || orchestratorURL == "" || orchestratorToken == "" || workerToken == "" {
		slog.Error("PREVIEW_DOMAIN, ORCHESTRATOR_URL, ORCHESTRATOR_TOKEN, and WORKER_TOKEN are required")
		os.Exit(1)
	}
	signer, err := preview.NewSigner(preview.DeriveSigningSecret(workerToken))
	if err != nil {
		slog.Error("preview signer initialization failed", "err", err)
		os.Exit(1)
	}
	gateway, err := previewgateway.New(domain, signer, orchestrator.NewClient(orchestratorURL, orchestratorToken), workerToken)
	if err != nil {
		slog.Error("preview gateway initialization failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","role":"preview-gateway"}`))
	})
	mux.Handle("/", gateway.Handler())
	server := &http.Server{
		Addr: ":" + port, Handler: middleware.Logging(mux), ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout: 120 * time.Second, MaxHeaderBytes: 1 << 20,
	}
	go func() {
		slog.Info("preview gateway is running", "port", port, "domain", domain)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("preview gateway stopped", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func value(name, fallback string) string {
	if result := strings.TrimSpace(os.Getenv(name)); result != "" {
		return result
	}
	return fallback
}
