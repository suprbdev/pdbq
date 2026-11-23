package e2e

import (
	"testing"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/server"
)

// noIdentity runs every request as the connection role without
// credentials.
func noIdentity(c *config.Config) {
	c.Auth.Mode = auth.ModeNone
	c.Roles.Switch = false
}

// headersIdentity takes claims from headers sent over loopback, with the
// anonymous role for requests without them.
func headersIdentity(c *config.Config) {
	c.Auth.Mode = auth.ModeHeaders
	c.Auth.Headers.TrustedProxies = []string{"127.0.0.0/8", "::1"}
	c.Roles.Switch = true
	c.Roles.Anonymous = "anonymous"
}

// jwtIdentity verifies HS256 tokens signed with secret, with the anonymous
// role for requests without one.
func jwtIdentity(c *config.Config, secret string) {
	c.Auth.Mode = auth.ModeJWT
	c.Auth.JWT.Secret = secret
	c.Roles.Switch = true
	c.Roles.Anonymous = "anonymous"
}

func mustAuthenticator(t *testing.T, cfg config.Config) *server.Authenticator {
	t.Helper()
	authn, err := server.NewAuthenticator(cfg.Auth, cfg.Roles, nil)
	if err != nil {
		t.Fatal(err)
	}
	return authn
}
