package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"backend/internal/executor/firecracker"
	"backend/internal/template"
	"backend/internal/vmsize"
	workerconfig "backend/internal/workerplane/config"
)

// buildSpec is one template variant to build: its shape, what to run inside the
// build VM (warm-up + provision commands), and how to validate the resulting
// snapshot after restoring it.
type buildSpec struct {
	Name      string
	VCPUs     int // integer vCPU count (overcommit is a runtime lever, not baked here)
	MemoryMB  int
	DiskMB    int
	Warmup    bool     // run Node/Python warm-up before snapshot
	Provision []string // commands run in the build VM; a failure aborts the build
	Validate  []string // commands run on the RESTORED snapshot; must exit 0 to publish
}

// standardSpecs derives the standard sizes from vmsize.Sizes (the single source of
// truth) and bakes numpy into each. Disk is converted GB->MB here; CPU stays an
// integer count.
func standardSpecs(provision []string) []buildSpec {
	specs := make([]buildSpec, 0, len(vmsize.Sizes))
	for _, sz := range vmsize.Sizes {
		specs = append(specs, buildSpec{
			Name:      sz.Name,
			VCPUs:     sz.VCPUs,
			MemoryMB:  sz.MemoryMB,
			DiskMB:    sz.DiskGB * 1024,
			Warmup:    true,
			Provision: provision,
			Validate:  []string{"python3 -c 'import numpy'"},
		})
	}
	return specs
}

// microSpec is the bare benchmark variant: 1 vCPU / 128 MB / 512 MB, no warm-up,
// no packages. It's the smallest possible template — used to benchmark the
// build->publish->restore pipeline with tiny artifacts. Under 2x CPU overcommit its
// effective guaranteed share is ~0.5 vCPU.
func microSpec() buildSpec {
	return buildSpec{
		Name:     "micro",
		VCPUs:    1,
		MemoryMB: 128,
		DiskMB:   512,
		Warmup:   false,
		Validate: []string{"true"}, // prove it restores + the guest agent responds
	}
}

func (s buildSpec) vmConfig(cfg *workerconfig.Config) firecracker.VMConfig {
	return firecracker.VMConfig{
		VCPUCount:  s.VCPUs,
		MemSizeMiB: s.MemoryMB,
		DiskMB:     s.DiskMB,
		Timeout:    30 * time.Second,
		KernelPath: cfg.KernelPath,
		RootfsPath: cfg.RootfsPath,
		InitrdPath: cfg.InitrdPath,
		BootArgs:   "console=ttyS0 reboot=k panic=1 pci=off random.trust_cpu=on rng_core.default_quality=1024",
	}
}

// buildAndValidate builds one variant, then restores its snapshot and runs the
// validation commands (Codex's "validate the real artifact" gate). It returns the
// built variant ready to publish.
func buildAndValidate(ctx context.Context, mgr *firecracker.FireCrackerManager, cfg *workerconfig.Config, s buildSpec, snapRoot string) (template.BuiltVariant, error) {
	vmCfg := s.vmConfig(cfg)
	snapDir := filepath.Join(snapRoot, s.Name)

	tmpl, err := mgr.CreateTemplateWithProvision(ctx, vmCfg, snapDir, firecracker.TemplateBuildOptions{
		Warmup:    s.Warmup,
		Provision: s.Provision,
	})
	if err != nil {
		return template.BuiltVariant{}, fmt.Errorf("build %s: %w", s.Name, err)
	}

	if err := validateSnapshot(ctx, mgr, vmCfg, tmpl, s.Validate); err != nil {
		return template.BuiltVariant{}, fmt.Errorf("validate %s: %w", s.Name, err)
	}

	return template.BuiltVariant{
		Size:             s.Name,
		VCPUs:            s.VCPUs,
		MemoryMB:         s.MemoryMB,
		DiskMB:           s.DiskMB,
		SnapshotPath:     tmpl.SnapPath,
		MemoryPath:       tmpl.MemPath,
		WritableSeedPath: tmpl.WritableDiskPath,
	}, nil
}

// validateSnapshot restores a just-built snapshot into a throwaway VM and runs the
// validation commands over vsock. Any failure means the artifact must not ship.
func validateSnapshot(ctx context.Context, mgr *firecracker.FireCrackerManager, cfg firecracker.VMConfig, tmpl *firecracker.SnapshotTemplate, cmds []string) error {
	vm, err := mgr.LoadFromSnapshot(ctx, cfg, tmpl)
	if err != nil {
		return fmt.Errorf("restore for validation: %w", err)
	}
	defer mgr.Destroy(ctx, vm.ID)

	vsock := firecracker.NewVsockClient(vm.VsockPath)
	ready := false
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if ok, _, _ := vsock.Ping(); ok {
			ready = true
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !ready {
		return fmt.Errorf("restored VM vsock never became ready")
	}

	conn, err := vsock.Connect()
	if err != nil {
		return fmt.Errorf("vsock connect: %w", err)
	}
	defer conn.Close()
	for _, c := range cmds {
		resp, err := vsock.ExecOnConn(conn, c, 120)
		if err != nil {
			return fmt.Errorf("validation %q transport failed: %w", c, err)
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("validation %q failed (exit %d): %s", c, resp.ExitCode, strings.TrimSpace(resp.Stderr))
		}
	}
	return nil
}

// facts are the compatibility pins measured from the build host, recorded in every
// release manifest so a worker only restores a release its host actually matches.
type facts struct {
	arch, cpuCompatClass, firecrackerVersion      string
	assetBundleDigest, rootfsDigest, kernelDigest string
}

func gatherFacts(cfg *workerconfig.Config) (facts, error) {
	var f facts
	f.arch = runtime.GOARCH

	rootfs, err := template.SHA256File(cfg.RootfsPath)
	if err != nil {
		return f, fmt.Errorf("hash rootfs: %w", err)
	}
	f.rootfsDigest = rootfs

	kernel, err := template.SHA256File(cfg.KernelPath)
	if err != nil {
		return f, fmt.Errorf("hash kernel: %w", err)
	}
	f.kernelDigest = kernel

	f.firecrackerVersion = firecrackerVersion(cfg.FirecrackerBinary)
	f.cpuCompatClass = cpuCompatClass(f.arch)
	f.assetBundleDigest = assetBundleDigest(cfg.AssetsPath)
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
