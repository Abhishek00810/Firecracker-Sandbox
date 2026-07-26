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
	t.Setenv("ORCHESTRATOR_CPU_OVERCOMMIT_RATIO", "2")
	t.Setenv("ORCHESTRATOR_MEMORY_OVERCOMMIT_RATIO", "1.25")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8090" ||
		cfg.Token != "secret" ||
		cfg.WorkerToken != "worker-secret" ||
		cfg.HeartbeatTTL != 45*time.Second ||
		cfg.CPUOvercommitRatio != 2 ||
		cfg.MemoryOvercommitRatio != 1.25 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidOvercommitRatio(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgresql://postgres/renderops")
	t.Setenv("ORCHESTRATOR_TOKEN", "secret")
	t.Setenv("WORKER_TOKEN", "worker-secret")
	t.Setenv("ORCHESTRATOR_CPU_OVERCOMMIT_RATIO", "0.5")

	if _, err := Load(); err == nil {
		t.Fatal("expected invalid CPU overcommit ratio error")
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
