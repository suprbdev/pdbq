package server

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

func testServer(cfg config.Server) *Server {
	s, err := New(cfg, testAuth(), nil, "type Query { ok: Boolean }", slog.Default())
	if err != nil {
		panic(err)
	}
	return s
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec
}

func TestSDLHiddenByDefault(t *testing.T) {
	h := testServer(config.Server{}).Handler()
	if rec := get(t, h, "/schema.graphql"); rec.Code != http.StatusNotFound {
		t.Fatalf("/schema.graphql = %d, want 404 when expose_schema off", rec.Code)
	}
}

func TestSDLServedWhenExposed(t *testing.T) {
	h := testServer(config.Server{ExposeSchema: true}).Handler()
	rec := get(t, h, "/schema.graphql")
	if rec.Code != http.StatusOK {
		t.Fatalf("/schema.graphql = %d, want 200 when expose_schema on", rec.Code)
	}
	if rec.Body.String() != "type Query { ok: Boolean }" {
		t.Fatalf("unexpected SDL body: %q", rec.Body.String())
	}
}

func TestGraphiQLHiddenByDefault(t *testing.T) {
	h := testServer(config.Server{}).Handler()
	if rec := get(t, h, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("/ = %d, want 404 when graphiql off", rec.Code)
	}
}

func TestGraphiQLServedWhenEnabled(t *testing.T) {
	h := testServer(config.Server{GraphiQL: true}).Handler()
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ = %d, want 200 when graphiql on", rec.Code)
	}
}

func TestCORSHeadersAbsentByDefault(t *testing.T) {
	h := testServer(config.Server{}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty when cors_origins unset", got)
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := testServer(config.Server{CORSOrigins: []string{"https://example.com"}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want https://example.com", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h := testServer(config.Server{CORSOrigins: []string{"https://example.com"}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
}

func TestCORSWildcard(t *testing.T) {
	h := testServer(config.Server{CORSOrigins: []string{"*"}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin for wildcard config", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	h := testServer(config.Server{CORSOrigins: []string{"https://example.com"}}).Handler()
	req := httptest.NewRequest("OPTIONS", "/graphql", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, authorization")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want https://example.com", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != "content-type, authorization" {
		t.Fatalf("Access-Control-Allow-Headers = %q, want echoed request headers", got)
	}
}

func TestGzipOffByDefault(t *testing.T) {
	h := testServer(config.Server{}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when compression off", got)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestGzipCompressesWhenAccepted(t *testing.T) {
	h := testServer(config.Server{Compression: true}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip body: %v", err)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("unexpected decompressed body: %q", body)
	}
}

func TestGzipSkippedWithoutAcceptEncoding(t *testing.T) {
	h := testServer(config.Server{Compression: true}).Handler()
	rec := get(t, h, "/healthz")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when client does not accept gzip", got)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestGzipSkipsCORSPreflight(t *testing.T) {
	h := testServer(config.Server{Compression: true, CORSOrigins: []string{"*"}}).Handler()
	req := httptest.NewRequest("OPTIONS", "/graphql", nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty on preflight", got)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("preflight body = %q, want empty", rec.Body.String())
	}
}

func TestAcceptsGzip(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{"gzip", true},
		{"gzip, deflate, br", true},
		{"deflate", false},
		{"*", true},
		{"gzip;q=0", false},
		{"gzip;q=0.5", true},
		{"identity;q=1, gzip;q=0", false},
		{"br;q=1.0, gzip;q=0.8", true},
	}
	for _, tc := range cases {
		if got := acceptsGzip(tc.header); got != tc.want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestCORSPreflightDisallowedOrigin(t *testing.T) {
	h := testServer(config.Server{CORSOrigins: []string{"https://example.com"}}).Handler()
	req := httptest.NewRequest("OPTIONS", "/graphql", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed preflight origin", got)
	}
}
