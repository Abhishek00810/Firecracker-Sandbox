package template

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"time"
)

// cacheReadyMarker is written LAST into a variant's cache dir, so a partially
// downloaded variant is never mistaken for a complete one.
const cacheReadyMarker = ".ready"

// DiskCache is the worker's on-NVMe LocalCache. It downloads a release's variant
// artifacts from the (compressed) store, verifies each against the manifest
// checksum, and places them immutably in cacheRoot/<release>/<size>/. Cached
// files are chowned to the Firecracker user so the VMM (which drops to fcvm) can
// read the snapshot and reflink-clone the seed.
//
// The cache must live on the SAME reflink-capable filesystem as the per-sandbox
// writable disks, or seed cloning falls back to a full copy.
type DiskCache struct {
	root  string
	store ArtifactStore
	fcUID int
	fcGID int
}

// NewDiskCache creates a cache rooted at dir (created if needed). store must be
// the artifact store the builder published through (e.g. a CompressedStore over
// blob) so Get returns decompressed bytes.
func NewDiskCache(dir string, store ArtifactStore, fcUID, fcGID int) (*DiskCache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create template cache dir %s: %w", dir, err)
	}
	if fcUID > 0 {
		_ = os.Chown(dir, fcUID, fcGID)
	}
	return &DiskCache{root: dir, store: store, fcUID: fcUID, fcGID: fcGID}, nil
}

var _ LocalCache = (*DiskCache)(nil)

func (c *DiskCache) Path(releaseID, size string) string {
	return filepath.Join(c.root, releaseID, size)
}

func (c *DiskCache) Has(releaseID, size string) bool {
	_, err := os.Stat(filepath.Join(c.Path(releaseID, size), cacheReadyMarker))
	return err == nil
}

func (c *DiskCache) cachedVariant(releaseID, size string) CachedVariant {
	dir := c.Path(releaseID, size)
	return CachedVariant{
		ReleaseID:        releaseID,
		Size:             size,
		SnapshotPath:     filepath.Join(dir, ArtifactSnapshot),
		MemoryPath:       filepath.Join(dir, ArtifactMemory),
		WritableSeedPath: filepath.Join(dir, ArtifactWritableSeed),
	}
}

// Ensure downloads + verifies + caches the variant if not already present, and
// returns its on-disk paths. It never leaves a partial entry marked ready.
func (c *DiskCache) Ensure(ctx context.Context, m Manifest, size string) (CachedVariant, error) {
	v, ok := m.Variant(size)
	if !ok {
		return CachedVariant{}, fmt.Errorf("release %s has no variant %q", m.ReleaseID, size)
	}
	cv := c.cachedVariant(m.ReleaseID, size)
	if c.Has(m.ReleaseID, size) {
		return cv, nil // immutable, already cached
	}

	dir := c.Path(m.ReleaseID, size)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return CachedVariant{}, fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	if c.fcUID > 0 {
		_ = os.Chown(dir, c.fcUID, c.fcGID)
	}

	for _, a := range []Artifact{v.Snapshot, v.Memory, v.WritableSeed} {
		dst := filepath.Join(dir, a.Name)
		if err := c.fetchVerify(ctx, ArtifactKey(m.ReleaseID, size, a.Name), dst, a.SHA256); err != nil {
			return CachedVariant{}, err
		}
		if c.fcUID > 0 {
			if err := os.Chown(dst, c.fcUID, c.fcGID); err != nil {
				return CachedVariant{}, fmt.Errorf("chown cached %s: %w", dst, err)
			}
		}
	}

	// Mark ready only after every artifact is present + verified.
	if err := os.WriteFile(filepath.Join(dir, cacheReadyMarker), []byte(time.Now().UTC().Format(time.RFC3339)), 0o644); err != nil {
		return CachedVariant{}, fmt.Errorf("write cache marker: %w", err)
	}
	return cv, nil
}

// fetchVerify downloads key to a temp file (hashing the decompressed stream),
// checks it against wantSHA, and atomically renames it into place.
func (c *DiskCache) fetchVerify(ctx context.Context, key, dst, wantSHA string) error {
	rc, err := c.store.Get(ctx, key)
	if err != nil {
		return err
	}
	defer rc.Close()

	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	h := sha256.New()
	if err := copySparse(f, rc, h); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", key, err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		os.Remove(tmp)
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", key, got, wantSHA)
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("commit %s: %w", dst, err)
	}
	return nil
}

// copySparse copies src into f while hashing EVERY byte (so the checksum matches the
// manifest's hash of the full image), but writes runs of zeros as file HOLES instead
// of data. A "10 GB" writable seed is mostly zeros, so this keeps it sparse on disk —
// otherwise gunzip would balloon it back to full size and fill the cache disk.
func copySparse(f *os.File, src io.Reader, h hash.Hash) error {
	const block = 1 << 20 // 1 MiB
	buf := make([]byte, block)
	var offset int64
	for {
		n, err := io.ReadFull(src, buf)
		if n > 0 {
			chunk := buf[:n]
			h.Write(chunk)
			if !isAllZero(chunk) {
				if _, werr := f.WriteAt(chunk, offset); werr != nil {
					return werr
				}
			}
			offset += int64(n)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return err
		}
	}
	// Set the final length so a trailing hole is preserved at the right size.
	return f.Truncate(offset)
}

func isAllZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
