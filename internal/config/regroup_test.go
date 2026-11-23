package config

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestMovedKeysNameTheirReplacement(t *testing.T) {
	schema := ExampleYAML()
	olds := make([]string, 0, len(MovedKeys))
	for old := range MovedKeys {
		olds = append(olds, old)
	}
	sort.Strings(olds)
	for _, old := range olds {
		to := MovedKeys[old]
		if strings.HasPrefix(old, "plugins.settings.") {
			continue
		}
		leaf := to[strings.LastIndex(to, ".")+1:]
		if !strings.Contains(schema, leaf+":") {
			t.Errorf("%s moved to %s, which the reference config does not have", old, to)
		}
		t.Run(old, func(t *testing.T) {
			section, key, _ := strings.Cut(old, ".")
			yaml := fmt.Sprintf("auth:\n  jwt:\n    secret: s\n%s:\n  %s: 1\n", section, key)
			if strings.Contains(key, ".") {
				a, b, _ := strings.Cut(key, ".")
				yaml = fmt.Sprintf("auth:\n  jwt:\n    secret: s\n%s:\n  %s:\n    %s: 1\n", section, a, b)
			}
			wantLoadError(t, writeConfig(t, yaml), "unknown key "+old+": moved to "+to)
		})
	}
}

func TestMovedKeysFromEnv(t *testing.T) {
	for env, want := range map[string]string{
		"PDBQ_SERVER_MAX__COST":                    "moved to PDBQ_LIMITS_MAX__COST",
		"PDBQ_SERVER_SUBSCRIPTIONS":                "moved to PDBQ_SUBSCRIPTIONS_ENABLED",
		"PDBQ_SERVER_LIVE__CHANNEL":                "moved to PDBQ_LIVE_CHANNEL",
		"PDBQ_SERVER_INCREMENTAL__DELIVERY":        "moved to PDBQ_INCREMENTAL_ENABLED",
		"PDBQ_SERVER_DOCUMENT__CACHE__SIZE":        "moved to PDBQ_SERVER_DOCUMENT__CACHE_SIZE",
		"PDBQ_SERVER_DISABLE__INTROSPECTION":       "moved to PDBQ_SERVER_INTROSPECTION",
		"PDBQ_SERVER_APQ":                          "moved to PDBQ_PERSISTED__QUERIES_APQ",
		"PDBQ_SCHEMA_DOMAIN__SCALARS":              "moved to PDBQ_SCHEMA_DOMAINS",
		"PDBQ_TRANSACTIONS_PARTIAL__MUTATIONS":     "moved to PDBQ_TRANSACTIONS_PARTIAL_ENABLED",
		"PDBQ_PLUGINS_SETTINGS_AGGREGATES_ENABLED": "moved to PDBQ_AGGREGATES_ENABLED",
	} {
		t.Run(env, func(t *testing.T) {
			t.Setenv("PDBQ_AUTH_JWT_SECRET", "s")
			t.Setenv(env, "true")
			wantLoadError(t, "", env, want)
		})
	}
}

func TestRemovedKeysRejected(t *testing.T) {
	for _, yaml := range []string{
		"transactions:\n  mutations: true\n",
		"plugins:\n  settings:\n    nested_mutations:\n      force_transactions: true\n",
	} {
		wantLoadError(t, writeConfig(t, "auth:\n  jwt:\n    secret: s\n"+yaml), "unknown key")
	}
}

func TestRegroupedKeysLoad(t *testing.T) {
	path := writeConfig(t, `
auth:
  jwt:
    secret: s
server:
  introspection: false
  document_cache:
    size: 7
limits:
  max_cost: 1234
  max_depth: 9
  max_body_bytes: 2048
persisted_queries:
  apq: true
  only: true
subscriptions:
  enabled: true
  topics: [chat]
live:
  enabled: true
  channel: my_live
incremental:
  enabled: true
  max_responses: 3
schema:
  domains: false
aggregates:
  enabled: true
transactions:
  partial:
    enabled: true
    max_items: 20
`)
	t.Setenv("PDBQ_LIMITS_MAX__PAGE__SIZE", "50")
	t.Setenv("PDBQ_SUBSCRIPTIONS_KEEPALIVE", "3s")
	t.Setenv("PDBQ_TRANSACTIONS_PARTIAL_MAX__ITEMS", "30")
	t.Setenv("PDBQ_SERVER_DOCUMENT__CACHE_BYTES", "4096")
	cfg, err := Load(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Introspection || cfg.Server.DocumentCache.Size != 7 || cfg.Server.DocumentCache.Bytes != 4096 {
		t.Errorf("server = %+v", cfg.Server)
	}
	if cfg.Limits.MaxCost != 1234 || cfg.Limits.MaxDepth != 9 || cfg.Limits.MaxBodyBytes != 2048 || cfg.Limits.MaxPageSize != 50 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if !cfg.PersistedQueries.APQ || !cfg.PersistedQueries.Only {
		t.Errorf("persisted_queries = %+v", cfg.PersistedQueries)
	}
	if !cfg.Subscriptions.Enabled || cfg.Subscriptions.Keepalive != 3*time.Second || len(cfg.Subscriptions.Topics) != 1 {
		t.Errorf("subscriptions = %+v", cfg.Subscriptions)
	}
	if !cfg.Live.Enabled || cfg.Live.Channel != "my_live" || !cfg.Incremental.Enabled || cfg.Incremental.MaxResponses != 3 {
		t.Errorf("live = %+v, incremental = %+v", cfg.Live, cfg.Incremental)
	}
	if cfg.Schema.Domains || !cfg.Aggregates.Enabled {
		t.Errorf("schema.domains = %v, aggregates.enabled = %v", cfg.Schema.Domains, cfg.Aggregates.Enabled)
	}
	if !cfg.TX.Partial.Enabled || cfg.TX.Partial.MaxItems != 30 {
		t.Errorf("transactions.partial = %+v", cfg.TX.Partial)
	}
}

func TestRegroupDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.Server.Introspection || !cfg.Schema.Domains || cfg.Aggregates.Enabled {
		t.Errorf("introspection %v, domains %v, aggregates %v", cfg.Server.Introspection, cfg.Schema.Domains, cfg.Aggregates.Enabled)
	}
	if cfg.Limits.MaxCost != 100000 || cfg.Limits.MaxPageSize != 100 || cfg.Limits.MaxDepth != 15 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.Incremental.MaxResponses != 5 || cfg.TX.Partial.MaxItems != 1000 || cfg.TX.Partial.Enabled {
		t.Errorf("incremental.max_responses %d, transactions.partial %+v", cfg.Incremental.MaxResponses, cfg.TX.Partial)
	}
}
