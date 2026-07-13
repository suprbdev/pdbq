package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

// Authenticator turns an HTTP request into (claims, role) per the configured
// claim source: verified JWT, trusted gateway headers, or none.
type Authenticator struct {
	cfg config.RLS
	// jwks verifies asymmetric tokens when rls.auth.jwks_url is set.
	jwks *jwksCache
}

func NewAuthenticator(cfg config.RLS) *Authenticator {
	a := &Authenticator{cfg: cfg}
	if cfg.Auth.JWKSURL != "" {
		a.jwks = newJWKSCache(cfg.Auth.JWKSURL, cfg.Auth.JWKSCacheTTL)
	}
	return a
}

// Authenticate never fails open: a present-but-invalid credential is an
// error; an absent credential yields the anonymous role.
func (a *Authenticator) Authenticate(r *http.Request) (map[string]any, string, error) {
	if !a.cfg.Enabled {
		return nil, "", nil
	}
	switch a.cfg.Auth.Mode {
	case "jwt":
		return a.fromJWT(r)
	case "headers":
		return a.fromHeaders(r)
	default:
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
}

func (a *Authenticator) fromJWT(r *http.Request) (map[string]any, string, error) {
	authz := r.Header.Get("Authorization")
	if authz == "" {
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
	tokenStr, ok := strings.CutPrefix(authz, "Bearer ")
	if !ok {
		return nil, "", fmt.Errorf("malformed Authorization header")
	}
	claims := jwt.MapClaims{}
	// WithExpirationRequired: a token without an exp claim would otherwise
	// never expire — stolen tokens would stay valid until secret rotation.
	methods := []string{"HS256", "HS384", "HS512"}
	keyfunc := func(t *jwt.Token) (any, error) {
		return []byte(a.cfg.Auth.JWTSecret), nil
	}
	if a.jwks != nil {
		// Asymmetric verification: the algorithm allowlist swaps entirely so
		// an attacker cannot downgrade to HMAC-with-public-key.
		methods = []string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}
		keyfunc = func(t *jwt.Token) (any, error) {
			kid, _ := t.Header["kid"].(string)
			return a.jwks.key(kid)
		}
	}
	parserOpts := []jwt.ParserOption{
		jwt.WithValidMethods(methods),
		jwt.WithExpirationRequired(),
	}
	if a.cfg.Auth.JWTIssuer != "" {
		parserOpts = append(parserOpts, jwt.WithIssuer(a.cfg.Auth.JWTIssuer))
	}
	if a.cfg.Auth.JWTAudience != "" {
		parserOpts = append(parserOpts, jwt.WithAudience(a.cfg.Auth.JWTAudience))
	}
	_, err := jwt.ParseWithClaims(tokenStr, claims, keyfunc, parserOpts...)
	if err != nil {
		return nil, "", fmt.Errorf("invalid token: %w", err)
	}
	return a.resolve(map[string]any(claims))
}

func (a *Authenticator) fromHeaders(r *http.Request) (map[string]any, string, error) {
	claims := map[string]any{}
	prefix := http.CanonicalHeaderKey(a.cfg.Auth.HeaderPrefix)
	for name, vals := range r.Header {
		if strings.HasPrefix(name, prefix) && len(vals) > 0 {
			key := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, prefix), "-", "_"))
			claims[key] = vals[0]
		}
	}
	if len(claims) == 0 {
		return map[string]any{}, a.cfg.AnonymousRole, nil
	}
	return a.resolve(claims)
}

// resolve extracts the database role from claims (falling back to
// default_role, then anonymous) and enforces the allowed_roles policy on
// claim-supplied roles.
func (a *Authenticator) resolve(claims map[string]any) (map[string]any, string, error) {
	role := a.cfg.DefaultRole
	fromClaim := false
	if v, ok := claims[a.cfg.RoleClaim]; ok {
		if s, ok := v.(string); ok && s != "" {
			role = s
			fromClaim = true
		}
	}
	if role == "" {
		role = a.cfg.AnonymousRole
	}
	if fromClaim && !a.roleAllowed(role) {
		return nil, "", fmt.Errorf("role %q is not allowed", role)
	}
	return claims, role, nil
}

// roleAllowed applies rls.allowed_roles: empty list allows everything;
// otherwise the role must be listed, or be the configured default/anonymous
// role (the operator chose those explicitly).
func (a *Authenticator) roleAllowed(role string) bool {
	if len(a.cfg.AllowedRoles) == 0 || role == a.cfg.DefaultRole || role == a.cfg.AnonymousRole {
		return true
	}
	for _, r := range a.cfg.AllowedRoles {
		if r == role {
			return true
		}
	}
	return false
}
