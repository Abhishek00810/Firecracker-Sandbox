package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPausedRecordPreservesImmutableRootfs(t *testing.T) {
	want := filepath.Join(t.TempDir(), "rootfs-version.ext4")
	session := &Session{ID: "sandbox-1", RootfsPathAtPause: want, CheckpointRef: "sandbox-checkpoints/sandbox-1/generations/generation-1/manifest.json"}
	recovered := recordFromSession(session).toSession()
	if recovered.RootfsPathAtPause != want {
		t.Fatalf("rootfs path = %q, want %q", recovered.RootfsPathAtPause, want)
	}
	if recovered.CheckpointRef != session.CheckpointRef {
		t.Fatalf("checkpoint ref = %q, want %q", recovered.CheckpointRef, session.CheckpointRef)
	}
}

func TestReadManifestRetainsDurableCheckpointWhenLocalFilesAreMissing(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "paused-sessions.json")
	record := pausedRecord{
		ID:               "sandbox-1",
		CheckpointRef:    "sandbox-checkpoints/sandbox-1/generations/generation-1/manifest.json",
		SnapPath:         "/missing/snap",
		MemPath:          "/missing/mem",
		WritableDiskPath: "/missing/disk",
	}
	data, err := json.Marshal([]pausedRecord{record})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sessions, err := readManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].CheckpointRef != record.CheckpointRef {
		t.Fatalf("durable paused session was dropped: %#v", sessions)
	}
}
