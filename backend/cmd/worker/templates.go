package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"backend/internal/executor/firecracker"
	"backend/internal/template"
	"backend/internal/vmsize"
	workerconfig "backend/internal/workerplane/config"
)

// env reads a trimmed string env var with a default.
func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// resolveTemplates returns the per-size snapshot templates keyed by vmsize.Key,
// either by DOWNLOADING a prebuilt release from object storage (TEMPLATE_SOURCE=
// prebuilt — the decoupled, restore-only path) or by BUILDING them locally at
// startup (the default/legacy path).
func resolveTemplates(ctx context.Context, cfg *workerconfig.Config, vmManager *firecracker.FireCrackerManager, baseCfg firecracker.VMConfig) (map[string]*firecracker.SnapshotTemplate, error) {
	if env("TEMPLATE_SOURCE", "build") == "prebuilt" {
		return loadPrebuiltTemplates(ctx, cfg)
	}
	return buildTemplates(vmManager, cfg, baseCfg), nil
}

// buildTemplates is the legacy path: build one snapshot template per size at
// startup. Unchanged behavior — extracted so the prebuilt path can replace it.
func buildTemplates(vmManager *firecracker.FireCrackerManager, cfg *workerconfig.Config, baseCfg firecracker.VMConfig) map[string]*firecracker.SnapshotTemplate {
	out := make(map[string]*firecracker.SnapshotTemplate, len(vmsize.Sizes))

	var deflt *firecracker.SnapshotTemplate
	if tmpl, err := createTemplateWithRetry(vmManager, baseCfg, cfg.SnapshotDir, 3); err != nil {
		slog.Warn("snapshot template creation failed, falling back to cold boot", "err", err)
	} else {
		deflt = tmpl
		slog.Info("snapshot template ready", "snap", tmpl.SnapPath)
	}

	for _, sz := range vmsize.Sizes {
		if sz.IsDefault() {
			out[sz.Key()] = deflt
			continue
		}
		szCfg := baseCfg
		szCfg.VCPUCount = sz.VCPUs
		szCfg.MemSizeMiB = sz.MemoryMB
		szCfg.DiskGB = sz.DiskGB
		snapSubDir := filepath.Join(cfg.SnapshotDir, sz.Name)
		if err := os.MkdirAll(snapSubDir, 0o755); err != nil {
			slog.Warn("could not create size snapshot dir", "size", sz.Name, "err", err)
		} else if cfg.FCRunUID > 0 {
			os.Chown(snapSubDir, cfg.FCRunUID, cfg.FCRunGID)
		}
		if tmpl, err := createTemplateWithRetry(vmManager, szCfg, snapSubDir, 3); err != nil {
			slog.Warn("size template creation failed, falling back to cold boot", "size", sz.Name, "err", err)
			out[sz.Key()] = nil
		} else {
			out[sz.Key()] = tmpl
			slog.Info("size template ready", "size", sz.Name)
		}
	}
	return out
}

// loadPrebuiltTemplates downloads the configured release from object storage,
// verifies host compatibility, caches its variants on NVMe, and returns per-size
// snapshot templates — no VMs are built. Which release is the default comes from
// config (DEFAULT_TEMPLATE_RELEASE, else the store's active pointer), never a
// hardcoded name.
func loadPrebuiltTemplates(ctx context.Context, cfg *workerconfig.Config) (map[string]*firecracker.SnapshotTemplate, error) {
	baseStore, err := blobStoreFromEnv()
	if err != nil {
		return nil, err
	}
	repo := template.NewStoreManifestRepository(baseStore)
	artifactStore := template.NewCompressedStore(baseStore)

	cacheDir := env("TEMPLATE_CACHE_DIR", filepath.Join(cfg.RootDirectory, "template-cache"))
	cache, err := template.NewDiskCache(cacheDir, artifactStore, cfg.FCRunUID, cfg.FCRunGID)
	if err != nil {
		return nil, err
	}

	// The default release is EXTERNAL config, not a hardcoded template name.
	releaseID := env("DEFAULT_TEMPLATE_RELEASE", "")
	if releaseID == "" {
		ptr, ok, aerr := repo.Active(ctx)
		if aerr != nil {
			return nil, fmt.Errorf("resolve active release: %w", aerr)
		}
		if !ok {
			return nil, fmt.Errorf("no DEFAULT_TEMPLATE_RELEASE set and no active release in the store")
		}
		releaseID = ptr.ReleaseID
	}

	m, err := repo.Get(ctx, releaseID)
	if err != nil {
		return nil, fmt.Errorf("get manifest %s: %w", releaseID, err)
	}

	facts, err := template.GatherHostFacts(cfg.RootfsPath, cfg.KernelPath, cfg.FirecrackerBinary, cfg.AssetsPath)
	if err != nil {
		return nil, fmt.Errorf("gather host facts: %w", err)
	}
	if err := template.NewCompatibilityValidator().Compatible(m, facts); err != nil {
		return nil, err
	}

	out := make(map[string]*firecracker.SnapshotTemplate, len(m.Variants))
	for size, v := range m.Variants {
		cv, cerr := cache.Ensure(ctx, m, size)
		if cerr != nil {
			return nil, fmt.Errorf("cache variant %s: %w", size, cerr)
		}
		out[vmsize.Key(v.VCPUs, v.MemoryMB, v.DiskMB/1024)] = &firecracker.SnapshotTemplate{
			SnapPath:         cv.SnapshotPath,
			MemPath:          cv.MemoryPath,
			RootfsPath:       cfg.RootfsPath, // local asset rootfs, digest-pinned + validated above
			WritableDiskPath: cv.WritableSeedPath,
			VsockPath:        v.VsockPath,
			TapName:          v.TapName,
		}
		slog.Info("prebuilt template cached", "release", releaseID, "size", size)
	}
	return out, nil
}

// blobStoreFromEnv builds the Azure Blob artifact store from BLOB_* env vars.
//
// NOTE: this uses the account KEY today. A runtime worker should use a READ-ONLY
// SAS instead (workers run untrusted code and shouldn't hold write creds) — a
// tracked follow-up requiring SAS support in AzureBlobStore.
func blobStoreFromEnv() (template.ArtifactStore, error) {
	acct := env("BLOB_STORAGE_ACCOUNT", "")
	if acct == "" {
		return nil, fmt.Errorf("TEMPLATE_SOURCE=prebuilt requires BLOB_STORAGE_ACCOUNT")
	}
	return template.NewAzureBlobStore(acct, env("BLOB_SECRET_KEY", ""), env("BLOB_CONTAINER_NAME", ""))
}
