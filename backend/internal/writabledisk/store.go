package writabledisk

import (
	"context"
	"fmt"
	"strings"
)

const BackendFilesystem = "filesystem"

type Config struct {
	Backend   string
	Root      string
	CloneMode CloneMode
}

// Store owns the writable ext4 block images attached to Firecracker guests.
// Implementations may use any storage system that can present each image as a
// local path to Firecracker, such as local NVMe, Azure Managed Disk, or EBS.
type Store interface {
	Create(ctx context.Context, sandboxID string, sizeMiB int) (string, error)
	Clone(ctx context.Context, sandboxID, sourcePath string) (string, error)
	Delete(ctx context.Context, path string) error
	List(ctx context.Context) ([]string, error)
	Root() string
}

func New(cfg Config) (Store, error) {
	backend := strings.ToLower(strings.TrimSpace(cfg.Backend))
	if backend == "" {
		backend = BackendFilesystem
	}
	switch backend {
	case BackendFilesystem:
		return NewFilesystem(cfg.Root, cfg.CloneMode)
	default:
		return nil, fmt.Errorf("unsupported writable disk backend %q", cfg.Backend)
	}
}
