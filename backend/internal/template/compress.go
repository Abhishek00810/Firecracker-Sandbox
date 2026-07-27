package template

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
)

// CompressedStore wraps an ArtifactStore, transparently gzipping objects on Put
// and gunzipping on Get. Template artifacts — especially the writable seed, which
// is a mostly-zero ext4 image — compress enormously, so this keeps blob storage
// and transfer small. Callers always see raw bytes; only the stored object is gzip.
// The manifest keeps RAW checksums/sizes, so verification happens on the
// decompressed stream.
//
// Wrap this around the ARTIFACT store only; keep the manifest/active-pointer on the
// raw store so those small JSON objects stay human-readable in the bucket.
type CompressedStore struct {
	inner ArtifactStore
}

func NewCompressedStore(inner ArtifactStore) *CompressedStore {
	return &CompressedStore{inner: inner}
}

var _ ArtifactStore = (*CompressedStore)(nil)

// Put gzips r on the fly (via an io.Pipe, so a multi-GB artifact is never buffered
// in memory) and stores the compressed stream under key.
func (c *CompressedStore) Put(ctx context.Context, key string, r io.Reader) error {
	pr, pw := io.Pipe()
	go func() {
		gz := gzip.NewWriter(pw)
		_, err := io.Copy(gz, r)
		if err == nil {
			err = gz.Close()
		}
		// Propagate any compression error to the inner Put's reader as EOF/error.
		pw.CloseWithError(err)
	}()
	if err := c.inner.Put(ctx, key, pr); err != nil {
		pr.CloseWithError(err) // unblock the goroutine if the store write failed early
		return err
	}
	return nil
}

// Get downloads and gunzips key, returning the raw (decompressed) stream.
func (c *CompressedStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	rc, err := c.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(rc)
	if err != nil {
		rc.Close()
		return nil, fmt.Errorf("open gzip for %s: %w", key, err)
	}
	return &gzipReadCloser{gz: gz, under: rc}, nil
}

// Stat/List report on the underlying (compressed) object — size is the compressed
// size, which is fine for existence checks and listing.
func (c *CompressedStore) Stat(ctx context.Context, key string) (int64, bool, error) {
	return c.inner.Stat(ctx, key)
}

func (c *CompressedStore) List(ctx context.Context, prefix string) ([]string, error) {
	return c.inner.List(ctx, prefix)
}

// gzipReadCloser closes both the gzip reader and the underlying object stream.
type gzipReadCloser struct {
	gz    *gzip.Reader
	under io.ReadCloser
}

func (g *gzipReadCloser) Read(p []byte) (int, error) { return g.gz.Read(p) }

func (g *gzipReadCloser) Close() error {
	gzErr := g.gz.Close()
	underErr := g.under.Close()
	if gzErr != nil {
		return gzErr
	}
	return underErr
}
