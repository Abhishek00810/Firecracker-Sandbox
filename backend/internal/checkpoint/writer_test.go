package checkpoint

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"backend/internal/template"
)

func TestWriterCommitsPointerLast(t *testing.T) {
	store := newRecordingStore()
	writer, err := NewWriter(store, "sandbox-checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	writer.now = func() time.Time { return time.Date(2026, 8, 12, 1, 2, 3, 0, time.UTC) }

	dir := t.TempDir()
	snap := writeTestFile(t, dir, "snap", "vm-state")
	mem := writeTestFile(t, dir, "mem", "guest-memory")
	disk := writeTestFile(t, dir, "disk", "user-data")
	sandboxID := "a6846a48-db37-46bc-907a-3a9e15093603"

	manifestKey, err := writer.Save(context.Background(), Input{
		SandboxID: sandboxID, SnapshotPath: snap, MemoryPath: mem, WritableDiskPath: disk,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.order) != 5 {
		t.Fatalf("writes=%d, want 5: %v", len(store.order), store.order)
	}
	if store.order[3] != manifestKey {
		t.Fatalf("manifest write=%q, want %q", store.order[3], manifestKey)
	}
	wantPointer := "sandbox-checkpoints/" + sandboxID + "/latest.json"
	if store.order[4] != wantPointer {
		t.Fatalf("last write=%q, want %q", store.order[4], wantPointer)
	}

	var manifest Manifest
	if err := json.Unmarshal(store.objects[manifestKey], &manifest); err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256([]byte("user-data"))
	if manifest.WritableDisk.SizeBytes != int64(len("user-data")) || manifest.WritableDisk.SHA256 != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("unexpected writable artifact: %+v", manifest.WritableDisk)
	}
}

func TestWriterDoesNotCommitManifestAfterArtifactFailure(t *testing.T) {
	store := newRecordingStore()
	store.failAt = 2
	writer, err := NewWriter(store, "sandbox-checkpoints")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err = writer.Save(context.Background(), Input{
		SandboxID:        "a6846a48-db37-46bc-907a-3a9e15093603",
		SnapshotPath:     writeTestFile(t, dir, "snap", "snap"),
		MemoryPath:       writeTestFile(t, dir, "mem", "mem"),
		WritableDiskPath: writeTestFile(t, dir, "disk", "disk"),
	})
	if err == nil {
		t.Fatal("expected upload failure")
	}
	for _, key := range store.order {
		if strings.HasSuffix(key, "manifest.json") || strings.HasSuffix(key, "latest.json") {
			t.Fatalf("committed %q after partial upload", key)
		}
	}
}

func writeTestFile(t *testing.T, dir, name, contents string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

type recordingStore struct {
	objects map[string][]byte
	order   []string
	failAt  int
}

func newRecordingStore() *recordingStore { return &recordingStore{objects: make(map[string][]byte)} }

func (s *recordingStore) Put(_ context.Context, key string, r io.Reader) error {
	s.order = append(s.order, key)
	if s.failAt > 0 && len(s.order) == s.failAt {
		return fmt.Errorf("injected failure")
	}
	b, err := io.ReadAll(r)
	if err == nil {
		s.objects[key] = b
	}
	return err
}

func (s *recordingStore) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, template.ErrNotFound
}
func (s *recordingStore) Stat(context.Context, string) (int64, bool, error) { return 0, false, nil }
func (s *recordingStore) List(context.Context, string) ([]string, error)    { return nil, nil }
