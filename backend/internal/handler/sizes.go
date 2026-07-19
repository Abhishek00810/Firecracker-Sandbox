package handler

import "backend/internal/vmsize"

// resolveSize validates the requested shape against the canonical size menu
// (vmsize.Sizes — the single source of truth shared with pool construction +
// routing). Explicit resources win, then a named size, then the default.
func resolveSize(req CreateSessionRequest) (vmsize.Size, error) {
	if req.Resources != nil {
		return vmsize.Resolve(req.Resources.VCPUs, req.Resources.MemoryMB, req.Resources.DiskGB)
	}
	if req.Size != "" {
		return vmsize.ByName(req.Size)
	}
	return vmsize.Default(), nil
}
