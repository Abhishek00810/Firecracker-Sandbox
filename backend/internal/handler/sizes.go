package handler

import "backend/internal/vmsize"

// resolveSize validates a requested resource shape against the canonical size menu
// (vmsize.Sizes — the single source of truth shared with pool construction + routing).
// A nil or all-zero request returns the default size.
func resolveSize(r *Resources) (vmsize.Size, error) {
	if r == nil {
		return vmsize.Default(), nil
	}
	return vmsize.Resolve(r.VCPUs, r.MemoryMB, r.DiskGB)
}
