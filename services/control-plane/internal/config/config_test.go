package config

import "testing"

func TestLoadRequiresStaticWorkerConfiguration(t *testing.T) {
	t.Setenv("STATIC_WORKER_ID", "")
	t.Setenv("STATIC_WORKER_URL", "")
	t.Setenv("WORKER_TOKEN", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted missing worker configuration")
	}
}

func TestLoadReturnsStaticWorkerConfiguration(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("STATIC_WORKER_ID", "worker-host-01")
	t.Setenv("STATIC_WORKER_URL", "https://10.0.1.20:9000/")
	t.Setenv("WORKER_TOKEN", "worker-secret")
	t.Setenv("DATABASE_URL", "postgres://user:pass@localhost:5432/db")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Port != "9090" || cfg.WorkerID != "worker-host-01" || cfg.WorkerURL != "https://10.0.1.20:9000" || cfg.WorkerToken != "worker-secret" || cfg.DatabaseURL != "postgres://user:pass@localhost:5432/db" {
		t.Fatalf("Load() = %#v", cfg)
	}
}
