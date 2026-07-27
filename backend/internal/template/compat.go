package template

import (
	"fmt"
	"strings"
)

// HostFacts describes the runtime environment a worker would restore a release
// on. The worker gathers these at startup (arch, CPU class, Firecracker version,
// and the digests of its asset-bundle rootfs/kernel) and asks a
// CompatibilityValidator whether a given release is safe to restore here.
type HostFacts struct {
	Arch               string
	CPUCompatClass     string
	FirecrackerVersion string
	AssetBundleDigest  string
	RootfsDigest       string
	KernelDigest       string
}

// CompatibilityValidator decides whether a release manifest can be restored on a
// host. Firecracker snapshots bake CPU features, a kernel, a rootfs and a
// Firecracker version, so a mismatch is unsafe — this fails CLOSED (production
// must never silently cold-boot an incompatible or missing template).
type CompatibilityValidator interface {
	Compatible(m Manifest, host HostFacts) error
}

// IncompatibleError lists every reason a release is not restorable on a host.
type IncompatibleError struct {
	ReleaseID string
	Reasons   []string
}

func (e *IncompatibleError) Error() string {
	return fmt.Sprintf("release %s incompatible with host: %s", e.ReleaseID, strings.Join(e.Reasons, "; "))
}

// digestValidator requires an exact match on every compatibility pin.
type digestValidator struct{}

// NewCompatibilityValidator returns the default validator: exact-match on arch,
// CPU class, Firecracker version, and rootfs/kernel digests.
func NewCompatibilityValidator() CompatibilityValidator { return digestValidator{} }

var _ CompatibilityValidator = digestValidator{}

func (digestValidator) Compatible(m Manifest, host HostFacts) error {
	var reasons []string
	mismatch := func(field, want, got string) {
		if want != got {
			reasons = append(reasons, fmt.Sprintf("%s: template=%q host=%q", field, want, got))
		}
	}
	mismatch("arch", m.Arch, host.Arch)
	mismatch("cpu_compat_class", m.CPUCompatClass, host.CPUCompatClass)
	mismatch("firecracker_version", m.FirecrackerVersion, host.FirecrackerVersion)
	mismatch("rootfs_digest", m.RootfsDigest, host.RootfsDigest)
	mismatch("kernel_digest", m.KernelDigest, host.KernelDigest)
	// Asset-bundle digest is a coarser roll-up; compare only when both sides know
	// it, so a host that doesn't track it isn't rejected on that alone.
	if m.AssetBundleDigest != "" && host.AssetBundleDigest != "" {
		mismatch("asset_bundle_digest", m.AssetBundleDigest, host.AssetBundleDigest)
	}
	if len(reasons) > 0 {
		return &IncompatibleError{ReleaseID: m.ReleaseID, Reasons: reasons}
	}
	return nil
}
