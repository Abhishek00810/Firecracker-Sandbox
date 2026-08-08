package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadResolvesPathsAndDirs(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host validation in worker config requires Linux")
	}

	tmp := t.TempDir()
	assetsDir := filepath.Join(tmp, "assets")
	kernelDir := filepath.Join(assetsDir, "kernel")
	rootfsDir := filepath.Join(assetsDir, "rootfs")
	if err := os.MkdirAll(kernelDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootfsDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(kernelDir, "vmlinux"), "kernel", 0644)
	writeFile(t, filepath.Join(rootfsDir, "rootfs-alpine.ext4"), "rootfs", 0644)
	writeFile(t, filepath.Join(assetsDir, "initramfs.cpio.gz"), "initrd", 0644)

	firecrackerBinary := filepath.Join(tmp, "firecracker")
	writeFile(t, firecrackerBinary, "#!/bin/sh\nexit 0\n", 0755)

	socketDir := filepath.Join(tmp, "sockets")
	activeDiskDir := filepath.Join(tmp, "active-disks")
	snapshotDir := filepath.Join(tmp, "snapshots")

	t.Setenv("ROOT_DIRECTORY", tmp)
	t.Setenv("ASSETS_PATH", assetsDir)
	t.Setenv("FIRECRACKER_BINARY", firecrackerBinary)
	t.Setenv("SOCKET_DIR", socketDir)
	t.Setenv("ACTIVE_DISK_BACKEND", "filesystem")
	t.Setenv("ACTIVE_DISK_DIR", activeDiskDir)
	t.Setenv("ACTIVE_DISK_CLONE_MODE", "required")
	t.Setenv("SNAPSHOT_DIR", snapshotDir)
	t.Setenv("HOST_VALIDATION_MODE", "warn")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.RootDirectory != tmp {
		t.Fatalf("expected root directory %q, got %q", tmp, cfg.RootDirectory)
	}
	if cfg.AssetsPath != assetsDir {
		t.Fatalf("expected assets path %q, got %q", assetsDir, cfg.AssetsPath)
	}
	if cfg.FirecrackerBinary != firecrackerBinary {
		t.Fatalf("expected firecracker binary %q, got %q", firecrackerBinary, cfg.FirecrackerBinary)
	}
	if _, err := os.Stat(socketDir); err != nil {
		t.Fatalf("socket dir not created: %v", err)
	}
	if _, err := os.Stat(snapshotDir); err != nil {
		t.Fatalf("snapshot dir not created: %v", err)
	}
	if cfg.ActiveDiskDir != activeDiskDir {
		t.Fatalf("expected active disk dir %q, got %q", activeDiskDir, cfg.ActiveDiskDir)
	}
	if cfg.ActiveDiskBackend != "filesystem" {
		t.Fatalf("expected filesystem backend, got %q", cfg.ActiveDiskBackend)
	}
	if cfg.ActiveDiskCloneMode != "required" {
		t.Fatalf("expected required clone mode, got %q", cfg.ActiveDiskCloneMode)
	}
	if _, err := os.Stat(activeDiskDir); err != nil {
		t.Fatalf("active disk dir not created: %v", err)
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}
