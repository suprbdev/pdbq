package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/auth"
)

func writeConfig(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pdbq.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func wantLoadError(t *testing.T, path string, want ...string) {
	t.Helper()
	_, err := Load(path, nil)
	if err == nil {
		t.Fatal("Load accepted the config, want an error")
	}
	for _, w := range want {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not mention %q", err, w)
		}
	}
}

func TestDefaultsNeedCredentialsOrModeNone(t *testing.T) {
	cfg := DefaultConfig()
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "auth.jwt.secret, auth.jwt.secret_file or auth.jwt.jwks_url is required") {
		t.Fatalf("defaults without a secret: err = %v, want the missing secret", err)
	}
	for name, mutate := range map[string]func(*Config){
		"secret":         func(c *Config) { c.Auth.JWT.Secret = "s" },
		"jwks":           func(c *Config) { c.Auth.JWT.JWKSURL = "https://issuer.example/jwks.json" },
		"none anonymous": func(c *Config) { c.Auth.Mode = auth.ModeNone; c.Roles.Anonymous = "anonymous" },
		"none no switch": func(c *Config) { c.Auth.Mode = auth.ModeNone; c.Roles.Switch = false },
	} {
		cfg := DefaultConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestDefaultIdentity(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Auth.Mode != auth.ModeJWT || cfg.Auth.Headers.Prefix != "X-Pdbq-Claim-" || cfg.Auth.JWT.RoleClaimPath != "role" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
	if !cfg.Roles.Switch || cfg.Roles.Anonymous != "" {
		t.Errorf("roles = %+v, want switch on and no anonymous role", cfg.Roles)
	}
	if cfg.Claims.Prefix != "jwt.claims" || cfg.Claims.JSONSetting != "" {
		t.Errorf("claims = %+v", cfg.Claims)
	}
}

func TestStatementTimeoutBelowRequestTimeout(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Database.StatementTimeout != 25*time.Second || cfg.Database.StatementTimeout >= cfg.Server.RequestTimeout {
		t.Errorf("statement_timeout %v, request_timeout %v", cfg.Database.StatementTimeout, cfg.Server.RequestTimeout)
	}
}

func TestLoadRejectsUnknownKeys(t *testing.T) {
	t.Run("typo", func(t *testing.T) {
		wantLoadError(t, writeConfig(t, "limits:\n  max_dpeth: 3\n"), "unknown key limits.max_dpeth", "did you mean limits.max_depth?")
	})
	t.Run("rls section", func(t *testing.T) {
		wantLoadError(t, writeConfig(t, "rls:\n  enabled: false\n  anonymous_role: web\n  auth:\n    jwt_secret: s\n"),
			"unknown key rls.enabled: moved to roles.switch",
			"unknown key rls.anonymous_role: moved to roles.anonymous",
			"unknown key rls.auth.jwt_secret: moved to auth.jwt.secret")
	})
	t.Run("rls env", func(t *testing.T) {
		t.Setenv("PDBQ_RLS_AUTH_JWT__SECRET", "s")
		wantLoadError(t, "", "PDBQ_RLS_AUTH_JWT__SECRET", "moved to PDBQ_AUTH_JWT_SECRET")
	})
	t.Run("env typo", func(t *testing.T) {
		t.Setenv("PDBQ_AUTH_JWT_SECRT", "s")
		wantLoadError(t, "", "PDBQ_AUTH_JWT_SECRT", "did you mean PDBQ_AUTH_JWT_SECRET?")
	})
	t.Run("kebab plugin settings", func(t *testing.T) {
		wantLoadError(t, writeConfig(t, "plugins:\n  settings:\n    advanced-filters:\n      relations: false\n    nested-mutations:\n      max_depth: 2\n"),
			"moved to plugins.settings.advanced_filters.relations",
			"moved to plugins.settings.nested_mutations.max_depth")
	})
	t.Run("unknown plugin setting", func(t *testing.T) {
		wantLoadError(t, writeConfig(t, "plugins:\n  settings:\n    postgis:\n      typed_geometri: true\n"),
			"did you mean plugins.settings.postgis.typed_geometry?")
	})
}

func TestUnrelatedEnvIgnored(t *testing.T) {
	t.Setenv("PDBQ_TEST_DATABASE_URL", "postgres://elsewhere/db")
	t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
	if _, err := Load("", nil); err != nil {
		t.Fatal(err)
	}
}

func TestPluginSettingsFromEnv(t *testing.T) {
	t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
	t.Setenv("PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS", "false")
	t.Setenv("PDBQ_AGGREGATES_ENABLED", "true")
	t.Setenv("PDBQ_AGGREGATES_SCAN__COST", "7")
	t.Setenv("PDBQ_PLUGINS_SETTINGS_NESTED__MUTATIONS_MAX__DEPTH", "2")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	s := cfg.Plugins.Settings
	if s.AdvancedFilters.Relations || !s.AdvancedFilters.Computed {
		t.Errorf("advanced_filters = %+v, want relations off and the defaults kept", s.AdvancedFilters)
	}
	if a := cfg.Aggregates; !a.Enabled || a.ScanCost != 7 || !a.Grouped {
		t.Errorf("aggregates = %+v", a)
	}
	if s.NestedMutations.MaxDepth != 2 || s.NestedMutations.MaxOperations != 50 {
		t.Errorf("nested_mutations = %+v", s.NestedMutations)
	}
}

func TestPluginsDisabledNames(t *testing.T) {
	wantLoadError(t, writeConfig(t, "auth:\n  jwt:\n    secret: s\nplugins:\n  disabled: [smart-comments, nope]\n"),
		`plugins.disabled: unknown plugin "smart-comments", did you mean "smart_comments"?`,
		`plugins.disabled: unknown plugin "nope"`)
	cfg, err := Load(writeConfig(t, "auth:\n  jwt:\n    secret: s\nplugins:\n  disabled: [simple_names, aggregates]\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if d := cfg.DisabledPlugins(); !d["simple_names"] || !d["aggregates"] {
		t.Errorf("disabled = %v", d)
	}
}

func TestLogFormatValidated(t *testing.T) {
	wantLoadError(t, writeConfig(t, "auth:\n  jwt:\n    secret: s\nlog:\n  format: logfmt\n"), `log.format: "logfmt" is not 'text' or 'json'`)
}

func TestStarterLoads(t *testing.T) {
	cfg, err := Load(writeConfig(t, StarterYAML), nil)
	if err != nil {
		t.Fatalf("starter config: %v", err)
	}
	if cfg.Auth.Mode != auth.ModeNone || cfg.Roles.Switch {
		t.Errorf("starter identity = %+v %+v, want mode none and no role switch", cfg.Auth, cfg.Roles)
	}
}

func TestConnectionIdentityOnDefaults(t *testing.T) {
	cfg, err := LoadUnvalidated("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Validate() == nil {
		t.Fatal("defaults validated without credentials")
	}
	cfg.ConnectionIdentity()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("connection identity on defaults: %v", err)
	}
	if cfg.Auth.Mode != auth.ModeNone || cfg.Roles.Switch {
		t.Errorf("identity = %+v %+v", cfg.Auth, cfg.Roles)
	}
}

func TestLoadUnvalidatedStillStrict(t *testing.T) {
	if _, err := LoadUnvalidated(writeConfig(t, "rls:\n  enabled: false\n"), nil); err == nil || !strings.Contains(err.Error(), "moved to roles.switch") {
		t.Fatalf("err = %v, want the moved key", err)
	}
}

func TestBuiltinPluginNamesSnakeCase(t *testing.T) {
	for _, n := range BuiltinPluginNames {
		if strings.Contains(n, "-") {
			t.Errorf("plugin name %q is not snake_case", n)
		}
	}
}

func TestExampleYAMLLoadsStrictly(t *testing.T) {
	cfg, err := LoadUnvalidated(writeConfig(t, ExampleYAML()), nil)
	if err != nil {
		t.Fatalf("reference config rejected: %v", err)
	}
	if cfg.Plugins.Settings != DefaultPluginSettings() {
		t.Errorf("plugin settings = %+v, want the defaults", cfg.Plugins.Settings)
	}
	def := DefaultConfig()
	if cfg.Limits != def.Limits || cfg.Server.DocumentCache != def.Server.DocumentCache || cfg.Incremental != def.Incremental ||
		cfg.Live != def.Live || cfg.PersistedQueries != def.PersistedQueries || cfg.TX != def.TX || cfg.Aggregates != def.Aggregates {
		t.Errorf("reference config does not load the defaults: %+v", cfg)
	}
}
