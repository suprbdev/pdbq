// Package config defines pdbq's configuration model and its loading rules:
// YAML file < environment (PDBQ_*) < command-line flags.
//
// Every field carries a `koanf` tag (its YAML/env path) and a `doc` tag; the
// annotated reference YAML (`pdbq config example`) is generated from these
// tags so documentation can never drift from the structs.
package config

import (
	"time"

	flag "github.com/spf13/pflag"
	"github.com/suprbdev/pdbcore/confx"
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

// The shared sections (koanf paths and doc strings) live in pdbcore/confx;
// Server, Schema (with expose_partitions) and TX (with per_request) are
// pdbq's own.
type (
	Database = confx.Database
	Filters  = confx.Filters
	RLS      = confx.RLS
	Auth     = confx.Auth
	Watch    = confx.Watch
	Errors   = confx.Errors
	Plugins  = confx.Plugins
	Log      = confx.Log
)

type Server struct {
	Addr                 string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	GraphiQL             bool          `koanf:"graphiql" doc:"Serve the GraphiQL playground at / (off by default; enable for development)."`
	ExposeSchema         bool          `koanf:"expose_schema" doc:"Serve the generated SDL at /schema.graphql (off by default; it reveals every table, column and relation to unauthenticated callers)."`
	RequestTimeout       time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout."`
	MaxBodyBytes         int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes."`
	MaxDepth             int           `koanf:"max_depth" doc:"Maximum GraphQL selection depth per operation."`
	MaxCost              int           `koanf:"max_cost" doc:"Maximum estimated cost (selected fields x list multipliers) per operation."`
	MaxPageSize          int           `koanf:"max_page_size" doc:"Maximum rows per page; first/last above this are rejected and it is the default page size when neither is given."`
	DocumentCacheSize    int           `koanf:"document_cache_size" doc:"Number of parsed and validated GraphQL documents cached by query text (least recently used evicted first; at most 32 MiB of query text). 0 disables the cache."`
	CORSOrigins          []string      `koanf:"cors_origins" doc:"Allowed CORS origins for /graphql (exact match, or '*' for any). Empty disables CORS headers entirely."`
	Compression          bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	APQ                  bool          `koanf:"apq" doc:"Enable Apollo automatic persisted queries: clients send a sha256 hash in the persistedQuery extension and register the document once on a miss (in-memory cache)."`
	PersistedQueriesPath string        `koanf:"persisted_queries_path" doc:"JSON file mapping sha256 hex hashes to GraphQL documents, preloaded as persisted queries (never evicted)."`
	PersistedOnly        bool          `koanf:"persisted_only" doc:"Reject requests that do not reference a persisted query via the persistedQuery extension (requires apq or persisted_queries_path)."`
	ReadOnly             bool          `koanf:"read_only" doc:"Reject every mutation operation (read-only mode) for maintenance, demos or defense in depth. Also toggleable at runtime when embedding pdbq as a library (App.SetReadOnly)."`
	DisableIntrospection bool          `koanf:"disable_introspection" doc:"Reject __schema / __type introspection queries (off by default). Enable in production to hide the schema shape; __typename keeps working."`
}

// Schema is confx.Schema plus expose_partitions. It repeats the shared
// fields rather than embedding confx.Schema so composite literals keep
// naming them directly.
type Schema struct {
	Schemas          []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath        string   `koanf:"cache_path" doc:"Boot from this schema cache file instead of introspecting (see pdbq schema dump)."`
	Functions        bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
	ExposePartitions bool     `koanf:"expose_partitions" doc:"Expose the partitions of partitioned tables as tables of their own. Off by default: their rows are reachable through the partitioned parent."`
}

// Shared returns the confx.Schema part of s.
func (s Schema) Shared() confx.Schema {
	return confx.Schema{Schemas: s.Schemas, CachePath: s.CachePath, Functions: s.Functions}
}

type TX struct {
	Mutations  bool   `koanf:"mutations" doc:"Wrap every mutation in a transaction."`
	PerRequest bool   `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Isolation  string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
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
			Addr:              ":8080",
			RequestTimeout:    30 * time.Second,
			MaxBodyBytes:      1 << 20,
			MaxDepth:          15,
			MaxCost:           10000,
			MaxPageSize:       100,
			DocumentCacheSize: 1000,
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
// PDBQ_* env vars, then flags (highest precedence). Pass only the flags the
// user set (confx.ChangedFlags) so unset flags do not shadow YAML/env.
func Load(path string, flags *flag.FlagSet) (Config, error) {
	cfg := DefaultConfig()
	if err := confx.Load("PDBQ_", path, flags, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks cross-field invariants.
func (c *Config) Validate() error {
	var errs []string
	errs = append(errs, confx.ValidateErrors(c.Errors)...)
	errs = append(errs, confx.ValidateRLS(c.RLS)...)
	// max_retries is checked below, after the server rules, to keep the
	// pre-pdbcore message order.
	errs = append(errs, confx.ValidateTX(confx.TX{Mutations: c.TX.Mutations, Isolation: c.TX.Isolation})...)
	errs = append(errs, confx.ValidateWatch(c.Watch, c.Schema.CachePath)...)
	errs = append(errs, confx.ValidateSchema(c.Schema.Shared())...)
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
	if c.Server.DocumentCacheSize < 0 {
		errs = append(errs, "server.document_cache_size must be >= 0")
	}
	errs = append(errs, confx.ValidateLog(c.Log)...)
	return confx.InvalidConfig(errs)
}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool { return c.Plugins.DisabledSet() }
