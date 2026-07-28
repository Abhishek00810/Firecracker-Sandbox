// Package template models the standard-template RELEASE: the immutable set of
// Firecracker snapshot artifacts (one per size) that a builder publishes to
// durable object storage and workers download + restore. It holds only the
// shared vocabulary and helpers — types, manifest schema, checksums, storage
// interfaces, and a local-dir store for dev/tests. It boots no VMs and depends
// on nothing but the standard library, so both the (future) builder and the
// worker can import it without pulling in Firecracker or a cloud SDK.
//
// A RELEASE is one build. It contains three SIZE variants (nano/small/medium)
// and is addressed by an immutable release_id. Activation/rollback flips a
// separate, tiny active pointer; the release contents never change.
package template

import (
	"encoding/json"
	"fmt"
	"time"
)

// SchemaVersion is the manifest format version. Bump on any incompatible change
// to the on-disk/object JSON so an older worker can refuse a newer manifest.
const SchemaVersion = 1

// Artifact file names within a variant. These are the object keys' leaf names
// and the cached files' basenames — kept in one place so builder and worker agree.
const (
	ArtifactSnapshot     = "snap"               // Firecracker VM + device + CPU state
	ArtifactMemory       = "mem"                // guest RAM dump
	ArtifactWritableSeed = "writable-seed.ext4" // golden overlay upper disk (baked packages live here)
)

// Artifact is one immutable file in a release variant, pinned by content hash.
type Artifact struct {
	Name   string `json:"name"`   // one of the Artifact* constants
	SHA256 string `json:"sha256"` // lowercase hex
	Bytes  int64  `json:"bytes"`
}

// Variant is one size (nano/small/medium) within a release. Each size boots a
// differently-shaped VM, so each has its own snapshot + memory + writable seed.
type Variant struct {
	Size         string   `json:"size"`  // "nano" | "small" | "medium" | "micro"
	VCPUs        int      `json:"vcpus"` // integer vCPU count (E2B-style; overcommit is a runtime lever)
	MemoryMB     int      `json:"memory_mb"`
	DiskMB       int      `json:"disk_mb"`       // MB granularity so sub-GB sizes (e.g. micro) are expressible
	Snapshot     Artifact `json:"snapshot"`      // .snap  — CPU + device state
	Memory       Artifact `json:"memory"`        // .mem   — RAM
	WritableSeed Artifact `json:"writable_seed"` // .ext4  — golden upper disk seed
	// Device names BAKED into the snapshot at build time. A restoring worker must
	// reproduce them: Firecracker re-binds VsockPath on resume and the slot TAP is
	// renamed to TapName. Both are absolute/host names, so builder and worker must
	// share the same SocketDir convention (standardized).
	VsockPath string `json:"vsock_path"`
	TapName   string `json:"tap_name"`
}

// Manifest describes ONE immutable standard-template release (all sizes together).
// The rootfs/kernel/firecracker are NOT stored in the release — they are pinned by
// digest here and reused from the worker's asset bundle, so we never ship the same
// rootfs three times. A manifest is written only once every artifact is durably
// uploaded and validated; presence of a valid manifest + the active pointer means READY.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	ReleaseID     string    `json:"release_id"`
	CreatedAt     time.Time `json:"created_at"`
	Arch          string    `json:"arch"` // e.g. "amd64"

	// Compatibility pins. A worker restores this release only if its own host
	// matches these (see CompatibilityValidator). CPUCompatClass captures the
	// CPU-feature class the snapshots were built against (Firecracker bakes CPUID).
	CPUCompatClass     string `json:"cpu_compat_class"`
	FirecrackerVersion string `json:"firecracker_version"`
	AssetBundleDigest  string `json:"asset_bundle_digest"`
	RootfsDigest       string `json:"rootfs_digest"`
	KernelDigest       string `json:"kernel_digest"`

	// Variants keyed by size name ("nano"/"small"/"medium").
	Variants map[string]Variant `json:"variants"`
}

// ActivePointer names the currently-active release. It is small and mutable —
// flipped last on activation and re-pointed on rollback — while the release it
// references stays immutable. Workers read this to learn which release to sync.
type ActivePointer struct {
	ReleaseID string    `json:"release_id"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Variant returns the named size variant and whether it exists.
func (m Manifest) Variant(size string) (Variant, bool) {
	v, ok := m.Variants[size]
	return v, ok
}

// Validate checks a manifest is internally complete enough to publish. It does
// not verify that the referenced artifacts exist in storage — that is the
// publisher's job (upload artifacts, then write the manifest, then activate).
func (m Manifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest schema_version %d != supported %d", m.SchemaVersion, SchemaVersion)
	}
	if m.ReleaseID == "" {
		return fmt.Errorf("manifest release_id is empty")
	}
	if m.Arch == "" {
		return fmt.Errorf("manifest arch is empty")
	}
	if m.CPUCompatClass == "" || m.FirecrackerVersion == "" || m.RootfsDigest == "" || m.KernelDigest == "" {
		return fmt.Errorf("manifest is missing a compatibility pin (cpu_compat_class/firecracker_version/rootfs_digest/kernel_digest)")
	}
	if len(m.Variants) == 0 {
		return fmt.Errorf("manifest has no variants")
	}
	for size, v := range m.Variants {
		if size != v.Size {
			return fmt.Errorf("variant map key %q != variant.size %q", size, v.Size)
		}
		if v.VCPUs <= 0 || v.MemoryMB <= 0 || v.DiskMB <= 0 {
			return fmt.Errorf("variant %q has non-positive resources", size)
		}
		if v.VsockPath == "" || v.TapName == "" {
			return fmt.Errorf("variant %q missing baked device names (vsock_path/tap_name)", size)
		}
		for _, pair := range []struct {
			want string
			got  Artifact
		}{
			{ArtifactSnapshot, v.Snapshot},
			{ArtifactMemory, v.Memory},
			{ArtifactWritableSeed, v.WritableSeed},
		} {
			a := pair.got
			if a.Name != pair.want {
				return fmt.Errorf("variant %q artifact name %q != expected %q", size, a.Name, pair.want)
			}
			if a.SHA256 == "" {
				return fmt.Errorf("variant %q artifact %q missing sha256", size, a.Name)
			}
			if a.Bytes <= 0 {
				return fmt.Errorf("variant %q artifact %q has non-positive size", size, a.Name)
			}
		}
	}
	return nil
}

// MarshalManifest serializes a manifest to canonical, indented JSON for storage.
func MarshalManifest(m Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal manifest: %w", err)
	}
	return data, nil
}

// UnmarshalManifest parses a manifest from stored JSON and validates it.
func UnmarshalManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("unmarshal manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, fmt.Errorf("invalid manifest: %w", err)
	}
	return m, nil
}
