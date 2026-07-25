package cache

import (
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/testutil"
)

func TestRoundTrip(t *testing.T) {
	cat := testutil.FixtureCatalog()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, cat); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := cat.Hash()
	h2, _ := loaded.Hash()
	if h1 != h2 {
		t.Fatalf("hash mismatch after round trip: %s != %s", h1, h2)
	}
}

func TestCheckDetectsDrift(t *testing.T) {
	cat := testutil.FixtureCatalog()
	path := filepath.Join(t.TempDir(), "schema.cache")
	if err := Save(path, cat); err != nil {
		t.Fatal(err)
	}
	if drift, err := Check(path, testutil.FixtureCatalog()); err != nil || drift != "" {
		t.Fatalf("no-drift check failed: drift=%q err=%v", drift, err)
	}
	changed := testutil.FixtureCatalog()
	changed.Tables[0].Columns = changed.Tables[0].Columns[1:]
	drift, err := Check(path, changed)
	if err != nil {
		t.Fatal(err)
	}
	if drift == "" {
		t.Fatal("expected drift to be detected")
	}
	if !strings.Contains(drift, "column") {
		t.Fatalf("expected drift detail naming the dropped column, got %q", drift)
	}
}

func TestLoadRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bogus")
	if err := os.WriteFile(path, []byte("not a cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for non-gzip input")
	}
}

func TestLoadRejectsDecompressionBomb(t *testing.T) {
	orig := maxCacheBytes
	maxCacheBytes = 1 << 20 // shrink so the test stays fast
	defer func() { maxCacheBytes = orig }()
	path := filepath.Join(t.TempDir(), "bomb.cache")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	// A stream of zeros compresses ~1000:1, so an over-limit payload is a
	// tiny file on disk — exactly the gzip-bomb shape Load must refuse.
	if _, err := io.CopyN(zw, zeroReader{}, maxCacheBytes+2); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Load(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want decompressed-size error", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}
