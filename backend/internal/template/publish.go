package template

import (
	"context"
	"fmt"
	"os"
	"time"
)

// BuiltVariant is the local output of building one size before it is published:
// the on-disk snapshot, memory and golden-writable-seed files the builder just
// produced (e.g. via Firecracker CreateTemplate).
type BuiltVariant struct {
	Size             string
	VCPUs            int
	MemoryMB         int
	DiskMB           int
	SnapshotPath     string // local path to the .snap
	MemoryPath       string // local path to the .mem
	WritableSeedPath string // local path to the golden writable-seed .ext4
}

// ReleaseInputs is everything needed to publish one standard-template release:
// its identity, the compatibility pins (measured from the build host), and the
// built variants.
type ReleaseInputs struct {
	ReleaseID          string
	CreatedAt          time.Time
	Arch               string
	CPUCompatClass     string
	FirecrackerVersion string
	AssetBundleDigest  string
	RootfsDigest       string
	KernelDigest       string
	Variants           []BuiltVariant
}

// PublishRelease checksums and uploads every variant's artifacts, then writes the
// immutable release manifest. It deliberately does NOT activate the release — the
// caller activates last (repo.Activate), AFTER any post-build validation, so a
// failure between publish and activate leaves the release published-but-inactive
// (invisible to workers) rather than half-live.
func PublishRelease(ctx context.Context, store ArtifactStore, repo ManifestRepository, in ReleaseInputs) (Manifest, error) {
	if in.ReleaseID == "" {
		return Manifest{}, fmt.Errorf("release id is required")
	}
	if len(in.Variants) == 0 {
		return Manifest{}, fmt.Errorf("release %s has no variants to publish", in.ReleaseID)
	}
	createdAt := in.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	variants := make(map[string]Variant, len(in.Variants))
	for _, bv := range in.Variants {
		if _, dup := variants[bv.Size]; dup {
			return Manifest{}, fmt.Errorf("duplicate variant size %q in release %s", bv.Size, in.ReleaseID)
		}
		snap, err := uploadArtifact(ctx, store, in.ReleaseID, bv.Size, ArtifactSnapshot, bv.SnapshotPath)
		if err != nil {
			return Manifest{}, err
		}
		mem, err := uploadArtifact(ctx, store, in.ReleaseID, bv.Size, ArtifactMemory, bv.MemoryPath)
		if err != nil {
			return Manifest{}, err
		}
		seed, err := uploadArtifact(ctx, store, in.ReleaseID, bv.Size, ArtifactWritableSeed, bv.WritableSeedPath)
		if err != nil {
			return Manifest{}, err
		}
		variants[bv.Size] = Variant{
			Size:         bv.Size,
			VCPUs:        bv.VCPUs,
			MemoryMB:     bv.MemoryMB,
			DiskMB:       bv.DiskMB,
			Snapshot:     snap,
			Memory:       mem,
			WritableSeed: seed,
		}
	}

	m := Manifest{
		SchemaVersion:      SchemaVersion,
		ReleaseID:          in.ReleaseID,
		CreatedAt:          createdAt,
		Arch:               in.Arch,
		CPUCompatClass:     in.CPUCompatClass,
		FirecrackerVersion: in.FirecrackerVersion,
		AssetBundleDigest:  in.AssetBundleDigest,
		RootfsDigest:       in.RootfsDigest,
		KernelDigest:       in.KernelDigest,
		Variants:           variants,
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("assembled manifest is invalid: %w", err)
	}
	if err := repo.Publish(ctx, m); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// uploadArtifact hashes a local file, uploads it under its release/size/name key,
// and returns the artifact record (name + checksum + size) for the manifest.
func uploadArtifact(ctx context.Context, store ArtifactStore, releaseID, size, name, path string) (Artifact, error) {
	if path == "" {
		return Artifact{}, fmt.Errorf("release %s variant %s: %s artifact path is empty", releaseID, size, name)
	}
	sum, err := SHA256File(path)
	if err != nil {
		return Artifact{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("stat %s: %w", path, err)
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	if err := store.Put(ctx, ArtifactKey(releaseID, size, name), f); err != nil {
		return Artifact{}, fmt.Errorf("upload %s variant %s %s: %w", releaseID, size, name, err)
	}
	return Artifact{Name: name, SHA256: sum, Bytes: info.Size()}, nil
}
