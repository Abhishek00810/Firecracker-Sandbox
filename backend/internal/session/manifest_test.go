package session

import (
	"path/filepath"
	"testing"
)

func TestPausedRecordPreservesImmutableRootfs(t *testing.T) {
	want := filepath.Join(t.TempDir(), "rootfs-version.ext4")
	session := &Session{ID: "sandbox-1", RootfsPathAtPause: want}
	recovered := recordFromSession(session).toSession()
	if recovered.RootfsPathAtPause != want {
		t.Fatalf("rootfs path = %q, want %q", recovered.RootfsPathAtPause, want)
	}
}
