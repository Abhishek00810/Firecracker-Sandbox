package writabledisk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseCloneMode(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  CloneMode
	}{
		{"", CloneAuto},
		{"AUTO", CloneAuto},
		{"required", CloneRequired},
		{"copy", CloneCopy},
	} {
		got, err := ParseCloneMode(tc.input)
		if err != nil || got != tc.want {
			t.Fatalf("ParseCloneMode(%q) = %q, %v; want %q", tc.input, got, err, tc.want)
		}
	}
	if _, err := ParseCloneMode("fallback"); err == nil {
		t.Fatal("expected invalid clone mode to fail")
	}
}

func TestNewSelectsBackend(t *testing.T) {
	store, err := New(Config{Backend: BackendFilesystem, Root: t.TempDir(), CloneMode: CloneAuto})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	if _, ok := store.(*Filesystem); !ok {
		t.Fatalf("New() returned %T, want *Filesystem", store)
	}
	if _, err := New(Config{Backend: "remote-nbd", Root: t.TempDir()}); err == nil {
		t.Fatal("expected an unsupported backend to fail")
	}
}

func TestFilesystemCreateBuildsSparseExt4Image(t *testing.T) {
	store := newTestFilesystem(t, CloneAuto)
	var command string
	var args []string
	store.run = func(_ context.Context, name string, gotArgs ...string) ([]byte, error) {
		command = name
		args = append([]string(nil), gotArgs...)
		return nil, nil
	}

	path, err := store.Create(context.Background(), "sandbox-1", 8)
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	if info.Size() != 8*1024*1024 {
		t.Fatalf("image size = %d, want %d", info.Size(), 8*1024*1024)
	}
	if command != "mkfs.ext4" {
		t.Fatalf("command = %q, want mkfs.ext4", command)
	}
	wantArgs := []string{"-q", "-F", "-m", "0", path}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("mkfs args = %#v, want %#v", args, wantArgs)
	}
}

func TestFilesystemCloneModes(t *testing.T) {
	for _, tc := range []struct {
		mode       CloneMode
		sparseFlag string
		reflink   string
	}{
		{CloneAuto, "--sparse=auto", "--reflink=auto"},
		{CloneRequired, "--sparse=auto", "--reflink=always"},
		{CloneCopy, "--sparse=always", "--reflink=never"},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			store := newTestFilesystem(t, tc.mode)
			var args []string
			store.run = func(_ context.Context, _ string, gotArgs ...string) ([]byte, error) {
				args = append([]string(nil), gotArgs...)
				return nil, nil
			}
			path, err := store.Clone(context.Background(), "sandbox-2", "/templates/golden.ext4")
			if err != nil {
				t.Fatalf("Clone() error: %v", err)
			}
			want := []string{tc.sparseFlag, tc.reflink, "/templates/golden.ext4", path}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("clone args = %#v, want %#v", args, want)
			}
		})
	}
}

func TestFilesystemCloneFailureRemovesPartialImage(t *testing.T) {
	store := newTestFilesystem(t, CloneRequired)
	store.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		destination := args[len(args)-1]
		if err := os.WriteFile(destination, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		return []byte("reflink not supported"), errors.New("exit status 1")
	}
	path := filepath.Join(store.Root(), "writable-sandbox-3.ext4")
	if _, err := store.Clone(context.Background(), "sandbox-3", "/templates/golden.ext4"); err == nil {
		t.Fatal("expected clone failure")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial clone still exists: %v", err)
	}
}

func TestFilesystemListAndDeleteAreStoreScoped(t *testing.T) {
	store := newTestFilesystem(t, CloneAuto)
	managed := filepath.Join(store.Root(), "writable-sandbox-4.ext4")
	if err := os.WriteFile(managed, []byte("disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store.Root(), "other.ext4"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(paths, []string{managed}) {
		t.Fatalf("List() = %#v, want %#v", paths, []string{managed})
	}
	if err := store.Delete(context.Background(), managed); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), filepath.Join(t.TempDir(), "writable-foreign.ext4")); err == nil {
		t.Fatal("expected deleting a path outside the store to fail")
	}
}

func TestFilesystemRejectsUnsafeSandboxID(t *testing.T) {
	store := newTestFilesystem(t, CloneAuto)
	if _, err := store.Create(context.Background(), "../escape", 1); err == nil {
		t.Fatal("expected unsafe sandbox id to fail")
	}
}

func newTestFilesystem(t *testing.T, mode CloneMode) *Filesystem {
	t.Helper()
	store, err := NewFilesystem(t.TempDir(), mode)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
