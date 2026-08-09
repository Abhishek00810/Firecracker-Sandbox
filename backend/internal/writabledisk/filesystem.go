package writabledisk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type CloneMode string

const (
	CloneAuto     CloneMode = "auto"
	CloneRequired CloneMode = "required"
	CloneCopy     CloneMode = "copy"
)

func ParseCloneMode(value string) (CloneMode, error) {
	mode := CloneMode(strings.ToLower(strings.TrimSpace(value)))
	if mode == "" {
		return CloneAuto, nil
	}
	switch mode {
	case CloneAuto, CloneRequired, CloneCopy:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid writable disk clone mode %q: expected auto, required, or copy", value)
	}
}

type commandRunner func(ctx context.Context, name string, args ...string) ([]byte, error)

type Filesystem struct {
	root      string
	cloneMode CloneMode
	run       commandRunner
}

func NewFilesystem(root string, cloneMode CloneMode) (*Filesystem, error) {
	absRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return nil, fmt.Errorf("resolve writable disk root: %w", err)
	}
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("writable disk root is required")
	}
	if _, err := ParseCloneMode(string(cloneMode)); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absRoot, 0o750); err != nil {
		return nil, fmt.Errorf("create writable disk root %q: %w", absRoot, err)
	}
	return &Filesystem{
		root:      absRoot,
		cloneMode: cloneMode,
		run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		},
	}, nil
}

func (f *Filesystem) Root() string { return f.root }

func (f *Filesystem) Create(ctx context.Context, sandboxID string, sizeMiB int) (string, error) {
	if sizeMiB <= 0 {
		return "", fmt.Errorf("writable disk size must be positive, got %d MiB", sizeMiB)
	}
	path, err := f.pathFor(sandboxID)
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create writable disk %s: %w", path, err)
	}
	if err := file.Truncate(int64(sizeMiB) * 1024 * 1024); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("size sparse writable disk %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close writable disk %s: %w", path, err)
	}
	out, err := f.run(ctx, "mkfs.ext4", "-q", "-F", "-m", "0", path)
	if err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("format writable disk %s: %w: %s", path, err, out)
	}
	return path, nil
}

func (f *Filesystem) Clone(ctx context.Context, sandboxID, sourcePath string) (string, error) {
	if strings.TrimSpace(sourcePath) == "" {
		return "", errors.New("writable disk clone source is required")
	}
	path, err := f.pathFor(sandboxID)
	if err != nil {
		return "", err
	}

	args := make([]string, 0, 4)
	switch f.cloneMode {
	case CloneRequired:
		// GNU cp only permits reflink cloning with --sparse=auto. The cloned
		// ext4 image remains sparse because holes are shared by the reflink.
		args = append(args, "--sparse=auto", "--reflink=always")
	case CloneCopy:
		args = append(args, "--sparse=always", "--reflink=never")
	default:
		args = append(args, "--sparse=auto", "--reflink=auto")
	}
	args = append(args, sourcePath, path)
	if out, err := f.run(ctx, "cp", args...); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("clone writable disk %s to %s: %w: %s", sourcePath, path, err, out)
	}
	return path, nil
}

func (f *Filesystem) Delete(_ context.Context, path string) error {
	managedPath, err := f.validateManagedPath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(managedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("delete writable disk %s: %w", managedPath, err)
	}
	return nil
}

func (f *Filesystem) List(_ context.Context) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(f.root, "writable-*.ext4"))
	if err != nil {
		return nil, fmt.Errorf("list writable disks: %w", err)
	}
	return paths, nil
}

func (f *Filesystem) pathFor(sandboxID string) (string, error) {
	id := strings.TrimSpace(sandboxID)
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return "", fmt.Errorf("invalid sandbox id %q", sandboxID)
	}
	return filepath.Join(f.root, "writable-"+id+".ext4"), nil
}

func (f *Filesystem) validateManagedPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve writable disk path: %w", err)
	}
	if filepath.Dir(absPath) != f.root || !strings.HasPrefix(filepath.Base(absPath), "writable-") || filepath.Ext(absPath) != ".ext4" {
		return "", fmt.Errorf("writable disk path %q is outside store %q", path, f.root)
	}
	return absPath, nil
}
