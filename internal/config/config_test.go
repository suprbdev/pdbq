package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultsValid(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "test-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
}

func TestSubscriptionSettingsIgnoredWhileOff(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "s"
	cfg.Subscriptions.Topics = []string{"bad topic"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("subscription settings checked while off: %v", err)
	}
	cfg.Subscriptions.Enabled = true
	cfg.Subscriptions.Topics = []string{"chat", "user:*"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("valid subscription settings rejected: %v", err)
	}
}

func TestValidateAcceptsStrictErrors(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "s"
	cfg.Errors.Detail = "strict"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("strict rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	bad := func(mutate func(*Config)) error {
		cfg := DefaultConfig()
		cfg.Auth.JWT.Secret = "s"
		mutate(&cfg)
		return cfg.Validate()
	}
	cases := map[string]func(*Config){
		"errors.detail": func(c *Config) { c.Errors.Detail = "verbose" },
		"auth.mode":     func(c *Config) { c.Auth.Mode = "oauth" },
		"jwt secret":    func(c *Config) { c.Auth.JWT.Secret = "" },
		"isolation":     func(c *Config) { c.TX.Isolation = "chaos" },
		"watch+cache":   func(c *Config) { c.Watch.Enabled = true; c.Schema.CachePath = "x" },
		"no schemas":    func(c *Config) { c.Schema.Schemas = nil },
		"log level":     func(c *Config) { c.Log.Level = "loud" },
		"max page size": func(c *Config) { c.Limits.MaxPageSize = 0 },
		"readyz":        func(c *Config) { c.Server.ReadyzTimeout = 0 },
		"doc cache":     func(c *Config) { c.Server.DocumentCache.Size = -1 },
		"query tokens":  func(c *Config) { c.Limits.MaxQueryTokens = 0 },
		"input depth":   func(c *Config) { c.Limits.MaxInputDepth = 0 },
		"selections":    func(c *Config) { c.Limits.MaxSelections = 0 },
		"introspection": func(c *Config) { c.Limits.MaxIntrospectionBytes = 0 },
		"comparisons":   func(c *Config) { c.Limits.MaxFieldComparisons = 0 },
		"cache bytes":   func(c *Config) { c.Server.DocumentCache.Bytes = 0 },
		"cache entry":   func(c *Config) { c.Server.DocumentCache.MaxEntryBytes = 0 },
		"anon role":     func(c *Config) { c.Auth.Mode = "none" },
		"log format":    func(c *Config) { c.Log.Format = "xml" },
		"headers mode":  func(c *Config) { c.Auth.Mode = "headers" },
		"plugin name":   func(c *Config) { c.Plugins.Disabled = []string{"simple-names"} },
		"nested depth":  func(c *Config) { c.Plugins.Settings.NestedMutations.MaxDepth = 0 },
		"nested ops":    func(c *Config) { c.Plugins.Settings.NestedMutations.MaxOperations = 0 },
		"scan cost":     func(c *Config) { c.Aggregates.ScanCost = -1 },
		"max cost":      func(c *Config) { c.Limits.MaxCost = 0 },
		"max depth":     func(c *Config) { c.Limits.MaxDepth = 0 },
		"body bytes":    func(c *Config) { c.Limits.MaxBodyBytes = 0 },
		"partial items": func(c *Config) { c.TX.Partial.MaxItems = 0 },
		"pq only":       func(c *Config) { c.PersistedQueries.Only = true },
		"pq bytes":      func(c *Config) { c.PersistedQueries.MaxBytes = 0 },
		"pq entry":      func(c *Config) { c.PersistedQueries.MaxEntryBytes = 0 },
		"no timeout":    func(c *Config) { c.Server.RequestTimeout = 0 },
		"short timeout": func(c *Config) { c.Server.RequestTimeout = 500 * time.Millisecond },
		"watch channel": func(c *Config) { c.Watch.Enabled = true; c.Watch.Channel = "bad$$chan" },
		"trusted proxy": func(c *Config) { c.Auth.Headers.TrustedProxies = []string{"10.0.0.0/8", "not-an-ip"} },
		"metrics path":  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "metrics" },
		"metrics route": func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Path = "/graphql" },
		"metrics addr":  func(c *Config) { c.Metrics.Enabled = true; c.Metrics.Addr = c.Server.Addr },
		"subs max":      func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.MaxPerConnection = 0 },
		"subs buffer":   func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.Buffer = 0 },
		"subs ka":       func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.Keepalive = -1 },
		"subs conns":    func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.MaxConnections = 0 },
		"subs ops":      func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.MaxOperations = 0 },
		"subs rate":     func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.SubscribeRate = 0 },
		"subs prefix": func(c *Config) {
			c.Subscriptions.Enabled = true
			c.Subscriptions.TopicPrefix = strings.Repeat("p", 63)
		},
		"subs topics":   func(c *Config) { c.Subscriptions.Enabled = true; c.Subscriptions.Topics = []string{"bad topic"} },
		"contract name": func(c *Config) { c.Contracts = []Contract{{Name: "Public"}} },
		"contract metrics": func(c *Config) {
			c.Contracts = []Contract{{Name: "public"}}
			c.Metrics.Enabled = true
			c.Metrics.Path = "/public/metrics"
		},
		"contract path": func(c *Config) { c.Contracts = []Contract{{Name: "a"}, {Name: "b", Path: "/a"}} },
		"contract smart_comments": func(c *Config) {
			c.Contracts = []Contract{{Name: "a"}}
			c.Plugins.Disabled = []string{"smart_comments"}
		},
		"inc deferred":       func(c *Config) { c.Incremental.Enabled = true; c.Incremental.MaxDeferred = 0 },
		"inc batch":          func(c *Config) { c.Incremental.Enabled = true; c.Incremental.StreamBatchSize = 0 },
		"inc write timeout":  func(c *Config) { c.Incremental.Enabled = true; c.Incremental.WriteTimeout = 0 },
		"inc responses":      func(c *Config) { c.Incremental.Enabled = true; c.Incremental.MaxResponses = 0 },
		"inc responses pool": func(c *Config) { c.Incremental.Enabled = true; c.Incremental.MaxResponses = int(c.Database.MaxConns) },
		"live no subs":       func(c *Config) { c.Live.Enabled = true },
		"live channel":       func(c *Config) { c.Subscriptions.Enabled, c.Live.Enabled, c.Live.Channel = true, true, "pdbq-live" },
		"live channel length": func(c *Config) {
			c.Subscriptions.Enabled, c.Live.Enabled, c.Live.Channel = true, true, strings.Repeat("c", 55)
		},
		"live throttle":    func(c *Config) { c.Subscriptions.Enabled, c.Live.Enabled, c.Live.Throttle = true, true, -1 },
		"live concurrency": func(c *Config) { c.Subscriptions.Enabled, c.Live.Enabled, c.Live.Concurrency = true, true, 0 },
		"live per conn":    func(c *Config) { c.Subscriptions.Enabled, c.Live.Enabled, c.Live.MaxPerConnection = true, true, 0 },
		"live ops":         func(c *Config) { c.Subscriptions.Enabled, c.Live.Enabled, c.Live.MaxOperations = true, true, 0 },
	}
	for name, mutate := range cases {
		if err := bad(mutate); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestLoadRejectsUnsafeIdentitySettings(t *testing.T) {
	cases := []struct {
		name, yaml string
		want       []string
	}{
		{"allowed role none", "roles:\n  allowed: [app_user, none]\n", []string{"roles.allowed", `"none"`}},
		{"allowed role NONE", "roles:\n  allowed: [NONE]\n", []string{"roles.allowed", `"NONE"`}},
		{"allowed role public", "roles:\n  allowed: [public]\n", []string{"roles.allowed", `"public"`}},
		{"allowed role pg_", "roles:\n  allowed: [pg_read_all_data]\n", []string{"roles.allowed", `"pg_read_all_data"`}},
		{"anonymous role none", "roles:\n  anonymous: none\n", []string{"roles.anonymous", `"none"`}},
		{"default role pg_", "roles:\n  default: pg_write_all_data\n", []string{"roles.default", `"pg_write_all_data"`}},
		{"claims prefix", "claims:\n  prefix: plpgsql.claims\n", []string{"claims.prefix", `"plpgsql"`}},
		{"json setting under prefix", "claims:\n  json_setting: jwt.claims.all\n", []string{"claims.json_setting", "claims.prefix"}},
		{"headers without proxies", "auth:\n  mode: headers\n", []string{"auth.headers.trusted_proxies is required"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pdbq.yaml")
			yaml := "database:\n  url: \"postgres://file/db\"\n" + tc.yaml
			if !strings.HasPrefix(tc.yaml, "auth:") {
				yaml += "auth:\n  jwt:\n    secret: s\n"
			}
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
	t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
	t.Setenv("PDBQ_ROLES_ALLOWED", "app_user,none")
	_, err := Load("", nil)
	if err == nil || !strings.Contains(err.Error(), `roles.allowed: "none"`) {
		t.Fatalf("Load error = %v, want roles.allowed: \"none\" rejected", err)
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
auth:
  mode: none
roles:
  switch: false
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
	if cfg.Limits.MaxDepth != 15 {
		t.Errorf("default lost: %d", cfg.Limits.MaxDepth)
	}
}

func TestExposePartitionsFromEnv(t *testing.T) {
	if DefaultConfig().Schema.ExposePartitions {
		t.Fatal("schema.expose_partitions defaults to true, want false")
	}
	t.Setenv("PDBQ_SCHEMA_EXPOSE__PARTITIONS", "true")
	t.Setenv("PDBQ_AUTH_MODE", "none")
	t.Setenv("PDBQ_ROLES_SWITCH", "false")
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
		"auth:", "jwt:", "secret_file:", "headers:", "trusted_proxies:", "mint_type:",
		"roles:", "switch:", "anonymous:", "claims:", "json_setting:", "transactions:", "watch:", "errors:",
		"plugins:", "simple_names:", "advanced_filters:", "relations_opt_in:", "postgis:", "typed_geometry:", "contrib:",
		"aggregates:", "scan_cost:", "nested_mutations:", "max_operations:", "log:",
		"limits:", "max_cost:", "persisted_queries:", "subscriptions:", "live:", "incremental:",
		"document_cache:", "introspection:", "domains:", "partial:", "max_items:",
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
	cfg.Auth.JWT.Secret = "s"
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
	t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Metrics.Enabled || cfg.Metrics.Addr != ":9090" || cfg.Metrics.Path != "/metrics" {
		t.Fatalf("metrics = %+v", cfg.Metrics)
	}
}

func TestContracts(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "s"
	cfg.Contracts = []Contract{{Name: "internal", Path: "/"}, {Name: "public"}, {Name: "partner", Default: "exclude"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if ct, err := cfg.Contract("partner"); err != nil || ct.Default != "exclude" {
		t.Errorf("Contract(partner) = %+v, %v", ct, err)
	}
	if ct, err := cfg.Contract(""); err != nil || ct.Name != "" {
		t.Errorf("Contract(\"\") = %+v, %v", ct, err)
	}
	if _, err := cfg.Contract("nope"); err == nil || err.Error() != `config: unknown contract "nope"` {
		t.Errorf("Contract(nope) error = %v", err)
	}
}

func TestContractsFromEnv(t *testing.T) {
	t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
	t.Setenv("PDBQ_CONTRACTS", "public,partner")
	cfg, err := Load("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Contracts) != 2 || cfg.Contracts[0].Name != "public" || cfg.Contracts[1].Name != "partner" {
		t.Errorf("contracts = %+v", cfg.Contracts)
	}
}

func TestIncrementalMaxResponsesUnboundedPool(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Auth.JWT.Secret = "s"
	cfg.Incremental.Enabled = true
	cfg.Database.MaxConns = 0
	cfg.Incremental.MaxResponses = 50
	if err := cfg.Validate(); err != nil {
		t.Fatalf("max_responses with the pgxpool default pool: %v", err)
	}
}
