package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsValid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RLS.Auth.JWTSecret = "test-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestValidateAcceptsStrictErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RLS.Auth.JWTSecret = "s"
	cfg.Errors.Detail = "strict"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("strict rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	bad := func(mutate func(*Config)) error {
		cfg := DefaultConfig()
		cfg.RLS.Auth.JWTSecret = "s"
		mutate(&cfg)
		return cfg.Validate()
	}
	cases := map[string]func(*Config){
		"errors.detail": func(c *Config) { c.Errors.Detail = "verbose" },
		"auth.mode":     func(c *Config) { c.RLS.Auth.Mode = "oauth" },
		"jwt secret":    func(c *Config) { c.RLS.Auth.JWTSecret = "" },
		"isolation":     func(c *Config) { c.TX.Isolation = "chaos" },
		"watch+cache":   func(c *Config) { c.Watch.Enabled = true; c.Schema.CachePath = "x" },
		"no schemas":    func(c *Config) { c.Schema.Schemas = nil },
		"log level":     func(c *Config) { c.Log.Level = "loud" },
		"max page size": func(c *Config) { c.Server.MaxPageSize = 0 },
		"anon role":     func(c *Config) { c.RLS.AnonymousRole = "" },
		"watch channel": func(c *Config) { c.Watch.Enabled = true; c.Watch.Channel = "bad$$chan" },
		"trusted proxy": func(c *Config) { c.RLS.Auth.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} },
	}
	for name, mutate := range cases {
		if err := bad(mutate); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoadLayering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pdbq.yaml")
	yaml := `
database:
  url: "postgres://file/db"
server:
  addr: ":9999"
rls:
  enabled: false
`
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PDBQ_SERVER_ADDR", ":7777") // env overrides file
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://file/db" {
		t.Errorf("file value lost: %q", cfg.Database.URL)
	}
	if cfg.Server.Addr != ":7777" {
		t.Errorf("env did not override file: %q", cfg.Server.Addr)
	}
	if cfg.Server.MaxDepth != 15 {
		t.Errorf("default lost: %d", cfg.Server.MaxDepth)
	}
}

func TestExampleYAMLCoversEverything(t *testing.T) {
	out := ExampleYAML()
	for _, key := range []string{
		"database:", "url:", "server:", "max_depth:", "filters:", "indexed_only:",
		"rls:", "anonymous_role:", "transactions:", "watch:", "errors:", "plugins:", "log:",
	} {
		if !strings.Contains(out, key) {
			t.Errorf("example yaml missing %q", key)
		}
	}
	// Every doc tag should surface as a comment.
	if !strings.Contains(out, "# Only allow filtering on columns covered by an index") {
		t.Error("doc comments not rendered")
	}
}
