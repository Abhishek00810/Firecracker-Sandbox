package template

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// SHA256File returns the lowercase-hex SHA-256 of a file's contents. It streams
// the file so a multi-GB memory snapshot is hashed without loading it into RAM.
// This mirrors the asset-bundle verification in internal/bootstrap so template
// artifacts use the same integrity mechanism.
func SHA256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SHA256Reader returns the lowercase-hex SHA-256 of everything read from r.
func SHA256Reader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hash reader: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// VerifyFile recomputes a file's SHA-256 and fails if it does not match want.
// Used after every download before a file is placed in the immutable cache, so
// a corrupt transfer never becomes a usable template.
func VerifyFile(path, want string) error {
	got, err := SHA256File(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s want %s", path, got, want)
	}
	return nil
}
