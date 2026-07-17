package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Port           string
	WorkerID       string
	WorkerURL      string
	WorkerToken    string
	DatabaseURL    string
	RequestTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Port: valueOrDefault("PORT", "8081"),
		// Single-server default: with one worker there's nothing to disambiguate,
		// so the worker id auto-defaults to "worker-1". Multi-worker deployments
		// set STATIC_WORKER_ID explicitly (and, later, workers self-register).
		WorkerID:       valueOrDefault("STATIC_WORKER_ID", "worker-1"),
		WorkerURL:      strings.TrimRight(strings.TrimSpace(os.Getenv("STATIC_WORKER_URL")), "/"),
		WorkerToken:    strings.TrimSpace(os.Getenv("WORKER_TOKEN")),
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		RequestTimeout: 35 * time.Second,
	}
	parsed, err := url.Parse(cfg.WorkerURL)
	if err != nil || parsed.Host == "" {
		return Config{}, fmt.Errorf("STATIC_WORKER_URL must be a valid absolute URL")
	}
	if cfg.WorkerToken == "" {
		return Config{}, fmt.Errorf("WORKER_TOKEN must be set")
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("DATABASE_URL must be set")
	}
	return cfg, nil
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
