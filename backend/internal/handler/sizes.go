package handler

import "fmt"

// allowedSizes = the resource shapes we can actually serve today. Each needs a
// matching snapshot template + pool. Today: one (the default). Add entries as
// per-size templates land — no API change required.
var allowedSizes = []Resources{
	{VCPUs: 1, MemoryMB: 256, DiskGB: 0}, // default
	// {VCPUs: 2, MemoryMB: 512, DiskGB: 5}, // ← uncomment when the medium template exists
}

// resolveSize validates a requested resource shape against allowedSizes. A nil or
// all-zero request returns the default size.
func resolveSize(r *Resources) (Resources, error) {
	if r == nil || (r.VCPUs == 0 && r.MemoryMB == 0 && r.DiskGB == 0) {
		return allowedSizes[0], nil
	}
	for _, s := range allowedSizes {
		if *r == s {
			return s, nil
		}
	}
	return Resources{}, fmt.Errorf("unsupported size %+v (allowed: %+v)", *r, allowedSizes)
}

