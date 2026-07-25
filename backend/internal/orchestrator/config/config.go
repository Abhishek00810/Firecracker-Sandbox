package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL  string
	Port         string
	Token        string
	HeartbeatTTL time.Duration
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		Port:        defaultString(strings.TrimSpace(os.Getenv("ORCHESTRATOR_PORT")), "8090"),
		Token:       strings.TrimSpace(os.Getenv("ORCHESTRATOR_TOKEN")),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}
	if cfg.Token == "" {
		return nil, errors.New("ORCHESTRATOR_TOKEN must be set")
	}
	ttlSeconds := 30
	if raw := strings.TrimSpace(os.Getenv("ORCHESTRATOR_HEARTBEAT_TTL_SECONDS")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("ORCHESTRATOR_HEARTBEAT_TTL_SECONDS must be a positive integer")
		}
		ttlSeconds = value
	}
	cfg.HeartbeatTTL = time.Duration(ttlSeconds) * time.Second
	return cfg, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
