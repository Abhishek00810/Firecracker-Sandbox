package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureLayout(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout: %v", err)
	}
	for _, sub := range Layout {
		if fi, err := os.Stat(filepath.Join(root, sub)); err != nil || !fi.IsDir() {
			t.Fatalf("expected dir %q: err=%v", sub, err)
		}
	}
	if err := EnsureLayout(root); err != nil {
		t.Fatalf("EnsureLayout is not idempotent: %v", err)
	}
	if err := EnsureLayout(""); err == nil {
		t.Fatal("expected error for empty root")
	}
}

// buildBundle writes an asset bundle at root/BundleName in the exact format
// `make bundle` produces (tar of assets/... plus a sha256sum-format manifest),
// so this exercises real Makefile↔agent compatibility. Returns file contents.
func buildBundle(t *testing.T, root string) map[string]string {
	t.Helper()
	src := t.TempDir()
	assetsSrc := filepath.Join(src, "assets")
	files := map[string]string{
		"kernel/vmlinux":            "KERNEL-bytes",
		"rootfs/rootfs-alpine.ext4": "ROOTFS-bytes",
		"initramfs.cpio.gz":         "INITRD-bytes",
		"firecracker":               "#!/bin/sh\nexit 0\n",
	}
	for rel, content := range files {
		p := filepath.Join(assetsSrc, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var mf strings.Builder
	for _, rel := range []string{"kernel/vmlinux", "rootfs/rootfs-alpine.ext4", "initramfs.cpio.gz", "firecracker"} {
		sum, err := sha256File(filepath.Join(assetsSrc, rel))
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&mf, "%s  %s\n", sum, rel) // sha256sum format
	}
	if err := os.WriteFile(filepath.Join(assetsSrc, "manifest.sha256"), []byte(mf.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	// Mirror `make bundle`: tar the files with no top-level dir, so the agent
	// extracts them straight into $ROOT/assets.
	args := []string{"-czf", filepath.Join(root, BundleName), "-C", assetsSrc,
		"kernel/vmlinux", "rootfs/rootfs-alpine.ext4", "initramfs.cpio.gz", "firecracker", "manifest.sha256"}
	if out, err := exec.Command("tar", args...).CombinedOutput(); err != nil {
		t.Fatalf("tar: %v: %s", err, out)
	}
	return files
}

func TestEnsureAssetsBundleRoundTrip(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	files := buildBundle(t, root)

	if err := EnsureAssets(root); err != nil {
		t.Fatalf("EnsureAssets: %v", err)
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(root, "assets", rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("%s content mismatch: got %q want %q", rel, got, want)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, "assets", "firecracker")); err != nil {
		t.Fatal(err)
	} else if fi.Mode()&0o100 == 0 {
		t.Fatalf("firecracker not executable: %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(root, "assets", installedMarker)); err != nil {
		t.Fatalf("install marker missing: %v", err)
	}
	if err := EnsureAssets(root); err != nil {
		t.Fatalf("EnsureAssets not idempotent: %v", err)
	}
}

func TestEnsureAssetsNoBundleNoAssets(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	err := EnsureAssets(root)
	if err == nil || !strings.Contains(err.Error(), "must push") {
		t.Fatalf("expected 'must push' error, got %v", err)
	}
}

// TestEnsureAssetsVerifiesEvenOnFastPath proves the marker gate does not blindly
// trust: an unchanged bundle still triggers manifest verification, catching a
// tampered/corrupted asset on disk.
func TestEnsureAssetsVerifiesEvenOnFastPath(t *testing.T) {
	root := t.TempDir()
	if err := EnsureLayout(root); err != nil {
		t.Fatal(err)
	}
	buildBundle(t, root)
	if err := EnsureAssets(root); err != nil {
		t.Fatal(err)
	}
	// Corrupt an unpacked asset; leave the bundle (so extract is skipped by the
	// marker) — verification must still fire and reject it.
	if err := os.WriteFile(filepath.Join(root, "assets", "kernel", "vmlinux"), []byte("CORRUPT"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := EnsureAssets(root)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}
