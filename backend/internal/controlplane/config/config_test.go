package config

import "testing"

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", " postgresql://renderops:secret@postgres:5432/renderops ")
	t.Setenv("PORT", "")
	t.Setenv("LOG_LEVEL", "debug")
	t.Setenv("LOG_FORMAT", "text")
	t.Setenv("SSH_COMMAND", " ssh worker ")
	t.Setenv("AGENT_LOCAL_PORT", "")
	t.Setenv("AGENT_ADDR", " worker.internal:9876 ")
	t.Setenv("WORKER_TOKEN", " worker-secret ")

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
	if cfg.SSHCommand != "ssh worker" {
		t.Fatalf("unexpected SSH command %q", cfg.SSHCommand)
	}
	if cfg.AgentLocalPort != "19876" {
		t.Fatalf("expected default agent local port 19876, got %q", cfg.AgentLocalPort)
	}
	if cfg.AgentAddr != "worker.internal:9876" {
		t.Fatalf("unexpected agent address %q", cfg.AgentAddr)
	}
	if cfg.WorkerToken != "worker-secret" {
		t.Fatal("worker token was not loaded")
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded without DATABASE_URL")
	}
}
