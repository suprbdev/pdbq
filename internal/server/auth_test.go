package server

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

func TestReservedRoleClaimRejected(t *testing.T) {
	a := config.DefaultConfig().Auth
	a.Mode = "headers"
	a.Headers.TrustedProxies = []string{"192.0.2.0/24"}
	authn, err := NewAuthenticator(a, config.DefaultConfig().Roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(config.Config{Limits: config.Limits{MaxBodyBytes: 1 << 20}}, authn, nil, "type Query { ok: Boolean }", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	for _, role := range []string{"none", "NONE", "public", "pg_read_all_data", "bad-role"} {
		req := httptest.NewRequest("POST", "/graphql", strings.NewReader(`{"query":"{ ok }"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pdbq-Claim-Role", role)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"code":"role_not_allowed"`) {
			t.Errorf("role %q: %d %s, want 403 role_not_allowed", role, rec.Code, rec.Body)
		}
	}
}
