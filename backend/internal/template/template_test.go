package template

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPublishActivateCacheAndRepair(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := NewLocalStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewStoreManifestRepository(store)
	artifacts := NewCompressedStore(store)

	inputDir := t.TempDir()
	snapshot := []byte("snapshot-state")
	memory := append([]byte("memory"), make([]byte, 2<<20)...)
	seed := append([]byte("ext4-seed"), make([]byte, 2<<20)...)
	snapshotPath := writeTestArtifact(t, inputDir, "snap", snapshot)
	memoryPath := writeTestArtifact(t, inputDir, "mem", memory)
	seedPath := writeTestArtifact(t, inputDir, "seed", seed)

	manifest, err := PublishRelease(ctx, artifacts, repo, ReleaseInputs{
		ReleaseID:          "standard-test",
		CreatedAt:          time.Unix(1, 0).UTC(),
		Arch:               "amd64",
		CPUCompatClass:     "test-cpu",
		FirecrackerVersion: "v1",
		RootfsDigest:       "rootfs",
		KernelDigest:       "kernel",
		Variants: []BuiltVariant{{
			Size:             "nano",
			VCPUs:            1,
			MemoryMB:         256,
			DiskMB:           1024,
			SnapshotPath:     snapshotPath,
			MemoryPath:       memoryPath,
			WritableSeedPath: seedPath,
			VsockPath:        "/run/renderops/template.vsock",
			TapName:          "fc-tap-template",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Activate(ctx, manifest.ReleaseID); err != nil {
		t.Fatal(err)
	}
	ptr, exists, err := repo.Active(ctx)
	if err != nil || !exists || ptr.ReleaseID != manifest.ReleaseID {
		t.Fatalf("active pointer = %#v, exists=%t, err=%v", ptr, exists, err)
	}

	cache, err := NewDiskCache(filepath.Join(t.TempDir(), "cache"), artifacts, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := cache.Ensure(ctx, manifest, "nano")
	if err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, cached.SnapshotPath, snapshot)
	assertFileContent(t, cached.MemoryPath, memory)
	assertFileContent(t, cached.WritableSeedPath, seed)

	// A stale .ready marker must not make a corrupted artifact trustworthy.
	if err := os.WriteFile(cached.SnapshotPath, []byte("corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Ensure(ctx, manifest, "nano"); err != nil {
		t.Fatalf("repair corrupt cache: %v", err)
	}
	assertFileContent(t, cached.SnapshotPath, snapshot)
}

func TestManifestRejectsWrongArtifactNames(t *testing.T) {
	t.Parallel()

	m := validManifest()
	v := m.Variants["nano"]
	v.Snapshot.Name = ArtifactMemory
	m.Variants["nano"] = v
	if err := m.Validate(); err == nil {
		t.Fatal("expected wrong artifact name to be rejected")
	}
}

func TestCompatibilityValidatorReportsAllMismatches(t *testing.T) {
	t.Parallel()

	m := validManifest()
	err := NewCompatibilityValidator().Compatible(m, HostFacts{
		Arch:               "arm64",
		CPUCompatClass:     "other",
		FirecrackerVersion: "v2",
		RootfsDigest:       "other-rootfs",
		KernelDigest:       "other-kernel",
	})
	incompatible, ok := err.(*IncompatibleError)
	if !ok {
		t.Fatalf("expected IncompatibleError, got %T: %v", err, err)
	}
	if len(incompatible.Reasons) != 5 {
		t.Fatalf("got %d mismatch reasons, want 5: %v", len(incompatible.Reasons), incompatible.Reasons)
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion:      SchemaVersion,
		ReleaseID:          "release-1",
		CreatedAt:          time.Unix(1, 0).UTC(),
		Arch:               "amd64",
		CPUCompatClass:     "cpu",
		FirecrackerVersion: "v1",
		RootfsDigest:       "rootfs",
		KernelDigest:       "kernel",
		Variants: map[string]Variant{
			"nano": {
				Size:     "nano",
				VCPUs:    1,
				MemoryMB: 256,
				DiskMB:   1024,
				Snapshot: Artifact{
					Name: ArtifactSnapshot, SHA256: "snapshot", Bytes: 1,
				},
				Memory: Artifact{
					Name: ArtifactMemory, SHA256: "memory", Bytes: 1,
				},
				WritableSeed: Artifact{
					Name: ArtifactWritableSeed, SHA256: "seed", Bytes: 1,
				},
				VsockPath: "/run/renderops/template.vsock",
				TapName:   "fc-tap-template",
			},
		},
	}
}

func writeTestArtifact(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s content mismatch: got %d bytes, want %d", path, len(got), len(want))
	}
}
