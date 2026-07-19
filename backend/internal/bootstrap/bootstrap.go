// Package bootstrap makes a fresh KVM host self-sufficient for the worker agent.
// The control plane stays dumb: it only ships the binary (+ asset bundle) and
// starts the agent. Everything needed to actually run VMs — the ROOT_DIRECTORY
// layout, the pushed assets, and the host network provisioning that server.sh
// used to do out-of-band — the agent does to itself here, idempotently, on
// startup, before it serves. Each step is "verify, and only create if missing".
//
// This file is Piece 1: the directory layout. Asset unpacking and host
// provisioning land in later steps but hang off the same EnsureLayout root.
package bootstrap

import (
	"fmt"
	"os"
	"path/filepath"
)

// Layout is the standard directory tree the agent owns under ROOT_DIRECTORY,
// mirroring plan.md:
//
//	$ROOT/
//	├── bin/         # the pushed agent binary
//	├── assets/      # kernel/, rootfs/, initramfs (pushed bundle, Piece 3)
//	├── sockets/     # Firecracker API + vsock UDS
//	├── snapshots/   # VM snapshot templates + pause state
//	└── logs/
var Layout = []string{
	"bin",
	filepath.Join("assets", "kernel"),
	filepath.Join("assets", "rootfs"),
	"sockets",
	"snapshots",
	"logs",
}

// EnsureLayout creates the ROOT_DIRECTORY tree if absent. Idempotent — existing
// directories are left untouched. root must already be absolute (config's
// resolveRoot expands ~ and makes it absolute before calling this).
func EnsureLayout(root string) error {
	if root == "" {
		return fmt.Errorf("bootstrap: empty root directory")
	}
	for _, sub := range Layout {
		dir := filepath.Join(root, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("bootstrap: create %s: %w", dir, err)
		}
	}
	return nil
}
