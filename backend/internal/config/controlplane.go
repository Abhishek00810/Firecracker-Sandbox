package config

import (
	"errors"
	"os"
	"strings"
)

// LoadControlPlane loads only what the control plane needs: Postgres, auth,
// and the listen port. No Firecracker assets, no KVM/cgroup host validation —
// the control plane runs on any OS; VM requirements move to the host agents.
func LoadControlPlane() (*Config, error) {
	cfg := &Config{
		DatabaseURL:       strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SupabaseURL:       strings.TrimSpace(os.Getenv("SUPABASE_URL")),
		SupabaseJWTSecret: defaultString(strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET")), strings.TrimSpace(os.Getenv("SUPABASE_JWT_KEY"))),
		Port:              defaultString(strings.TrimSpace(os.Getenv("PORT")), "8080"),
		LogLevel:          defaultString(strings.TrimSpace(os.Getenv("LOG_LEVEL")), "info"),
		LogFormat:         defaultString(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "json"),
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}

	return cfg, nil
}
