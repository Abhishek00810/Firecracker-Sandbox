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
		DatabaseURL:    strings.TrimSpace(os.Getenv("DATABASE_URL")),
		Port:           defaultString(strings.TrimSpace(os.Getenv("PORT")), "8080"),
		LogLevel:       defaultString(strings.TrimSpace(os.Getenv("LOG_LEVEL")), "info"),
		LogFormat:      defaultString(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "json"),
		SSHCommand:     strings.TrimSpace(os.Getenv("SSH_COMMAND")),
		AgentLocalPort: defaultString(strings.TrimSpace(os.Getenv("AGENT_LOCAL_PORT")), "19876"),
		AgentAddr:      defaultString(strings.TrimSpace(os.Getenv("AGENT_ADDR")), "127.0.0.1:9876"),
		WorkerToken:    strings.TrimSpace(os.Getenv("WORKER_TOKEN")),
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}

	return cfg, nil
}
