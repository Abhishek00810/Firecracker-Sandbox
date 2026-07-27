package template

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// ManifestRepository resolves the active release and reads/writes release
// manifests. Phase 1 is manifest-only (no database): it is backed by objects in
// an ArtifactStore. Later phases may add a Postgres catalog at the control-plane
// layer, but the worker-facing source of truth stays the manifest in storage.
type ManifestRepository interface {
	// Active returns the current active-release pointer. exists is false (nil
	// error) when no release has been activated yet.
	Active(ctx context.Context) (ptr ActivePointer, exists bool, err error)
	// Get reads and validates a release's manifest. Returns ErrNotFound if absent.
	Get(ctx context.Context, releaseID string) (Manifest, error)
	// Publish writes a release's immutable manifest. Call ONLY after every
	// artifact the manifest references is durably uploaded and validated — a
	// release is not usable until its manifest exists.
	Publish(ctx context.Context, m Manifest) error
	// Activate flips the active pointer to releaseID. Call LAST, after Publish,
	// so activation is atomic and never exposes a half-uploaded release. It
	// verifies the target manifest exists before switching.
	Activate(ctx context.Context, releaseID string) error
}

// storeManifestRepo implements ManifestRepository over an ArtifactStore.
type storeManifestRepo struct {
	store ArtifactStore
	now   func() time.Time
}

// NewStoreManifestRepository returns a ManifestRepository backed by store.
func NewStoreManifestRepository(store ArtifactStore) ManifestRepository {
	return &storeManifestRepo{store: store, now: time.Now}
}

var _ ManifestRepository = (*storeManifestRepo)(nil)

func (r *storeManifestRepo) Active(ctx context.Context) (ActivePointer, bool, error) {
	data, err := r.getObject(ctx, ActiveKey())
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ActivePointer{}, false, nil
		}
		return ActivePointer{}, false, err
	}
	var ptr ActivePointer
	if err := jsonUnmarshal(data, &ptr); err != nil {
		return ActivePointer{}, false, fmt.Errorf("decode active pointer: %w", err)
	}
	if ptr.ReleaseID == "" {
		return ActivePointer{}, false, fmt.Errorf("active pointer has empty release_id")
	}
	return ptr, true, nil
}

func (r *storeManifestRepo) Get(ctx context.Context, releaseID string) (Manifest, error) {
	if releaseID == "" {
		return Manifest{}, fmt.Errorf("release id is required")
	}
	data, err := r.getObject(ctx, ManifestKey(releaseID))
	if err != nil {
		return Manifest{}, err
	}
	return UnmarshalManifest(data)
}

func (r *storeManifestRepo) Publish(ctx context.Context, m Manifest) error {
	if err := m.Validate(); err != nil {
		return fmt.Errorf("refusing to publish invalid manifest: %w", err)
	}
	data, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	if err := r.store.Put(ctx, ManifestKey(m.ReleaseID), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("publish manifest %s: %w", m.ReleaseID, err)
	}
	return nil
}

func (r *storeManifestRepo) Activate(ctx context.Context, releaseID string) error {
	if releaseID == "" {
		return fmt.Errorf("release id is required")
	}
	// Never activate a release whose manifest is missing/invalid.
	if _, err := r.Get(ctx, releaseID); err != nil {
		return fmt.Errorf("cannot activate %s: %w", releaseID, err)
	}
	ptr := ActivePointer{ReleaseID: releaseID, UpdatedAt: r.now().UTC()}
	data, err := jsonMarshalIndent(ptr)
	if err != nil {
		return fmt.Errorf("encode active pointer: %w", err)
	}
	if err := r.store.Put(ctx, ActiveKey(), bytes.NewReader(data)); err != nil {
		return fmt.Errorf("activate %s: %w", releaseID, err)
	}
	return nil
}

// getObject reads a whole object into memory. Only used for small JSON objects
// (manifest, active pointer) — never for the multi-GB artifacts.
func (r *storeManifestRepo) getObject(ctx context.Context, key string) ([]byte, error) {
	rc, err := r.store.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", key, err)
	}
	return data, nil
}
