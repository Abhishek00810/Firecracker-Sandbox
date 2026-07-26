package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL           string
	Port                  string
	Token                 string
	WorkerToken           string
	HeartbeatTTL          time.Duration
	CPUOvercommitRatio    float64
	MemoryOvercommitRatio float64
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL: strings.TrimSpace(os.Getenv("DATABASE_URL")),
		Port:        defaultString(strings.TrimSpace(os.Getenv("ORCHESTRATOR_PORT")), "8090"),
		Token:       strings.TrimSpace(os.Getenv("ORCHESTRATOR_TOKEN")),
		WorkerToken: strings.TrimSpace(os.Getenv("WORKER_TOKEN")),
	}
	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}
	if cfg.Token == "" {
		return nil, errors.New("ORCHESTRATOR_TOKEN must be set")
	}
	if cfg.WorkerToken == "" {
		return nil, errors.New("WORKER_TOKEN must be set")
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

	cpuOvercommitRatio, err := overcommitRatio("ORCHESTRATOR_CPU_OVERCOMMIT_RATIO")
	if err != nil {
		return nil, err
	}
	cfg.CPUOvercommitRatio = cpuOvercommitRatio

	memoryOvercommitRatio, err := overcommitRatio("ORCHESTRATOR_MEMORY_OVERCOMMIT_RATIO")
	if err != nil {
		return nil, err
	}
	cfg.MemoryOvercommitRatio = memoryOvercommitRatio
	return cfg, nil
}

func overcommitRatio(name string) (float64, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return 1, nil
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 1 {
		return 0, fmt.Errorf("%s must be a finite number greater than or equal to 1", name)
	}
	return value, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
