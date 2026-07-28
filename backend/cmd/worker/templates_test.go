package main

import (
	"strings"
	"testing"
	"time"

	"backend/internal/template"
	"backend/internal/vmsize"
)

func TestValidateStandardRelease(t *testing.T) {
	t.Parallel()

	m := completeStandardManifest()
	if err := validateStandardRelease(m); err != nil {
		t.Fatalf("complete release rejected: %v", err)
	}

	delete(m.Variants, vmsize.Sizes[0].Name)
	if err := validateStandardRelease(m); err == nil || !strings.Contains(err.Error(), "missing required size") {
		t.Fatalf("missing size error = %v", err)
	}
}

func TestValidateStandardReleaseRejectsWrongResources(t *testing.T) {
	t.Parallel()

	m := completeStandardManifest()
	sz := vmsize.Sizes[0]
	v := m.Variants[sz.Name]
	v.MemoryMB++
	m.Variants[sz.Name] = v
	if err := validateStandardRelease(m); err == nil || !strings.Contains(err.Error(), "expected") {
		t.Fatalf("resource mismatch error = %v", err)
	}
}

func completeStandardManifest() template.Manifest {
	variants := make(map[string]template.Variant, len(vmsize.Sizes))
	for _, sz := range vmsize.Sizes {
		variants[sz.Name] = template.Variant{
			Size:     sz.Name,
			VCPUs:    sz.VCPUs,
			MemoryMB: sz.MemoryMB,
			DiskMB:   sz.DiskGB * 1024,
		}
	}
	return template.Manifest{
		SchemaVersion:      template.SchemaVersion,
		ReleaseID:          "standard-test",
		CreatedAt:          time.Unix(1, 0).UTC(),
		Arch:               "amd64",
		CPUCompatClass:     "cpu",
		FirecrackerVersion: "v1",
		RootfsDigest:       "rootfs",
		KernelDigest:       "kernel",
		Variants:           variants,
	}
}
