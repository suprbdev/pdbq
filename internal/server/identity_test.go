package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

const identitySecret = "identity-secret"

var errCaptured = errors.New("captured")

// captureHook records the identity of the operation and stops it before
// it reaches the database.
type captureHook struct {
	role   string
	claims map[string]any
	calls  int
}

func (h *captureHook) BeforeOperation(ctx context.Context, op *exec.Operation) (context.Context, error) {
	h.calls++
	h.role, h.claims = op.Role, op.Claims
	return ctx, errCaptured
}

func (h *captureHook) AfterOperation(context.Context, *exec.Operation, *exec.Result) {}

func identityServer(t *testing.T, a config.Auth, r config.Roles, srv config.Config) (http.Handler, *captureHook) {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	hook := &captureHook{}
	ex := exec.New(nil, built, nil, []exec.RequestHook{hook}, exec.Options{})
	authn, err := NewAuthenticator(a, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv.Limits.MaxBodyBytes = 1 << 20
	s, err := New(srv, authn, ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), hook
}

func jwtAuth() config.Auth {
	a := config.DefaultConfig().Auth
	a.JWT.Secret = identitySecret
	return a
}

func signedToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(identitySecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func postIdentity(h http.Handler, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(`{"query":"{ __typename }"}`))
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAnonymousRefusedByDefault(t *testing.T) {
	h, hook := identityServer(t, jwtAuth(), config.DefaultConfig().Roles, config.Config{})
	rec := postIdentity(h, nil)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("status %d, WWW-Authenticate %q, want 401 Bearer", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if !strings.Contains(rec.Body.String(), `"errors":[{"message":"authentication required"}]`) {
		t.Errorf("body = %s", rec.Body)
	}
	if hook.calls != 0 {
		t.Error("refused request reached the executor")
	}
}

func TestAnonymousRoleWhenConfigured(t *testing.T) {
	r := config.DefaultConfig().Roles
	r.Anonymous = "anonymous"
	h, hook := identityServer(t, jwtAuth(), r, config.Config{})
	postIdentity(h, nil)
	if hook.calls != 1 || hook.role != "anonymous" {
		t.Fatalf("calls %d, role %q, want the anonymous role", hook.calls, hook.role)
	}
}

func TestInvalidTokenChallenge(t *testing.T) {
	h, _ := identityServer(t, jwtAuth(), config.DefaultConfig().Roles, config.Config{})
	rec := postIdentity(h, http.Header{"Authorization": {"Bearer not-a-token"}})
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("status %d, WWW-Authenticate %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestRoleNotAllowedChallenge(t *testing.T) {
	r := config.DefaultConfig().Roles
	r.Allowed = []string{"app_user"}
	h, hook := identityServer(t, jwtAuth(), r, config.Config{})
	rec := postIdentity(h, http.Header{"Authorization": {"Bearer " + signedToken(t, jwt.MapClaims{"role": "admin"})}})
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") != `Bearer error="insufficient_scope"` {
		t.Fatalf("status %d, WWW-Authenticate %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
	if hook.calls != 0 {
		t.Error("refused request reached the executor")
	}
}

func TestBearerSchemeCaseInsensitive(t *testing.T) {
	h, hook := identityServer(t, jwtAuth(), config.DefaultConfig().Roles, config.Config{})
	tok := signedToken(t, jwt.MapClaims{"role": "app_user", "user_id": "7"})
	for _, scheme := range []string{"bearer", "BEARER", "Bearer"} {
		hook.calls = 0
		rec := postIdentity(h, http.Header{"Authorization": {scheme + " " + tok}})
		if hook.calls != 1 || hook.role != "app_user" || hook.claims["user_id"] != "7" {
			t.Errorf("%s: status %d %s, role %q, claims %v", scheme, rec.Code, rec.Body, hook.role, hook.claims)
		}
	}
}

func TestRoleSwitchOffRunsAsConnectionRole(t *testing.T) {
	r := config.DefaultConfig().Roles
	r.Switch = false
	h, hook := identityServer(t, jwtAuth(), r, config.Config{})
	postIdentity(h, http.Header{"Authorization": {"Bearer " + signedToken(t, jwt.MapClaims{"role": "app_user", "user_id": "7"})}})
	if hook.calls != 1 || hook.role != "" || hook.claims["user_id"] != "7" {
		t.Fatalf("calls %d, role %q, claims %v, want the connection role with the claims", hook.calls, hook.role, hook.claims)
	}
	rec := postIdentity(h, http.Header{"Authorization": {"Bearer forged"}})
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("invalid token with roles.switch off: status %d, want 401", rec.Code)
	}
}

func TestHeadersModeRequiresTrustedProxies(t *testing.T) {
	a := config.DefaultConfig().Auth
	a.Mode = "headers"
	if _, err := NewAuthenticator(a, config.DefaultConfig().Roles, nil); err == nil {
		t.Fatal("headers mode without trusted proxies accepted")
	}
}

func TestHeadersModeUntrustedPeer(t *testing.T) {
	a := config.DefaultConfig().Auth
	a.Mode = "headers"
	a.Headers.TrustedProxies = []string{"10.0.0.1"}
	h, hook := identityServer(t, a, config.DefaultConfig().Roles, config.Config{})
	rec := postIdentity(h, http.Header{"X-Pdbq-Claim-Role": {"app_user"}})
	if rec.Code != http.StatusUnauthorized || hook.calls != 0 {
		t.Fatalf("claim headers from an untrusted peer: status %d, executor calls %d", rec.Code, hook.calls)
	}
}

func TestCORSHeaders(t *testing.T) {
	r := config.DefaultConfig().Roles
	r.Anonymous = "anonymous"
	h, _ := identityServer(t, jwtAuth(), r, config.Config{Server: config.Server{CORSOrigins: []string{"https://app.example"}, Compression: true}})
	rec := postIdentity(h, http.Header{"Origin": {"https://app.example"}, "Accept-Encoding": {"gzip"}})
	vary := strings.Join(rec.Header().Values("Vary"), ", ")
	if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Origin and Accept-Encoding", vary)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != "WWW-Authenticate" {
		t.Errorf("Access-Control-Expose-Headers = %q", got)
	}

	rec = postIdentity(h, http.Header{"Origin": {"https://evil.example"}})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" || rec.Header().Get("Access-Control-Expose-Headers") != "" {
		t.Errorf("disallowed origin got CORS headers: %v", rec.Header())
	}

	req := httptest.NewRequest(http.MethodOptions, "/graphql", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	pre := httptest.NewRecorder()
	h.ServeHTTP(pre, req)
	if pre.Code != http.StatusNoContent || pre.Header().Get("Access-Control-Allow-Methods") != "POST, GET, OPTIONS" {
		t.Errorf("preflight: %d %v", pre.Code, pre.Header())
	}
}
