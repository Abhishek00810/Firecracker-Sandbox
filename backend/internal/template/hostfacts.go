package template

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// GatherHostFacts measures the compatibility pins from THIS host: arch, CPU class,
// Firecracker version, and the digests of the rootfs/kernel assets. The builder
// records these in a release manifest; a worker compares its own facts against a
// manifest (via CompatibilityValidator) before restoring a release.
func GatherHostFacts(rootfsPath, kernelPath, firecrackerBinary, assetsPath string) (HostFacts, error) {
	var f HostFacts
	f.Arch = runtime.GOARCH

	rootfs, err := SHA256File(rootfsPath)
	if err != nil {
		return f, fmt.Errorf("hash rootfs: %w", err)
	}
	f.RootfsDigest = rootfs

	kernel, err := SHA256File(kernelPath)
	if err != nil {
		return f, fmt.Errorf("hash kernel: %w", err)
	}
	f.KernelDigest = kernel

	f.FirecrackerVersion = firecrackerVersion(firecrackerBinary)
	f.CPUCompatClass = cpuCompatClass(f.Arch)
	f.AssetBundleDigest = assetBundleDigest(assetsPath)
	return f, nil
}

// firecrackerVersion runs `firecracker --version` and returns its first line.
func firecrackerVersion(binary string) string {
	out, err := exec.Command(binary, "--version").Output()
	if err != nil {
		return "unknown"
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if line == "" {
		return "unknown"
	}
	return line
}

// cpuCompatClass returns a conservative CPU class: the CPU model name from
// /proc/cpuinfo, so a release only restores on the same CPU model (Firecracker
// bakes CPUID into snapshots). Falls back to the arch when the model is unknown.
func cpuCompatClass(arch string) string {
	f, err := os.Open("/proc/cpuinfo")
	if err != nil {
		return "arch-" + arch
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "model name") {
			if i := strings.Index(line, ":"); i >= 0 {
				if v := strings.TrimSpace(line[i+1:]); v != "" {
					return v
				}
			}
		}
	}
	return "arch-" + arch
}

// assetBundleDigest reads the installed-bundle marker written by bootstrap, if any.
func assetBundleDigest(assetsPath string) string {
	data, err := os.ReadFile(filepath.Join(assetsPath, ".installed-bundle"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
