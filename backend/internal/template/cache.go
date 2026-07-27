package template

import "context"

// CachedVariant is the on-disk location of one verified size variant's files in
// the worker's local cache. These paths feed directly into the Firecracker
// restore path (snapshot + memory + writable-seed to reflink-clone).
type CachedVariant struct {
	ReleaseID        string
	Size             string
	SnapshotPath     string
	MemoryPath       string
	WritableSeedPath string
}

// LocalCache holds verified, IMMUTABLE release artifacts on the worker's
// persistent SSD/NVMe (never /dev/shm). A worker syncs the active release into
// the cache BEFORE it registers, so it only ever serves sandboxes it can restore.
//
// The implementation lives with the worker (Step 3), because it needs the
// worker's cache directory + ArtifactStore wiring; the interface is defined here
// so the shared toolkit owns the contract.
type LocalCache interface {
	// Has reports whether a fully-verified copy of (releaseID, size) is cached.
	Has(releaseID, size string) bool
	// Ensure downloads (if absent), verifies against the manifest checksums, and
	// atomically places the variant's artifacts in the immutable cache, returning
	// their on-disk paths. It must never leave a partially-written or unverified
	// entry visible.
	Ensure(ctx context.Context, m Manifest, size string) (CachedVariant, error)
	// Path returns the cache directory for (releaseID, size) whether or not it is
	// populated yet.
	Path(releaseID, size string) string
}
