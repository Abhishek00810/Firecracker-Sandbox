// template-builder is a one-shot process that builds the platform's standard
// Firecracker templates (nano/small/medium, with numpy baked in) plus a bare
// "micro" benchmark template, and publishes them as immutable, versioned releases
// to durable object storage. Runtime workers never build templates — they only
// download and restore what this builds. It runs on a KVM host (it boots real
// Firecracker VMs) and is triggered by an operator or CI, not by an API call.
package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"backend/internal/template"
	"backend/internal/workerplane/host"
)

func setupLogger() {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if os.Getenv("LOG_FORMAT") == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	slog.SetDefault(slog.New(h))
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func main() {
	setupLogger()

	h, err := host.Init()
	if err != nil {
		slog.Error("template-builder host init failed", "err", err)
		os.Exit(1)
	}
	cfg := h.Config
	mgr := h.VMManager
	ctx := context.Background()

	// Artifact store: local directory for now (an Azure Blob / R2 store slots in
	// behind template.ArtifactStore later, reading credentials from the environment).
	storeDir := env("TEMPLATE_STORE_DIR", filepath.Join(cfg.RootDirectory, "template-store"))
	snapRoot := env("TEMPLATE_BUILD_DIR", filepath.Join(cfg.SnapshotDir, "build"))
	store, err := template.NewLocalStore(storeDir)
	if err != nil {
		slog.Error("open local artifact store", "dir", storeDir, "err", err)
		os.Exit(1)
	}
	repo := template.NewStoreManifestRepository(store)

	f, err := gatherFacts(cfg)
	if err != nil {
		slog.Error("gather host facts", "err", err)
		os.Exit(1)
	}
	slog.Info("build host facts",
		"arch", f.arch, "cpu_compat_class", f.cpuCompatClass,
		"firecracker_version", f.firecrackerVersion,
		"rootfs_digest", short(f.rootfsDigest), "kernel_digest", short(f.kernelDigest))

	stamp := time.Now().UTC().Format("20060102-150405")
	npmProvision := []string{"pip3 install --no-cache-dir numpy"}

	// ---- standard release: nano/small/medium with numpy, activated for serving ----
	if envBool("BUILD_STANDARD", true) {
		var variants []template.BuiltVariant
		for _, s := range standardSpecs(npmProvision) {
			slog.Info("building standard variant", "size", s.Name, "vcpus", s.VCPUs, "memory_mb", s.MemoryMB, "disk_mb", s.DiskMB)
			bv, err := buildAndValidate(ctx, mgr, cfg, s, filepath.Join(snapRoot, "standard"))
			if err != nil {
				slog.Error("standard variant build failed", "size", s.Name, "err", err)
				os.Exit(1)
			}
			variants = append(variants, bv)
			time.Sleep(2 * time.Second) // let the previous build VM's kernel-side teardown settle
		}
		releaseID := "standard-" + stamp
		m, err := template.PublishRelease(ctx, store, repo, releaseInputs(releaseID, f, variants))
		if err != nil {
			slog.Error("publish standard release", "err", err)
			os.Exit(1)
		}
		if err := repo.Activate(ctx, releaseID); err != nil {
			slog.Error("activate standard release", "release_id", releaseID, "err", err)
			os.Exit(1)
		}
		slog.Info("standard release published + ACTIVATED", "release_id", m.ReleaseID, "variants", len(m.Variants))
	}

	// ---- micro release: bare benchmark, published but NOT activated ----
	if envBool("BUILD_MICRO", true) {
		s := microSpec()
		slog.Info("building micro benchmark variant", "vcpus", s.VCPUs, "memory_mb", s.MemoryMB, "disk_mb", s.DiskMB)
		bv, err := buildAndValidate(ctx, mgr, cfg, s, filepath.Join(snapRoot, "micro"))
		if err != nil {
			slog.Error("micro variant build failed", "err", err)
			os.Exit(1)
		}
		releaseID := "micro-" + stamp
		m, err := template.PublishRelease(ctx, store, repo, releaseInputs(releaseID, f, []template.BuiltVariant{bv}))
		if err != nil {
			slog.Error("publish micro release", "err", err)
			os.Exit(1)
		}
		slog.Info("micro benchmark release published (not activated)", "release_id", m.ReleaseID)
	}

	slog.Info("template builder finished", "store", storeDir)
}

func releaseInputs(releaseID string, f facts, variants []template.BuiltVariant) template.ReleaseInputs {
	return template.ReleaseInputs{
		ReleaseID:          releaseID,
		CreatedAt:          time.Now().UTC(),
		Arch:               f.arch,
		CPUCompatClass:     f.cpuCompatClass,
		FirecrackerVersion: f.firecrackerVersion,
		AssetBundleDigest:  f.assetBundleDigest,
		RootfsDigest:       f.rootfsDigest,
		KernelDigest:       f.kernelDigest,
		Variants:           variants,
	}
}

func short(digest string) string {
	if len(digest) > 12 {
		return digest[:12]
	}
	return digest
}
