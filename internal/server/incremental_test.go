package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func TestIncrementalAccept(t *testing.T) {
	cases := []struct {
		accept          string
		format          exec.IncrementalFormat
		ok, unsupported bool
	}{
		{"application/json", 0, false, false},
		{"", 0, false, false},
		{"multipart/mixed", exec.IncrementalV02, true, false},
		{"multipart/mixed;incrementalSpec=v0.2, application/json", exec.IncrementalV02, true, false},
		{`multipart/mixed; deferSpec="20220824", application/json`, exec.IncrementalDefer20220824, true, false},
		{"application/json, multipart/mixed;deferspec=20220824", exec.IncrementalDefer20220824, true, false},
		{"multipart/mixed;deferSpec=20200101", 0, false, true},
		{"multipart/mixed;incrementalSpec=v9, multipart/mixed;deferSpec=20220824", exec.IncrementalDefer20220824, true, false},
		{"multipart/mixed;boundary=x;incrementalSpec=V0.2", exec.IncrementalV02, true, false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(http.MethodPost, "/graphql", nil)
		if tc.accept != "" {
			r.Header.Set("Accept", tc.accept)
		}
		format, ok, unsupported := incrementalAccept(r)
		if format != tc.format || ok != tc.ok || unsupported != tc.unsupported {
			t.Errorf("Accept %q = (%v, %v, %v), want (%v, %v, %v)", tc.accept, format, ok, unsupported, tc.format, tc.ok, tc.unsupported)
		}
	}
}

func incrementalServer(t *testing.T, cfg config.Server) http.Handler {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, IncrementalDelivery: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	ex := exec.New(nil, built, nil, nil, exec.Options{Incremental: exec.IncrementalOptions{Enabled: true}})
	cfg.MaxBodyBytes = 1 << 20
	cfg.IncrementalDelivery = true
	s, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler()
}

func postAccept(h http.Handler, accept, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "gzip")
	h.ServeHTTP(rec, req)
	return rec
}

// TestIncrementalSingleResult checks that an operation with nothing to
// deliver incrementally is answered as plain, uncompressed JSON when the
// client accepts multipart/mixed.
func TestIncrementalSingleResult(t *testing.T) {
	h := incrementalServer(t, config.Server{RequestTimeout: time.Second, Compression: true})
	rec := postAccept(h, "multipart/mixed;incrementalSpec=v0.2, application/json", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || rec.Header().Get("Content-Encoding") != "" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
	if got := rec.Body.String(); got != `{"data":{"__typename":"Query"}}`+"\n" {
		t.Fatalf("body = %q", got)
	}
	rec = postAccept(h, "multipart/mixed", `{"query":"{ userById(id: 1) { email @stream } }"}`)
	if !strings.Contains(rec.Body.String(), "non-list field") {
		t.Fatalf("stream on a scalar: %s", rec.Body.String())
	}
}

func TestIncrementalNotAcceptable(t *testing.T) {
	h := incrementalServer(t, config.Server{})
	rec := postAccept(h, "multipart/mixed;deferSpec=20200101", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusNotAcceptable {
		t.Fatalf("got %d, want 406: %s", rec.Code, rec.Body.String())
	}
}

// TestIncrementalOffKeepsJSON checks that with the option off a client
// accepting multipart/mixed still gets the buffered (gzip) JSON response.
func TestIncrementalOffKeepsJSON(t *testing.T) {
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Server{MaxBodyBytes: 1 << 20, Compression: true}, testAuth(), exec.New(nil, built, nil, nil, exec.Options{}), built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	rec := postAccept(s.Handler(), "multipart/mixed;deferSpec=20200101", `{"query":"{ __typename }"}`)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("got %d %v", rec.Code, rec.Header())
	}
}
