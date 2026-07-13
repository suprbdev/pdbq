package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// apqServer builds a server with a real executor (nil pool: introspection
// queries like {__typename} resolve in memory).
func apqServer(t *testing.T, cfg config.Server) *Server {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, nil, nil, exec.Options{})
	s, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postJSON(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/graphql", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	return rec
}

const apqQuery = `{__typename}`

func apqExt(query string) string {
	return fmt.Sprintf(`{"persistedQuery":{"version":1,"sha256Hash":%q}}`, hashQuery(query))
}

func TestAPQRoundTrip(t *testing.T) {
	h := apqServer(t, config.Server{APQ: true, MaxBodyBytes: 1 << 20}).Handler()

	// 1. Hash-only miss -> PersistedQueryNotFound.
	rec := postJSON(t, h, fmt.Sprintf(`{"extensions":%s}`, apqExt(apqQuery)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "PersistedQueryNotFound") {
		t.Fatalf("miss: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 2. Register: query + matching hash executes and caches.
	rec = postJSON(t, h, fmt.Sprintf(`{"query":%q,"extensions":%s}`, apqQuery, apqExt(apqQuery)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("register: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// 3. Hash-only hit executes the cached document.
	rec = postJSON(t, h, fmt.Sprintf(`{"extensions":%s}`, apqExt(apqQuery)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("hit: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPQHashMismatchRejected(t *testing.T) {
	h := apqServer(t, config.Server{APQ: true, MaxBodyBytes: 1 << 20}).Handler()
	rec := postJSON(t, h, fmt.Sprintf(`{"query":%q,"extensions":{"persistedQuery":{"version":1,"sha256Hash":%q}}}`,
		apqQuery, hashQuery("something else")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch accepted: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPQViaGET(t *testing.T) {
	h := apqServer(t, config.Server{APQ: true, MaxBodyBytes: 1 << 20}).Handler()
	// Register via POST, fetch via GET with the extensions query param.
	postJSON(t, h, fmt.Sprintf(`{"query":%q,"extensions":%s}`, apqQuery, apqExt(apqQuery)))
	rec := get(t, h, "/graphql?extensions="+`{"persistedQuery":{"version":1,"sha256Hash":"`+hashQuery(apqQuery)+`"}}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("GET hit: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPersistedQueriesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persisted.json")
	doc, _ := json.Marshal(map[string]string{hashQuery(apqQuery): apqQuery})
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := config.Server{PersistedQueriesPath: path, PersistedOnly: true, MaxBodyBytes: 1 << 20}
	h := apqServer(t, cfg).Handler()

	// Listed hash executes.
	rec := postJSON(t, h, fmt.Sprintf(`{"extensions":%s}`, apqExt(apqQuery)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("allowlisted: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// Arbitrary queries are rejected in persisted-only mode.
	rec = postJSON(t, h, fmt.Sprintf(`{"query":%q}`, apqQuery))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("raw query accepted in persisted_only mode: code=%d", rec.Code)
	}
	// Registration is rejected without APQ.
	rec = postJSON(t, h, fmt.Sprintf(`{"query":"{__schema {queryType {name}}}","extensions":%s}`, apqExt("{__schema {queryType {name}}}")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("registration accepted without apq: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPersistedFileHashValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persisted.json")
	if err := os.WriteFile(path, []byte(`{"deadbeef":"{__typename}"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newPQStore(path); err == nil {
		t.Fatal("expected hash-mismatch error for bogus persisted file")
	}
}
