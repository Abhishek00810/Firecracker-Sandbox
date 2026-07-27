// Package host prepares a machine to run Firecracker VMs and is shared by the
// worker (which serves sandboxes) and the template-builder (which builds
// templates). Both need the same host readiness: unpack + validate assets,
// provision the fcvm user and network slots, init cgroups, and construct the
// Firecracker manager. Extracting it here keeps the two binaries in lockstep
// instead of duplicating startup logic.
package host

import (
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"

	"backend/internal/bootstrap"
	"backend/internal/cgroup"
	"backend/internal/executor/firecracker"
	workerconfig "backend/internal/workerplane/config"
)

// Host is the result of Init: the resolved config plus the ready-to-use manager.
type Host struct {
	Config        *workerconfig.Config
	VMManager     *firecracker.FireCrackerManager
	SlotCount     int
	MaxProvisions int
}

// Init loads config and brings the host up to the point where VMs can be booted:
// EnsureAssets -> ValidateAssets -> Provision -> cgroup.Init -> NewFirecrackerManager.
// In HostValidationMode "warn" (dev on a non-KVM box) a failed bootstrap step is
// logged and skipped; in strict mode it returns an error. cgroup.Init failure is
// always non-fatal (limits simply won't be enforced), matching the worker today.
func Init() (*Host, error) {
	cfg, err := workerconfig.Load()
	if err != nil {
		return nil, fmt.Errorf("load worker config: %w", err)
	}
	for _, warning := range cfg.Warnings {
		slog.Warn("startup warning", "message", warning)
	}

	slotCount := envInt("SLOT_COUNT", 50)
	maxProvisions := envInt("MAX_CONCURRENT_PROVISIONS", runtime.NumCPU())

	// step honors warn-vs-strict host validation: warn logs and continues, strict fails.
	step := func(stage string, err error) error {
		if err == nil {
			return nil
		}
		if cfg.HostValidationMode == "warn" {
			slog.Warn("bootstrap step skipped", "stage", stage, "err", err)
			return nil
		}
		return fmt.Errorf("bootstrap %s: %w", stage, err)
	}

	if err := step("assets", bootstrap.EnsureAssets(cfg.RootDirectory)); err != nil {
		return nil, err
	}
	if err := step("assets-validate", cfg.ValidateAssets()); err != nil {
		return nil, err
	}
	if uid, gid, perr := bootstrap.Provision(bootstrap.ProvisionParams{
		Root:        cfg.RootDirectory,
		SlotCount:   slotCount,
		SocketDir:   cfg.SocketDir,
		SnapshotDir: cfg.SnapshotDir,
		AssetsDir:   cfg.AssetsPath,
	}); perr != nil {
		if err := step("provision", perr); err != nil {
			return nil, err
		}
	} else {
		cfg.FCRunUID, cfg.FCRunGID = uid, gid
		slog.Info("host provisioned", "fc_uid", uid, "fc_gid", gid, "slots", slotCount)
	}

	if err := cgroup.Init(); err != nil {
		slog.Warn("cgroup init failed, limits will not be enforced", "err", err)
	}

	vmManager := firecracker.NewFirecrackerManager(
		cfg.SocketDir, cfg.AssetsPath, cfg.FirecrackerBinary,
		slotCount, maxProvisions, cfg.FCRunUID, cfg.FCRunGID,
	)

	return &Host{
		Config:        cfg,
		VMManager:     vmManager,
		SlotCount:     slotCount,
		MaxProvisions: maxProvisions,
	}, nil
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}
