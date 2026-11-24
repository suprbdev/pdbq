package server

import (
	"compress/gzip"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

func testAuth() *Authenticator {
	a, r := config.DefaultConfig().Auth, config.DefaultConfig().Roles
	a.JWT.Secret = "test-secret"
	r.Anonymous, r.Switch = "anonymous", false
	authn, err := NewAuthenticator(a, r, nil)
	if err != nil {
		panic(err)
	}
	return authn
}

func testServer(cfg config.Config) *Server {
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
	h := testServer(config.Config{}).Handler()
	if rec := get(t, h, "/schema.graphql"); rec.Code != http.StatusNotFound {
		t.Fatalf("/schema.graphql = %d, want 404 when introspection is off", rec.Code)
	}
}

func TestSDLServedWhenExposed(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{Introspection: true}}).Handler()
	rec := get(t, h, "/schema.graphql")
	if rec.Code != http.StatusOK {
		t.Fatalf("/schema.graphql = %d, want 200 when introspection is on", rec.Code)
	}
	if rec.Body.String() != "type Query { ok: Boolean }" {
		t.Fatalf("unexpected SDL body: %q", rec.Body.String())
	}
}

// TestSDLRequiresCredentialsLikeIntrospection answers /schema.graphql 401
// without credentials when roles.anonymous is empty, as /graphql answers an
// introspection query, and serves it to an authenticated request.
func TestSDLRequiresCredentialsLikeIntrospection(t *testing.T) {
	roles := config.DefaultConfig().Roles
	roles.Anonymous = ""
	authn, err := NewAuthenticator(jwtAuth(), roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{Server: config.Server{Introspection: true}}, authn, nil, "type Query { ok: Boolean }", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for name, header := range map[string]string{"no credentials": "", "invalid token": "Bearer nope"} {
		req := httptest.NewRequest("GET", "/schema.graphql", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: /schema.graphql = %d (WWW-Authenticate %q), want 401 with a challenge", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	}
	req := httptest.NewRequest("GET", "/schema.graphql", nil)
	req.Header.Set("Authorization", "Bearer "+signedToken(t, jwt.MapClaims{"role": "app_user"}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "type Query { ok: Boolean }" {
		t.Errorf("authenticated /schema.graphql = %d %q, want the SDL", rec.Code, rec.Body.String())
	}
}

func TestGraphiQLHiddenByDefault(t *testing.T) {
	h := testServer(config.Config{}).Handler()
	if rec := get(t, h, "/"); rec.Code != http.StatusNotFound {
		t.Fatalf("/ = %d, want 404 when graphiql off", rec.Code)
	}
}

func TestGraphiQLServedWhenEnabled(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{GraphiQL: true}}).Handler()
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ = %d, want 200 when graphiql on", rec.Code)
	}
}

func TestCORSHeadersAbsentByDefault(t *testing.T) {
	h := testServer(config.Config{}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty when cors_origins unset", got)
	}
}

func TestCORSAllowedOrigin(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{CORSOrigins: []string{"https://example.com"}}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want https://example.com", got)
	}
}

func TestCORSDisallowedOrigin(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{CORSOrigins: []string{"https://example.com"}}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed origin", got)
	}
}

func TestCORSWildcard(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{CORSOrigins: []string{"*"}}}).Handler()
	req := httptest.NewRequest("GET", "/healthz", nil)
	req.Header.Set("Origin", "https://anything.example")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://anything.example" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want echoed origin for wildcard config", got)
	}
}

func TestCORSPreflight(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{CORSOrigins: []string{"https://example.com"}}}).Handler()
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
	h := testServer(config.Config{}).Handler()
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
	h := testServer(config.Config{Server: config.Server{Compression: true}}).Handler()
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
	h := testServer(config.Config{Server: config.Server{Compression: true}}).Handler()
	rec := get(t, h, "/healthz")
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want empty when client does not accept gzip", got)
	}
	if rec.Body.String() != `{"status":"ok"}` {
		t.Fatalf("unexpected body: %q", rec.Body.String())
	}
}

func TestGzipSkipsCORSPreflight(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{Compression: true, CORSOrigins: []string{"*"}}}).Handler()
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

func TestCORSPreflightDisallowedOrigin(t *testing.T) {
	h := testServer(config.Config{Server: config.Server{CORSOrigins: []string{"https://example.com"}}}).Handler()
	req := httptest.NewRequest("OPTIONS", "/graphql", nil)
	req.Header.Set("Origin", "https://evil.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty for disallowed preflight origin", got)
	}
}
