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

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// apqServer builds a server with a real executor (nil pool: introspection
// queries like {__typename} resolve in memory).
func apqServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, nil, nil, exec.Options{DocumentCacheSize: 100})
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
	h := apqServer(t, config.Config{PersistedQueries: config.PersistedQueries{APQ: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}}).Handler()

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
	h := apqServer(t, config.Config{PersistedQueries: config.PersistedQueries{APQ: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}}).Handler()
	rec := postJSON(t, h, fmt.Sprintf(`{"query":%q,"extensions":{"persistedQuery":{"version":1,"sha256Hash":%q}}}`,
		apqQuery, hashQuery("something else")))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch accepted: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPQViaGET(t *testing.T) {
	h := apqServer(t, config.Config{PersistedQueries: config.PersistedQueries{APQ: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}}).Handler()
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
	cfg := config.Config{PersistedQueries: config.PersistedQueries{Path: path, Only: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}}
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
	if _, err := newPQStore(path, 0, 0); err == nil {
		t.Fatal("expected hash-mismatch error for bogus persisted file")
	}
}

// apqAuthServer is apqServer with jwt authentication and no anonymous role,
// so a request without credentials is answered 401.
func apqAuthServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	authn, err := NewAuthenticator(jwtAuth(), config.DefaultConfig().Roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg, authn, exec.New(nil, built, nil, nil, exec.Options{DocumentCacheSize: 100}), built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postBearer(t *testing.T, h http.Handler, body, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func apqRegister(t *testing.T, h http.Handler, query, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	return postBearer(t, h, fmt.Sprintf(`{"query":%q,"extensions":%s}`, query, apqExt(query)), bearer)
}

func apqLookup(t *testing.T, h http.Handler, query string) string {
	t.Helper()
	return postBearer(t, h, fmt.Sprintf(`{"extensions":%s}`, apqExt(query)), "").Body.String()
}

// TestAPQNotRegisteredBeforeAuthentication: a document sent for
// registration by a request that fails authentication is not registered, so
// a later hash-only request misses instead of finding it.
func TestAPQNotRegisteredBeforeAuthentication(t *testing.T) {
	s := apqAuthServer(t, config.Config{PersistedQueries: config.PersistedQueries{APQ: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}})
	h := s.Handler()
	for name, bearer := range map[string]string{"no credentials": "", "invalid token": "not-a-token"} {
		query := fmt.Sprintf("{ __typename } # %s", name)
		if rec := apqRegister(t, h, query, bearer); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s: registration answered %d, want 401: %s", name, rec.Code, rec.Body.String())
		}
		if body := apqLookup(t, h, query); !strings.Contains(body, "PersistedQueryNotFound") {
			t.Errorf("%s: document was registered: lookup answered %s", name, body)
		}
	}
	if _, n := s.pq.size(); n != 0 {
		t.Errorf("registered entries = %d, want 0", n)
	}
}

// TestAPQRegistersOnlyValidDocuments registers a document only once it
// parsed, passed the document limits and validated.
func TestAPQRegistersOnlyValidDocuments(t *testing.T) {
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, nil, nil, exec.Options{DocumentCacheSize: 100, MaxQueryTokens: 50})
	s, err := New(config.Config{PersistedQueries: config.PersistedQueries{APQ: true}, Limits: config.Limits{MaxBodyBytes: 1 << 20}}, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for name, query := range map[string]string{
		"parse error":      `{ __typename`,
		"validation error": `{ noSuchField }`,
		"token limit":      "{ " + strings.Repeat("a: __typename ", 30) + "}",
	} {
		if rec := apqRegister(t, h, query, ""); !strings.Contains(rec.Body.String(), `"errors"`) {
			t.Fatalf("%s: registration answered without errors: %s", name, rec.Body.String())
		}
		if body := apqLookup(t, h, query); !strings.Contains(body, "PersistedQueryNotFound") {
			t.Errorf("%s: rejected document was registered: lookup answered %s", name, body)
		}
	}
	if rec := apqRegister(t, h, apqQuery, ""); !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("valid registration: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if body := apqLookup(t, h, apqQuery); !strings.Contains(body, `"__typename":"Query"`) {
		t.Errorf("valid document was not registered: lookup answered %s", body)
	}
	if _, n := s.pq.size(); n != 1 {
		t.Errorf("registered entries = %d, want 1", n)
	}
}

// TestAPQStoreBoundedByBytes keeps the registered documents within
// persisted_queries.max_bytes, evicting the least recently used, and never
// registers a document above max_entry_bytes.
func TestAPQStoreBoundedByBytes(t *testing.T) {
	const maxBytes, maxEntry = 16 << 10, 2 << 10
	s, err := newPQStore("", maxBytes, maxEntry)
	if err != nil {
		t.Fatal(err)
	}
	query := func(i int) string { return fmt.Sprintf("{ __typename } # %d %s", i, strings.Repeat("x", 1000)) }
	first := query(0)
	s.put(hashQuery(first), first)
	for i := 1; i < 200; i++ {
		q := query(i)
		s.put(hashQuery(q), q)
		if _, ok := s.get(hashQuery(first)); !ok {
			t.Fatalf("recently used entry evicted after %d registrations", i)
		}
		if b, _ := s.size(); b > maxBytes {
			t.Fatalf("store holds %d bytes after %d registrations, above %d", b, i+1, maxBytes)
		}
	}
	if _, ok := s.get(hashQuery(query(1))); ok {
		t.Error("least recently used entry was not evicted")
	}
	b, n := s.size()
	if n < 10 || b < maxBytes-2*maxEntry {
		t.Errorf("store holds %d entries, %d bytes: the budget is not used", n, b)
	}

	big := "{ __typename } # " + strings.Repeat("y", maxEntry)
	s.put(hashQuery(big), big)
	if _, ok := s.get(hashQuery(big)); ok {
		t.Error("document above max_entry_bytes was registered")
	}
}

// TestAPQStoreDefaults bounds a store configured without limits by the
// defaults.
func TestAPQStoreDefaults(t *testing.T) {
	s, err := newPQStore("", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.maxBytes != defaultAPQBytes || s.maxEntry != defaultAPQEntryBytes {
		t.Errorf("bounds = %d/%d, want %d/%d", s.maxBytes, s.maxEntry, defaultAPQBytes, defaultAPQEntryBytes)
	}
}
