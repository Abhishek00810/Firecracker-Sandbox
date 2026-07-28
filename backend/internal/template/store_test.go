package template

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func mustPut(t *testing.T, s ArtifactStore, key string, data []byte) {
	t.Helper()
	if err := s.Put(context.Background(), key, bytes.NewReader(data)); err != nil {
		t.Fatalf("put %s: %v", key, err)
	}
}

func mustGet(t *testing.T, s ArtifactStore, key string) []byte {
	t.Helper()
	rc, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return data
}

func TestLocalStoreRoundTrip(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("hello template world")
	mustPut(t, s, "releases/r1/nano/snap", payload)

	if got := mustGet(t, s, "releases/r1/nano/snap"); !bytes.Equal(got, payload) {
		t.Fatalf("round trip mismatch: %q", got)
	}

	size, exists, err := s.Stat(context.Background(), "releases/r1/nano/snap")
	if err != nil || !exists || size != int64(len(payload)) {
		t.Fatalf("stat: size=%d exists=%v err=%v", size, exists, err)
	}

	keys, err := s.List(context.Background(), "releases/r1")
	if err != nil || len(keys) != 1 || keys[0] != "releases/r1/nano/snap" {
		t.Fatalf("list: %v err=%v", keys, err)
	}
}

func TestLocalStoreGetMissing(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	_, err := s.Get(context.Background(), "nope/missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	_, exists, err := s.Stat(context.Background(), "nope/missing")
	if err != nil || exists {
		t.Fatalf("stat missing: exists=%v err=%v", exists, err)
	}
}

func TestLocalStoreRejectsTraversal(t *testing.T) {
	s, _ := NewLocalStore(t.TempDir())
	if err := s.Put(context.Background(), "../escape", bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected traversal key to be rejected")
	}
}

func TestCompressedStoreRoundTripAndShrinksZeros(t *testing.T) {
	base, _ := NewLocalStore(t.TempDir())
	cs := NewCompressedStore(base)

	// A mostly-zero payload should compress to far less on the underlying store.
	payload := make([]byte, 1<<20) // 1 MiB of zeros
	copy(payload, []byte("marker"))
	mustPut(t, cs, "releases/r1/nano/mem", payload)

	if got := mustGet(t, cs, "releases/r1/nano/mem"); !bytes.Equal(got, payload) {
		t.Fatal("compressed round trip mismatch")
	}

	// The stored (compressed) object is much smaller than the raw payload.
	stored, exists, err := base.Stat(context.Background(), "releases/r1/nano/mem")
	if err != nil || !exists {
		t.Fatalf("stat compressed object: exists=%v err=%v", exists, err)
	}
	if stored >= int64(len(payload)) {
		t.Fatalf("compression did not shrink zeros: stored=%d raw=%d", stored, len(payload))
	}
}

func TestChecksum(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	// echo -n abc | sha256sum
	const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
	got, err := SHA256File(p)
	if err != nil || got != want {
		t.Fatalf("sha256=%s want=%s err=%v", got, want, err)
	}
	if err := VerifyFile(p, want); err != nil {
		t.Fatalf("verify should pass: %v", err)
	}
	if err := VerifyFile(p, "wronghash"); err == nil {
		t.Fatal("verify should fail on mismatch")
	}
}

func TestStoreManifestRepositoryPublishActivate(t *testing.T) {
	base, _ := NewLocalStore(t.TempDir())
	repo := NewStoreManifestRepository(base)
	ctx := context.Background()

	if _, ok, err := repo.Active(ctx); err != nil || ok {
		t.Fatalf("expected no active pointer initially: ok=%v err=%v", ok, err)
	}

	m := validManifest()
	if err := repo.Publish(ctx, m); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Activating a release whose manifest is missing must fail.
	if err := repo.Activate(ctx, "does-not-exist"); err == nil {
		t.Fatal("expected activate of missing release to fail")
	}

	if err := repo.Activate(ctx, m.ReleaseID); err != nil {
		t.Fatalf("activate: %v", err)
	}
	ptr, ok, err := repo.Active(ctx)
	if err != nil || !ok || ptr.ReleaseID != m.ReleaseID {
		t.Fatalf("active pointer wrong: %+v ok=%v err=%v", ptr, ok, err)
	}

	got, err := repo.Get(ctx, m.ReleaseID)
	if err != nil || got.ReleaseID != m.ReleaseID {
		t.Fatalf("get manifest: %+v err=%v", got, err)
	}
}
