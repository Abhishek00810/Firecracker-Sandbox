package bootstrap

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BundleName is where the control plane pushes the asset tarball (see the
// Makefile `agent` target). Its contents unpack to $ROOT/assets/... — kernel,
// rootfs, initramfs, the firecracker binary, and a manifest.sha256.
const BundleName = "renderops-assets.tar.gz"

// installedMarker records the sha256 of the bundle last unpacked, so an
// unchanged bundle is a no-op on restart instead of a needless re-extract.
const installedMarker = ".installed-bundle"

// EnsureAssets makes $ROOT/assets current from the pushed bundle: if a newer
// bundle is present it is unpacked, then every file is verified against the
// bundle's manifest.sha256. No bundle and no existing assets is an error — the
// agent cannot boot VMs without them. Idempotent.
func EnsureAssets(root string) error {
	assetsDir := filepath.Join(root, "assets")
	bundle := filepath.Join(root, BundleName)
	manifest := filepath.Join(assetsDir, "manifest.sha256")

	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		return fmt.Errorf("bootstrap: create assets dir: %w", err)
	}

	if fileExists(bundle) {
		sum, err := sha256File(bundle)
		if err != nil {
			return fmt.Errorf("bootstrap: hash bundle: %w", err)
		}
		marker := filepath.Join(assetsDir, installedMarker)
		if prev, _ := os.ReadFile(marker); strings.TrimSpace(string(prev)) != sum {
			if err := extractBundle(bundle, assetsDir); err != nil {
				return fmt.Errorf("bootstrap: extract bundle: %w", err)
			}
			if err := os.WriteFile(marker, []byte(sum), 0o644); err != nil {
				return fmt.Errorf("bootstrap: write install marker: %w", err)
			}
		}
	}

	if !fileExists(manifest) {
		return fmt.Errorf("bootstrap: no assets and no bundle at %s — the control plane must push one", bundle)
	}
	if err := verifyManifest(assetsDir, manifest); err != nil {
		return fmt.Errorf("bootstrap: asset verification failed: %w", err)
	}
	// The firecracker binary ships in the bundle; make sure it's executable.
	if fc := filepath.Join(assetsDir, "firecracker"); fileExists(fc) {
		if err := os.Chmod(fc, 0o755); err != nil {
			return fmt.Errorf("bootstrap: chmod firecracker: %w", err)
		}
	}
	return nil
}

// extractBundle unpacks the tarball into assetsDir. We shell out to tar rather
// than use archive/tar: tar is universally present and handles the ~1GB rootfs
// and file modes cleanly. The bundle has no top-level dir — its entries are
// kernel/…, rootfs/…, firecracker, manifest.sha256 — so extracting with -C
// assetsDir lands them at $ROOT/assets/… regardless of what the source dir that
// built the bundle was named.
func extractBundle(bundle, assetsDir string) error {
	if out, err := exec.Command("tar", "-xzf", bundle, "-C", assetsDir).CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, out)
	}
	return nil
}

// verifyManifest recomputes the sha256 of each "sha  relpath" line (sha256sum
// format, paths relative to assetsDir) and fails on any mismatch or missing file.
func verifyManifest(assetsDir, manifest string) error {
	f, err := os.Open(manifest)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return fmt.Errorf("malformed manifest line: %q", line)
		}
		want, rel := fields[0], fields[len(fields)-1]
		got, err := sha256File(filepath.Join(assetsDir, rel))
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		if got != want {
			return fmt.Errorf("%s: checksum mismatch (want %s, got %s)", rel, want, got)
		}
	}
	return sc.Err()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
