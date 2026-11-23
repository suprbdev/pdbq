package server

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

const (
	csrfMutation  = `mutation { createUser(input: {user: {email: "x@example.com"}}) { clientMutationId } }`
	mixedDocument = `query q { __typename } mutation m { createUser(input: {user: {email: "x@example.com"}}) { clientMutationId } }`
)

// csrfServer builds a server whose executor is in read-only mode, so a
// mutation that gets past the transport checks answers 200 with the
// read-only error instead of reaching the (nil) pool.
func csrfServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Subscriptions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	var ro atomic.Bool
	ro.Store(true)
	ex := exec.New(nil, built, nil, nil, exec.Options{DocumentCacheSize: 100, ReadOnly: &ro})
	if cfg.Limits.MaxBodyBytes == 0 {
		cfg.Limits.MaxBodyBytes = 1 << 20
	}
	s, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func getGraphQL(t *testing.T, h http.Handler, params url.Values) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graphql?"+params.Encode(), nil))
	return rec
}

func postGraphQL(t *testing.T, h http.Handler, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	h.ServeHTTP(rec, req)
	return rec
}

func TestGETRunsQueries(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	rec := getGraphQL(t, h, url.Values{"query": {`{ __typename }`}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("GET query: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGETRejectsMutationsAndSubscriptions(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	for _, params := range []url.Values{
		{"query": {csrfMutation}},
		{"query": {`subscription { listen(topic: "t") { relatedNodeId } }`}},
		{"query": {mixedDocument}, "operationName": {"m"}},
	} {
		rec := getGraphQL(t, h, params)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %v: %d %s, want 405", params, rec.Code, rec.Body.String())
			continue
		}
		if got := rec.Header().Get("Allow"); got != http.MethodPost {
			t.Errorf("GET %v: Allow = %q, want POST", params, got)
		}
		if !strings.Contains(rec.Body.String(), "not allowed over GET") {
			t.Errorf("GET %v: body %s", params, rec.Body.String())
		}
	}
}

func TestHEADRejectsMutations(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/graphql?"+url.Values{"query": {csrfMutation}}.Encode(),
		strings.NewReader(fmt.Sprintf(`{"query":%q}`, csrfMutation)))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("HEAD mutation: %d, want 405", rec.Code)
	}
}

func TestGETSelectsQueryFromMixedDocument(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	rec := getGraphQL(t, h, url.Values{"query": {mixedDocument}, "operationName": {"q"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("GET query of mixed document: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGETInvalidDocumentReportsValidationError(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	rec := getGraphQL(t, h, url.Values{"query": {`mutation { nope }`}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `Cannot query field \"nope\"`) {
		t.Fatalf("GET invalid mutation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestPOSTRunsMutations(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	for _, ct := range []string{"application/json", "application/json; charset=utf-8", "Application/JSON"} {
		rec := postGraphQL(t, h, ct, fmt.Sprintf(`{"query":%q}`, csrfMutation))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "read-only mode") {
			t.Errorf("POST %s mutation: %d %s, want it to reach the executor", ct, rec.Code, rec.Body.String())
		}
	}
}

func TestPOSTRejectsNonJSONContentTypes(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	body := fmt.Sprintf(`{"query":%q}`, csrfMutation)
	for _, ct := range []string{
		"",
		"text/plain",
		"text/plain; charset=utf-8",
		"application/x-www-form-urlencoded",
		"multipart/form-data; boundary=x",
		"application/graphql",
		"application/jsonx",
		"application/json; charset",
	} {
		rec := postGraphQL(t, h, ct, body)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("POST Content-Type %q: %d %s, want 415", ct, rec.Code, rec.Body.String())
		}
	}
}

func TestPersistedMutationOverGETRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pq.json")
	if err := os.WriteFile(path, fmt.Appendf(nil, `{%q:%q,%q:%q}`,
		hashQuery(csrfMutation), csrfMutation, hashQuery(apqQuery), apqQuery), 0o600); err != nil {
		t.Fatal(err)
	}
	h := csrfServer(t, config.Config{PersistedQueries: config.PersistedQueries{Path: path, Only: true}}).Handler()

	rec := getGraphQL(t, h, url.Values{"extensions": {apqExt(csrfMutation)}})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET persisted mutation: %d %s, want 405", rec.Code, rec.Body.String())
	}

	rec = getGraphQL(t, h, url.Values{"extensions": {apqExt(apqQuery)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"__typename":"Query"`) {
		t.Fatalf("GET persisted query: %d %s", rec.Code, rec.Body.String())
	}

	rec = postGraphQL(t, h, "application/json", fmt.Sprintf(`{"extensions":%s}`, apqExt(csrfMutation)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "read-only mode") {
		t.Fatalf("POST persisted mutation: %d %s, want it to reach the executor", rec.Code, rec.Body.String())
	}
}

func TestAPQMutationOverGETRejected(t *testing.T) {
	h := csrfServer(t, config.Config{PersistedQueries: config.PersistedQueries{APQ: true}}).Handler()
	rec := getGraphQL(t, h, url.Values{"query": {csrfMutation}, "extensions": {apqExt(csrfMutation)}})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET APQ register mutation: %d %s, want 405", rec.Code, rec.Body.String())
	}
	rec = getGraphQL(t, h, url.Values{"extensions": {apqExt(csrfMutation)}})
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET APQ hash-only mutation: %d %s, want 405", rec.Code, rec.Body.String())
	}
}

func TestOtherMethodsRejected(t *testing.T) {
	h := csrfServer(t, config.Config{}).Handler()
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(m, "/graphql", strings.NewReader(fmt.Sprintf(`{"query":%q}`, csrfMutation)))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /graphql: %d, want 405", m, rec.Code)
		}
	}
}
