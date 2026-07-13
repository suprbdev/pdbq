package server

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

const testSecret = "test-secret"

func testAuth() *Authenticator {
	return NewAuthenticator(config.RLS{
		Enabled:       true,
		AnonymousRole: "anonymous",
		RoleClaim:     "role",
		Auth:          config.Auth{Mode: "jwt", JWTSecret: testSecret},
	})
}

func signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testSecret))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestJWTWithoutExpRejected(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{"role": "app_user"}))
	if _, _, err := a.Authenticate(r); err == nil {
		t.Fatal("token without exp accepted; must be rejected")
	}
}

func TestJWTExpiredRejected(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(-time.Minute).Unix(),
	}))
	if _, _, err := a.Authenticate(r); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestJWTValidExpAccepted(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
		"role": "app_user",
		"exp":  time.Now().Add(time.Hour).Unix(),
	}))
	claims, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if role != "app_user" {
		t.Fatalf("role = %q, want app_user", role)
	}
	if claims["role"] != "app_user" {
		t.Fatalf("claims missing role: %v", claims)
	}
}

func TestAllowedRoles(t *testing.T) {
	newAuth := func(allowed ...string) *Authenticator {
		return NewAuthenticator(config.RLS{
			Enabled:       true,
			AnonymousRole: "anonymous",
			DefaultRole:   "app_default",
			RoleClaim:     "role",
			AllowedRoles:  allowed,
			Auth:          config.Auth{Mode: "jwt", JWTSecret: testSecret},
		})
	}
	cases := []struct {
		name    string
		allowed []string
		role    string
		wantErr bool
	}{
		{"empty list allows any", nil, "app_user", false},
		{"listed role allowed", []string{"app_user"}, "app_user", false},
		{"unlisted role rejected", []string{"app_user"}, "app_admin", true},
		{"default role always allowed", []string{"app_user"}, "app_default", false},
		{"anonymous role always allowed", []string{"app_user"}, "anonymous", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newAuth(tc.allowed...)
			r := httptest.NewRequest("POST", "/graphql", nil)
			r.Header.Set("Authorization", "Bearer "+signToken(t, jwt.MapClaims{
				"role": tc.role,
				"exp":  time.Now().Add(time.Hour).Unix(),
			}))
			_, role, err := a.Authenticate(r)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("role %q accepted, want rejection", tc.role)
				}
				return
			}
			if err != nil {
				t.Fatalf("role %q rejected: %v", tc.role, err)
			}
			if role != tc.role {
				t.Fatalf("role = %q, want %q", role, tc.role)
			}
		})
	}
}

func TestNoTokenIsAnonymous(t *testing.T) {
	a := testAuth()
	r := httptest.NewRequest("POST", "/graphql", nil)
	_, role, err := a.Authenticate(r)
	if err != nil {
		t.Fatal(err)
	}
	if role != "anonymous" {
		t.Fatalf("role = %q, want anonymous", role)
	}
}
