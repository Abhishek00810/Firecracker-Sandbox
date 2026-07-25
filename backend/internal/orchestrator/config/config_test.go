package config

import (
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	t.Setenv("DATABASE_URL", " postgresql://renderops:secret@postgres:5432/renderops ")
	t.Setenv("ORCHESTRATOR_TOKEN", " secret ")
	t.Setenv("WORKER_TOKEN", " worker-secret ")
	t.Setenv("ORCHESTRATOR_PORT", "")
	t.Setenv("ORCHESTRATOR_HEARTBEAT_TTL_SECONDS", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8090" || cfg.Token != "secret" || cfg.WorkerToken != "worker-secret" || cfg.HeartbeatTTL != 45*time.Second {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRequiresSecrets(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("ORCHESTRATOR_TOKEN", "")
	t.Setenv("WORKER_TOKEN", "")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing database URL error")
	}

	t.Setenv("DATABASE_URL", "postgresql://postgres/renderops")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing token error")
	}

	t.Setenv("ORCHESTRATOR_TOKEN", "secret")
	if _, err := Load(); err == nil {
		t.Fatal("expected missing worker token error")
	}
}
