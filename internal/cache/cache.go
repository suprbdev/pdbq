// Package cache (de)serializes the introspected Catalog so pdbq can boot
// without touching pg_catalog: gzip-compressed JSON with a format version and
// a content hash for drift detection (`pdbq schema dump|check`).
package cache

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/suprbdev/pdbq/internal/introspect"
)

// envelope wraps the catalog with versioning + hash metadata.
type envelope struct {
	FormatVersion int                 `json:"format_version"`
	Hash          string              `json:"hash"`
	Catalog       *introspect.Catalog `json:"catalog"`
}

// Save writes the catalog to path as gzipped JSON.
func Save(path string, cat *introspect.Catalog) error {
	hash, err := cat.Hash()
	if err != nil {
		return fmt.Errorf("cache: hash: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("cache: create: %w", err)
	}
	defer f.Close()
	zw := gzip.NewWriter(f)
	enc := json.NewEncoder(zw)
	if err := enc.Encode(envelope{
		FormatVersion: introspect.CatalogFormatVersion,
		Hash:          hash,
		Catalog:       cat,
	}); err != nil {
		return fmt.Errorf("cache: encode: %w", err)
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("cache: gzip: %w", err)
	}
	return f.Close()
}

// Load reads a catalog written by Save, verifying version and hash.
func Load(path string) (*introspect.Catalog, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cache: open: %w", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("cache: not a pdbq schema cache (gzip): %w", err)
	}
	defer zr.Close()
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("cache: read: %w", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("cache: decode: %w", err)
	}
	if env.FormatVersion != introspect.CatalogFormatVersion {
		return nil, fmt.Errorf("cache: format version %d, want %d — re-run `pdbq schema dump`",
			env.FormatVersion, introspect.CatalogFormatVersion)
	}
	if env.Catalog == nil {
		return nil, fmt.Errorf("cache: empty catalog")
	}
	hash, err := env.Catalog.Hash()
	if err != nil {
		return nil, err
	}
	if hash != env.Hash {
		return nil, fmt.Errorf("cache: hash mismatch (corrupted file)")
	}
	return env.Catalog, nil
}

// Check compares a live catalog against a cache file; returns a non-empty
// description on drift.
func Check(path string, live *introspect.Catalog) (string, error) {
	cached, err := Load(path)
	if err != nil {
		return "", err
	}
	ch, err := cached.Hash()
	if err != nil {
		return "", err
	}
	lh, err := live.Hash()
	if err != nil {
		return "", err
	}
	if ch != lh {
		lines := []string{fmt.Sprintf("schema drift detected: cache %s != live %s", ch[:12], lh[:12])}
		for _, d := range introspect.Diff(cached, live) {
			lines = append(lines, "  "+d)
		}
		return strings.Join(lines, "\n"), nil
	}
	return "", nil
}
