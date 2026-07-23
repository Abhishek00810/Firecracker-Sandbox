package config

import (
	"errors"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL string
	Port        string
	LogLevel    string
	LogFormat   string

	SSHCommand     string
	AgentLocalPort string
	AgentAddr      string
	WorkerToken    string
}

// Load reads only control-plane settings. It performs no worker host or asset
// validation, so the control plane remains independent of Firecracker.
func Load() (*Config, error) {
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

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
