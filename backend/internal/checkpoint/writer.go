package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"backend/internal/template"

	"github.com/google/uuid"
)

const manifestVersion = 1

// Input is the complete local state required to resume a paused sandbox.
type Input struct {
	SandboxID        string
	VCPUs            int
	MemoryMB         int
	DiskGB           int
	RootfsPath       string
	VsockPath        string
	TapName          string
	SnapshotPath     string
	MemoryPath       string
	WritableDiskPath string
}

type Resources struct {
	VCPUs    int `json:"vcpus"`
	MemoryMB int `json:"memory_mb"`
	DiskGB   int `json:"disk_gb"`
}

type ResumeMetadata struct {
	RootfsPath string `json:"rootfs_path"`
	VsockPath  string `json:"vsock_path"`
	TapName    string `json:"tap_name"`
}

// Artifact identifies one immutable object in a checkpoint generation.
type Artifact struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// Manifest is committed only after every checkpoint artifact is durable.
type Manifest struct {
	Version      int            `json:"version"`
	SandboxID    string         `json:"sandbox_id"`
	Generation   string         `json:"generation"`
	CreatedAt    time.Time      `json:"created_at"`
	Resources    Resources      `json:"resources"`
	Resume       ResumeMetadata `json:"resume"`
	Snapshot     Artifact       `json:"snapshot"`
	Memory       Artifact       `json:"memory"`
	WritableDisk Artifact       `json:"writable_disk"`
}

type activePointer struct {
	Version     int       `json:"version"`
	SandboxID   string    `json:"sandbox_id"`
	Generation  string    `json:"generation"`
	ManifestKey string    `json:"manifest_key"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Writer publishes immutable checkpoint generations to an object store.
type Writer struct {
	store  template.ArtifactStore
	prefix string
	now    func() time.Time
}

func NewWriter(store template.ArtifactStore, prefix string) (*Writer, error) {
	if store == nil {
		return nil, fmt.Errorf("checkpoint store is required")
	}
	prefix = strings.Trim(path.Clean(prefix), "/")
	if prefix == "" || prefix == "." || prefix == ".." || strings.HasPrefix(prefix, "../") {
		return nil, fmt.Errorf("invalid checkpoint prefix %q", prefix)
	}
	return &Writer{store: store, prefix: prefix, now: time.Now}, nil
}

// Save uploads the three resume artifacts and commits the manifest and active
// pointer last. Partial generations are therefore never selected for recovery.
func (w *Writer) Save(ctx context.Context, in Input) (string, error) {
	if _, err := uuid.Parse(in.SandboxID); err != nil {
		return "", fmt.Errorf("invalid sandbox id %q: %w", in.SandboxID, err)
	}
	if in.SnapshotPath == "" || in.MemoryPath == "" || in.WritableDiskPath == "" {
		return "", fmt.Errorf("checkpoint requires snapshot, memory and writable disk paths")
	}

	generation := uuid.NewString()
	base := path.Join(w.prefix, in.SandboxID, "generations", generation)
	createdAt := w.now().UTC()

	snapshot, err := w.putFile(ctx, path.Join(base, "vmstate.snap"), in.SnapshotPath)
	if err != nil {
		return "", fmt.Errorf("upload VM state: %w", err)
	}
	memory, err := w.putFile(ctx, path.Join(base, "memory.mem"), in.MemoryPath)
	if err != nil {
		return "", fmt.Errorf("upload memory: %w", err)
	}
	writable, err := w.putFile(ctx, path.Join(base, "writable.ext4"), in.WritableDiskPath)
	if err != nil {
		return "", fmt.Errorf("upload writable disk: %w", err)
	}

	manifest := Manifest{
		Version:      manifestVersion,
		SandboxID:    in.SandboxID,
		Generation:   generation,
		CreatedAt:    createdAt,
		Resources:    Resources{VCPUs: in.VCPUs, MemoryMB: in.MemoryMB, DiskGB: in.DiskGB},
		Resume:       ResumeMetadata{RootfsPath: in.RootfsPath, VsockPath: in.VsockPath, TapName: in.TapName},
		Snapshot:     snapshot,
		Memory:       memory,
		WritableDisk: writable,
	}
	manifestKey := path.Join(base, "manifest.json")
	if err := w.putJSON(ctx, manifestKey, manifest); err != nil {
		return "", fmt.Errorf("commit checkpoint manifest: %w", err)
	}
	pointer := activePointer{
		Version:     manifestVersion,
		SandboxID:   in.SandboxID,
		Generation:  generation,
		ManifestKey: manifestKey,
		UpdatedAt:   createdAt,
	}
	if err := w.putJSON(ctx, path.Join(w.prefix, in.SandboxID, "latest.json"), pointer); err != nil {
		return "", fmt.Errorf("activate checkpoint: %w", err)
	}
	return manifestKey, nil
}

func (w *Writer) putFile(ctx context.Context, key, filePath string) (Artifact, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return Artifact{}, fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	hash := sha256.New()
	counter := &byteCounter{}
	if err := w.store.Put(ctx, key, io.TeeReader(f, io.MultiWriter(hash, counter))); err != nil {
		return Artifact{}, err
	}
	return Artifact{Key: key, SizeBytes: counter.n, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

func (w *Writer) putJSON(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return w.store.Put(ctx, key, strings.NewReader(string(data)))
}

type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}
