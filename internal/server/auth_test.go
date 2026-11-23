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
	rls := config.DefaultConfig().RLS
	rls.Auth.Mode = "headers"
	authn := NewAuthenticator(rls)
	s, err := New(config.Server{MaxBodyBytes: 1 << 20}, authn, nil, "type Query { ok: Boolean }", slog.Default())
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
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "is not allowed") {
			t.Errorf("role %q: %d %s, want 401 role not allowed", role, rec.Code, rec.Body)
		}
	}
}
