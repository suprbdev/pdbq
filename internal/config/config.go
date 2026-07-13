// Package config defines pdbq's configuration model and its loading rules:
// YAML file < environment (PDBQ_*) < command-line flags.
//
// Every field carries a `koanf` tag (its YAML/env path) and a `doc` tag; the
// annotated reference YAML (`pdbq config example`) is generated from these
// tags so documentation can never drift from the structs.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	flag "github.com/spf13/pflag"
)

type Config struct {
	Database Database `koanf:"database"`
	Server   Server   `koanf:"server"`
	Schema   Schema   `koanf:"schema"`
	Filters  Filters  `koanf:"filters"`
	RLS      RLS      `koanf:"rls"`
	TX       TX       `koanf:"transactions"`
	Watch    Watch    `koanf:"watch"`
	Errors   Errors   `koanf:"errors"`
	Plugins  Plugins  `koanf:"plugins"`
	Log      Log      `koanf:"log"`
}

type Database struct {
	URL              string        `koanf:"url" doc:"PostgreSQL connection URL, e.g. postgres://user:pass@host:5432/db. Also honours standard PG* env vars when empty."`
	MaxConns         int32         `koanf:"max_conns" doc:"Maximum pooled connections."`
	ConnectTimeout   time.Duration `koanf:"connect_timeout" doc:"Timeout for establishing a connection."`
	StatementTimeout time.Duration `koanf:"statement_timeout" doc:"Per-statement timeout applied to every request (0 disables)."`
}

type Server struct {
	Addr                 string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	GraphiQL             bool          `koanf:"graphiql" doc:"Serve the GraphiQL playground at / (off by default; enable for development)."`
	ExposeSchema         bool          `koanf:"expose_schema" doc:"Serve the generated SDL at /schema.graphql (off by default; it reveals every table, column and relation to unauthenticated callers)."`
	RequestTimeout       time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout."`
	MaxBodyBytes         int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes."`
	MaxDepth             int           `koanf:"max_depth" doc:"Maximum GraphQL selection depth per operation."`
	MaxCost              int           `koanf:"max_cost" doc:"Maximum estimated cost (selected fields x list multipliers) per operation."`
	MaxPageSize          int           `koanf:"max_page_size" doc:"Maximum rows per page; first/last are clamped to this and it is the default page size when neither is given."`
	CORSOrigins          []string      `koanf:"cors_origins" doc:"Allowed CORS origins for /graphql (exact match, or '*' for any). Empty disables CORS headers entirely."`
	Compression          bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	APQ                  bool          `koanf:"apq" doc:"Enable Apollo automatic persisted queries: clients send a sha256 hash in the persistedQuery extension and register the document once on a miss (in-memory cache)."`
	PersistedQueriesPath string        `koanf:"persisted_queries_path" doc:"JSON file mapping sha256 hex hashes to GraphQL documents, preloaded as persisted queries (never evicted)."`
	PersistedOnly        bool          `koanf:"persisted_only" doc:"Reject requests that do not reference a persisted query via the persistedQuery extension (requires apq or persisted_queries_path)."`
}

type Schema struct {
	Schemas   []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath string   `koanf:"cache_path" doc:"Boot from this schema cache file instead of introspecting (see pdbq schema dump)."`
	Functions bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
}

type Filters struct {
	IndexedOnly  bool                `koanf:"indexed_only" doc:"Only allow filtering on columns covered by an index (default policy)."`
	AllowColumns map[string][]string `koanf:"allow_columns" doc:"Per-table extra filterable columns, keyed by schema.table, overriding indexed_only."`
}

type RLS struct {
	Enabled       bool     `koanf:"enabled" doc:"Run each request as a switched role with claims exposed via set_config (SET LOCAL). Disabling uses the privileged connection directly and logs a loud warning."`
	DefaultRole   string   `koanf:"default_role" doc:"Role assumed for authenticated requests without a role claim."`
	AnonymousRole string   `koanf:"anonymous_role" doc:"Role assumed for unauthenticated requests."`
	RoleClaim     string   `koanf:"role_claim" doc:"JWT claim (or header name in header mode) carrying the database role."`
	AllowedRoles  []string `koanf:"allowed_roles" doc:"Roles a request may assume via the role claim. Empty allows any role (subject to database grants); default_role and anonymous_role are always allowed."`
	ClaimsPrefix  string   `koanf:"claims_prefix" doc:"set_config namespace for request claims, e.g. pdbq.claims."`
	Auth          Auth     `koanf:"auth"`
}

type Auth struct {
	Mode         string        `koanf:"mode" doc:"Claim source: 'jwt', 'headers' (behind a trusted gateway), or 'none'."`
	JWTSecret    string        `koanf:"jwt_secret" doc:"HMAC secret for HS256/384/512 verification (jwt mode; ignored when jwks_url is set)."`
	JWKSURL      string        `koanf:"jwks_url" doc:"JWKS endpoint for asymmetric JWT verification (RS256/384/512, ES256/384/512). Takes precedence over jwt_secret."`
	JWKSCacheTTL time.Duration `koanf:"jwks_cache_ttl" doc:"How long fetched JWKS keys are cached; an unknown kid triggers an early refresh (key rotation)."`
	JWTIssuer    string        `koanf:"jwt_issuer" doc:"Expected iss claim; empty skips the check."`
	JWTAudience  string        `koanf:"jwt_audience" doc:"Expected aud claim; empty skips the check."`
	HeaderPrefix string        `koanf:"header_prefix" doc:"Header prefix mapped to claims in headers mode, e.g. X-Pdbq-Claim-."`
	JWTType      string        `koanf:"jwt_type" doc:"Schema-qualified composite type (e.g. public.jwt) minted into a signed JWT: any function returning it yields an HS256 token string built from the composite's fields (an exp field becomes the token expiry). Requires jwt_secret; jwt_issuer/jwt_audience are embedded when set. Empty disables minting."`
}

type TX struct {
	Mutations  bool   `koanf:"mutations" doc:"Wrap every mutation in a transaction."`
	PerRequest bool   `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
}

type Watch struct {
	Enabled      bool          `koanf:"enabled" doc:"Re-introspect and hot-swap the schema on DDL changes (dev only; refuses to combine with schema.cache_path)."`
	PollInterval time.Duration `koanf:"poll_interval" doc:"Poll interval used when event triggers cannot be installed."`
	Channel      string        `koanf:"channel" doc:"NOTIFY channel used by the DDL event trigger."`
}

type Errors struct {
	Detail string `koanf:"detail" doc:"'dev' exposes full PostgreSQL error detail in GraphQL errors; 'prod' returns sanitized messages with a correlation id."`
}

type Plugins struct {
	Disabled []string                  `koanf:"disabled" doc:"Plugin names to disable."`
	Settings map[string]map[string]any `koanf:"settings" doc:"Per-plugin configuration, keyed by plugin name."`
}

type Log struct {
	Level  string `koanf:"level" doc:"Log level: debug, info, warn, error."`
	Format string `koanf:"format" doc:"Log format: text or json."`
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Database: Database{
			MaxConns:         10,
			ConnectTimeout:   10 * time.Second,
			StatementTimeout: 30 * time.Second,
		},
		Server: Server{
			Addr:           ":8080",
			RequestTimeout: 30 * time.Second,
			MaxBodyBytes:   1 << 20,
			MaxDepth:       15,
			MaxCost:        10000,
			MaxPageSize:    100,
		},
		Schema: Schema{
			Schemas:   []string{"public"},
			Functions: true,
		},
		Filters: Filters{IndexedOnly: true},
		RLS: RLS{
			Enabled:       true,
			AnonymousRole: "anonymous",
			RoleClaim:     "role",
			ClaimsPrefix:  "pdbq.claims",
			Auth:          Auth{Mode: "jwt", HeaderPrefix: "X-Pdbq-Claim-", JWKSCacheTTL: time.Hour},
		},
		TX:      TX{Mutations: true, Isolation: "read_committed"},
		Watch:   Watch{PollInterval: 5 * time.Second, Channel: "pdbq_ddl"},
		Errors:  Errors{Detail: "prod"},
		Plugins: Plugins{Settings: map[string]map[string]any{}},
		Log:     Log{Level: "info", Format: "text"},
	}
}

// Load builds the effective config: defaults, then optional YAML file, then
// PDBQ_* env vars, then flags (highest precedence).
func Load(path string, flags *flag.FlagSet) (Config, error) {
	k := koanf.New(".")
	cfg := DefaultConfig()

	if path != "" {
		if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
			return cfg, fmt.Errorf("config: read %s: %w", path, err)
		}
	}
	// PDBQ_DATABASE_URL -> database.url ; single underscores map to dots,
	// so nested keys with underscores in their names use double form:
	// PDBQ_FILTERS_INDEXED__ONLY etc. Keep it simple: lowercase, first _ per
	// section splits — we normalize by replacing "__" then "_".
	if err := k.Load(env.Provider("PDBQ_", ".", envKeyMapper), nil); err != nil {
		return cfg, fmt.Errorf("config: env: %w", err)
	}
	if flags != nil {
		if err := k.Load(posflag.Provider(flags, ".", k), nil); err != nil {
			return cfg, fmt.Errorf("config: flags: %w", err)
		}
	}
	if err := k.Unmarshal("", &cfg); err != nil {
		return cfg, fmt.Errorf("config: unmarshal: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// envKeyMapper maps PDBQ_SERVER_MAX__DEPTH -> server.max_depth: double
// underscore preserves an underscore inside a key; single underscore is a
// level separator.
func envKeyMapper(s string) string {
	s = strings.ToLower(strings.TrimPrefix(s, "PDBQ_"))
	s = strings.ReplaceAll(s, "__", "\x00")
	s = strings.ReplaceAll(s, "_", ".")
	return strings.ReplaceAll(s, "\x00", "_")
}

// Validate checks cross-field invariants.
func (c *Config) Validate() error {
	var errs []string
	switch c.Errors.Detail {
	case "dev", "prod":
	default:
		errs = append(errs, fmt.Sprintf("errors.detail: %q is not 'dev' or 'prod'", c.Errors.Detail))
	}
	switch c.RLS.Auth.Mode {
	case "jwt", "headers", "none":
	default:
		errs = append(errs, fmt.Sprintf("rls.auth.mode: %q is not 'jwt', 'headers' or 'none'", c.RLS.Auth.Mode))
	}
	if c.RLS.Enabled && c.RLS.Auth.Mode == "jwt" && c.RLS.Auth.JWTSecret == "" && c.RLS.Auth.JWKSURL == "" {
		errs = append(errs, "rls.auth.jwt_secret or rls.auth.jwks_url is required when rls.enabled and auth mode is jwt")
	}
	if c.RLS.Auth.JWTType != "" && c.RLS.Auth.JWTSecret == "" {
		errs = append(errs, "rls.auth.jwt_type requires rls.auth.jwt_secret: minting signs with the HMAC secret (JWKS keys are verify-only)")
	}
	if jt := c.RLS.Auth.JWTType; jt != "" && (strings.Count(jt, ".") != 1 || strings.HasPrefix(jt, ".") || strings.HasSuffix(jt, ".")) {
		errs = append(errs, fmt.Sprintf("rls.auth.jwt_type: %q must be schema-qualified, e.g. public.jwt", jt))
	}
	if c.RLS.Enabled && c.RLS.AnonymousRole == "" {
		errs = append(errs, "rls.anonymous_role is required when rls.enabled: an empty role would run unauthenticated requests as the privileged connection role, bypassing RLS")
	}
	switch c.TX.Isolation {
	case "read_committed", "repeatable_read", "serializable":
	default:
		errs = append(errs, fmt.Sprintf("transactions.isolation: %q invalid", c.TX.Isolation))
	}
	if c.Watch.Enabled && c.Schema.CachePath != "" {
		errs = append(errs, "watch.enabled cannot be combined with schema.cache_path")
	}
	if len(c.Schema.Schemas) == 0 {
		errs = append(errs, "schema.schemas must list at least one schema")
	}
	if c.Server.PersistedOnly && !c.Server.APQ && c.Server.PersistedQueriesPath == "" {
		errs = append(errs, "server.persisted_only requires server.apq or server.persisted_queries_path")
	}
	if c.TX.MaxRetries < 0 {
		errs = append(errs, "transactions.max_retries must be >= 0")
	}
	if c.Server.MaxDepth < 1 {
		errs = append(errs, "server.max_depth must be >= 1")
	}
	if c.Server.MaxPageSize < 1 {
		errs = append(errs, "server.max_page_size must be >= 1")
	}
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Sprintf("log.level: %q invalid", c.Log.Level))
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool {
	out := map[string]bool{}
	for _, n := range c.Plugins.Disabled {
		out[n] = true
	}
	return out
}
