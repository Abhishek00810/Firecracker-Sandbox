package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	cases := []struct{ in, want string }{
		{"", filepath.Join(home, "aman")},   // default
		{"~", home},                         // bare tilde
		{"~/aman", filepath.Join(home, "aman")},
		{"~/aman/", filepath.Join(home, "aman")}, // trailing slash cleaned
		{"  ~/aman  ", filepath.Join(home, "aman")}, // trimmed
	}
	for _, c := range cases {
		got, err := resolveRoot(c.in)
		if err != nil {
			t.Fatalf("resolveRoot(%q): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("resolveRoot(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// An absolute path passes through untouched.
	abs := t.TempDir()
	if got, err := resolveRoot(abs); err != nil || got != abs {
		t.Fatalf("resolveRoot(%q) = %q, err=%v; want passthrough", abs, got, err)
	}
}
