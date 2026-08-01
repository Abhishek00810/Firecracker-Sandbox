package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", " postgresql://renderops:secret@postgres:5432/renderops ")
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("ORCHESTRATOR_URL", " http://orchestrator.internal:8090/ ")
	t.Setenv("ORCHESTRATOR_TOKEN", " orchestration-secret ")
	t.Setenv("WORKER_TOKEN", " worker-secret ")
	t.Setenv("TERMINAL_ALLOWED_ORIGINS", " dev.renderops.com, localhost:5173 ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.DatabaseURL != "postgresql://renderops:secret@postgres:5432/renderops" {
		t.Fatalf("unexpected database URL %q", cfg.DatabaseURL)
	}
	if cfg.Port != "8080" {
		t.Fatalf("expected default port 8080, got %q", cfg.Port)
	}
	if cfg.LogLevel != "debug" || cfg.LogFormat != "text" {
		t.Fatalf("unexpected logging config: level=%q format=%q", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.OrchestratorURL != "http://orchestrator.internal:8090" {
		t.Fatalf("unexpected orchestrator URL %q", cfg.OrchestratorURL)
	}
	if cfg.OrchestratorToken != "orchestration-secret" {
		t.Fatal("orchestrator token was not loaded")
	}
	if cfg.WorkerToken != "worker-secret" {
		t.Fatal("worker token was not loaded")
	}
	if len(cfg.TerminalAllowedOrigins) != 2 || cfg.TerminalAllowedOrigins[0] != "dev.renderops.com" {
		t.Fatalf("unexpected terminal origins: %v", cfg.TerminalAllowedOrigins)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ORCHESTRATOR_URL", "http://orchestrator.internal:8090")
	t.Setenv("ORCHESTRATOR_TOKEN", "secret")
	t.Setenv("WORKER_TOKEN", "worker-secret")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded without DATABASE_URL")
	}
}
