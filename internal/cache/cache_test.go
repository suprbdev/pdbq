package cache

import (
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
