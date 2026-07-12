package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type Config struct {
	DatabaseURL        string
	SupabaseURL        string
	SupabaseJWTSecret  string // legacy HS256 JWT secret (optional; ES256 uses JWKS)
	AssetsPath         string
	KernelPath         string
	RootfsPath         string
	InitrdPath         string
	FirecrackerBinary  string
	SocketDir          string
	SnapshotDir        string
	Port               string
	LogLevel           string
	LogFormat          string
	HostValidationMode string
	FCRunUID           int // uid Firecracker VMMs drop to via setpriv; 0 = run as root (disabled)
	FCRunGID           int // gid Firecracker VMMs drop to via setpriv; 0 = run as root (disabled)
	Warnings           []string
}

func Load() (*Config, error) {
	cfg := &Config{
		DatabaseURL:        strings.TrimSpace(os.Getenv("DATABASE_URL")),
		SupabaseURL:        strings.TrimSpace(os.Getenv("SUPABASE_URL")),
		SupabaseJWTSecret:  defaultString(strings.TrimSpace(os.Getenv("SUPABASE_JWT_SECRET")), strings.TrimSpace(os.Getenv("SUPABASE_JWT_KEY"))),
		Port:               defaultString(strings.TrimSpace(os.Getenv("PORT")), "8080"),
		LogLevel:           defaultString(strings.TrimSpace(os.Getenv("LOG_LEVEL")), "info"),
		LogFormat:          defaultString(strings.TrimSpace(os.Getenv("LOG_FORMAT")), "json"),
		HostValidationMode: defaultString(strings.TrimSpace(os.Getenv("HOST_VALIDATION_MODE")), "strict"),
	}

	if cfg.DatabaseURL == "" {
		return nil, errors.New("DATABASE_URL must be set")
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get working directory: %w", err)
	}

	cfg.AssetsPath, err = resolvePath(
		strings.TrimSpace(os.Getenv("ASSETS_PATH")),
		[]string{
			"/app/assets",
			filepath.Join(cwd, "assets"),
			filepath.Join(cwd, "..", "assets"),
		},
		true,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve assets path: %w", err)
	}

	cfg.FirecrackerBinary, err = resolvePath(
		strings.TrimSpace(os.Getenv("FIRECRACKER_BINARY")),
		[]string{
			"/app/firecracker/firecracker-v1.7.0-aarch64",
			filepath.Join(cwd, "release-v1.7.0-aarch64", "firecracker-v1.7.0-aarch64"),
			filepath.Join(cwd, "..", "release-v1.7.0-aarch64", "firecracker-v1.7.0-aarch64"),
		},
		false,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve firecracker binary: %w", err)
	}

	cfg.KernelPath = filepath.Join(cfg.AssetsPath, "kernel", "vmlinux")
	cfg.RootfsPath = filepath.Join(cfg.AssetsPath, "rootfs", "rootfs-alpine.ext4")
	cfg.InitrdPath = filepath.Join(cfg.AssetsPath, "initramfs.cpio.gz")

	if err := requireFile(cfg.KernelPath); err != nil {
		return nil, fmt.Errorf("kernel asset invalid: %w", err)
	}
	if err := requireFile(cfg.RootfsPath); err != nil {
		return nil, fmt.Errorf("rootfs asset invalid: %w", err)
	}
	if err := requireFile(cfg.InitrdPath); err != nil {
		return nil, fmt.Errorf("initramfs asset invalid: %w", err)
	}
	if err := requireExecutable(cfg.FirecrackerBinary); err != nil {
		return nil, fmt.Errorf("firecracker binary invalid: %w", err)
	}

	cfg.SocketDir = defaultString(strings.TrimSpace(os.Getenv("SOCKET_DIR")), filepath.Join(os.TempDir(), "fc-sockets"))
	cfg.SocketDir, err = filepath.Abs(cfg.SocketDir)
	if err != nil {
		return nil, fmt.Errorf("resolve socket dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SocketDir, 0755); err != nil {
		return nil, fmt.Errorf("create socket dir %q: %w", cfg.SocketDir, err)
	}

	cfg.SnapshotDir = defaultString(strings.TrimSpace(os.Getenv("SNAPSHOT_DIR")), "/dev/shm/fc-snapshots")
	cfg.SnapshotDir, err = filepath.Abs(cfg.SnapshotDir)
	if err != nil {
		return nil, fmt.Errorf("resolve snapshot dir: %w", err)
	}
	if err := os.MkdirAll(cfg.SnapshotDir, 0755); err != nil {
		return nil, fmt.Errorf("create snapshot dir %q: %w", cfg.SnapshotDir, err)
	}

	cfg.FCRunUID = intEnv("FC_RUN_UID", 0)
	cfg.FCRunGID = intEnv("FC_RUN_GID", 0)

	warnings, err := validateHost(cfg.HostValidationMode)
	if err != nil {
		return nil, err
	}
	cfg.Warnings = warnings

	return cfg, nil
}

func defaultString(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// intEnv reads an integer environment variable, returning fallback if unset or invalid.
func intEnv(key string, fallback int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func resolvePath(explicit string, candidates []string, wantDir bool) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return abs, nil
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if wantDir && info.IsDir() {
			return filepath.Abs(candidate)
		}
		if !wantDir && !info.IsDir() {
			return filepath.Abs(candidate)
		}
	}

	kind := "file"
	if wantDir {
		kind = "directory"
	}
	return "", fmt.Errorf("no %s found; set the relevant environment variable explicitly", kind)
}

func requireFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", path)
	}
	return nil
}

func requireExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%q is a directory", path)
	}
	if info.Mode()&0111 == 0 {
		return fmt.Errorf("%q is not executable", path)
	}
	return nil
}

func validateHost(mode string) ([]string, error) {
	var warnings []string

	if runtime.GOOS != "linux" {
		msg := fmt.Sprintf("host OS %q is unsupported for Firecracker; run the API on Linux", runtime.GOOS)
		if mode == "warn" {
			return []string{msg}, nil
		}
		return nil, errors.New(msg)
	}

	if _, err := os.Stat("/dev/kvm"); err != nil {
		msg := "missing /dev/kvm; Firecracker requires KVM on the host"
		if mode == "warn" {
			warnings = append(warnings, msg)
		} else {
			return nil, errors.New(msg)
		}
	}

	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		warnings = append(warnings, "cgroup v2 controller file not found; CPU and memory limits may not be enforced")
	}

	return warnings, nil
}
