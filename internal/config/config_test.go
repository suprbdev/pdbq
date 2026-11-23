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
		"readyz":        func(c *Config) { c.Server.ReadyzTimeout = 0 },
		"doc cache":     func(c *Config) { c.Server.DocumentCacheSize = -1 },
		"anon role":     func(c *Config) { c.RLS.AnonymousRole = "" },
		"watch channel": func(c *Config) { c.Watch.Enabled = true; c.Watch.Channel = "bad$$chan" },
		"trusted proxy": func(c *Config) { c.RLS.Auth.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} },
		"metrics path":  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "metrics" },
		"metrics route": func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/graphql" },
		"metrics addr":  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Addr = c.Server.Addr },
	}
	for name, mutate := range cases {
		if err := bad(mutate); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoadRejectsUnsafeRLSSettings(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       []string
	}{
		{"allowed role none", "rls:\n  allowed_roles: [app_user, none]\n", []string{"rls.allowed_roles", `"none"`}},
		{"allowed role NONE", "rls:\n  allowed_roles: [NONE]\n", []string{"rls.allowed_roles", `"NONE"`}},
		{"allowed role public", "rls:\n  allowed_roles: [public]\n", []string{"rls.allowed_roles", `"public"`}},
		{"allowed role pg_", "rls:\n  allowed_roles: [pg_read_all_data]\n", []string{"rls.allowed_roles", `"pg_read_all_data"`}},
		{"anonymous role none", "rls:\n  anonymous_role: none\n", []string{"rls.anonymous_role", `"none"`}},
		{"default role pg_", "rls:\n  default_role: pg_write_all_data\n", []string{"rls.default_role", `"pg_write_all_data"`}},
		{"claims prefix", "rls:\n  claims_prefix: plpgsql.claims\n", []string{"rls.claims_prefix", `"plpgsql"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pdbq.yaml")
			yaml := "database:\n  url: \"postgres://file/db\"\n" + tc.yaml + "  auth:\n    jwt_secret: s\n"
			if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path, nil)
			if err == nil {
				t.Fatal("Load accepted the config, want a validation error")
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %s", err, w)
				}
			}
		})
	}
}

func TestLoadRejectsAllowedRoleNoneFromEnv(t *testing.T) {
	t.Setenv("PDBQ_RLS_AUTH_JWT__SECRET", "s")
	t.Setenv("PDBQ_RLS_ALLOWED__ROLES", "app_user,none")
	_, err := Load("", nil)
	if err == nil || !strings.Contains(err.Error(), `rls.allowed_roles: "none"`) {
		t.Fatalf("Load error = %v, want rls.allowed_roles: \"none\" rejected", err)
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

func TestExposePartitionsFromEnv(t *testing.T) {
	if DefaultConfig().Schema.ExposePartitions {
		t.Fatal("schema.expose_partitions defaults to true, want false")
	}
	t.Setenv("PDBQ_SCHEMA_EXPOSE__PARTITIONS", "true")
	t.Setenv("PDBQ_RLS_ENABLED", "false")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Schema.ExposePartitions || !cfg.Schema.Functions || len(cfg.Schema.Schemas) != 1 {
		t.Errorf("schema section = %+v, want expose_partitions set and the defaults kept", cfg.Schema)
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

func TestValidateMetrics(t *testing.T) {
	cfg := DefaultConfig()
	cfg.RLS.Auth.JWTSecret = "s"
	cfg.Metrics.Path = "/graphql"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("disabled metrics must not be validated: %v", err)
	}
	cfg.Metrics.Enabled = true
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), `metrics.path: "/graphql" is a pdbq route`) {
		t.Fatalf("err = %v, want the route collision", err)
	}
	cfg.Metrics.Addr = ":9090"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("a route path on a separate listener must be allowed: %v", err)
	}
	cfg.Metrics = DefaultConfig().Metrics
	cfg.Metrics.Enabled = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("enabled defaults invalid: %v", err)
	}
}

func TestMetricsFromEnv(t *testing.T) {
	t.Setenv("PDBQ_METRICS_ENABLED", "true")
	t.Setenv("PDBQ_METRICS_ADDR", ":9090")
	t.Setenv("PDBQ_RLS_AUTH_JWT__SECRET", "s")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Metrics.Enabled || cfg.Metrics.Addr != ":9090" || cfg.Metrics.Path != "/metrics" {
		t.Fatalf("metrics = %+v", cfg.Metrics)
	}
}
