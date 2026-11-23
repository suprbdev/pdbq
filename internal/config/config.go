// Package config defines pdbq's configuration model and its loading rules:
// YAML file < environment (PDBQ_*) < command-line flags.
//
// Every field carries a `koanf` tag (its YAML/env path) and a `doc` tag; the
// annotated reference YAML (`pdbq config example`) is generated from these
// tags so documentation can never drift from the structs.
package config

import (
	"fmt"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/suprbdev/pdbcore/confx"
	"github.com/suprbdev/pdbq/internal/subscribe"
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
	Metrics  Metrics  `koanf:"metrics"`
}

// The shared sections (koanf paths and doc strings) live in pdbcore/confx;
// Server, Schema (with expose_partitions and domain_scalars) and TX (with
// per_request) are pdbq's own.
type (
	Database = confx.Database
	Filters  = confx.Filters
	RLS      = confx.RLS
	Auth     = confx.Auth
	Watch    = confx.Watch
	Errors   = confx.Errors
	Plugins  = confx.Plugins
	Log      = confx.Log
	Metrics  = confx.Metrics
)

type Server struct {
	Addr                       string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	GraphiQL                   bool          `koanf:"graphiql" doc:"Serve the GraphiQL playground at / (off by default; enable for development)."`
	ExposeSchema               bool          `koanf:"expose_schema" doc:"Serve the generated SDL at /schema.graphql (off by default; it reveals every table, column and relation to unauthenticated callers)."`
	RequestTimeout             time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout."`
	ReadyzTimeout              time.Duration `koanf:"readyz_timeout" doc:"Timeout of the database ping behind GET /readyz; an unreachable or slow database answers 503. /healthz stays a liveness check."`
	MaxBodyBytes               int64         `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes."`
	MaxDepth                   int           `koanf:"max_depth" doc:"Maximum GraphQL selection depth per operation."`
	MaxCost                    int           `koanf:"max_cost" doc:"Maximum estimated cost (selected fields x list multipliers) per operation."`
	MaxPageSize                int           `koanf:"max_page_size" doc:"Maximum rows per page; first/last above this are rejected and it is the default page size when neither is given."`
	MaxQueryTokens             int           `koanf:"max_query_tokens" doc:"Maximum lexical tokens (names, punctuation, values, comments) in a GraphQL document; parsing stops at the first token beyond it. Bounds the parse and validation work of one request."`
	MaxInputDepth              int           `koanf:"max_input_depth" doc:"Maximum nesting of input values (list and input object literals, and variable values of list and input object types), e.g. filter not/and/or chains."`
	MaxSelections              int           `koanf:"max_selections" doc:"Maximum selections (fields, fragment spreads, inline fragments) in a GraphQL document with every fragment spread expanded; rejects fragment bombs before they are walked."`
	MaxFieldComparisons        int           `koanf:"max_field_comparisons" doc:"Maximum field pairs validation compares to check that fields sharing a response key at one position (fragments expanded) can be merged; that check is quadratic in such repeated fields."`
	DocumentCacheSize          int           `koanf:"document_cache_size" doc:"Number of parsed and validated GraphQL documents cached by query text (least recently used evicted first, within document_cache_bytes). 0 disables the cache."`
	DocumentCacheBytes         int           `koanf:"document_cache_bytes" doc:"Estimated memory the document cache may retain (query text plus parsed and validated AST); least recently used documents are evicted beyond it."`
	DocumentCacheMaxEntryBytes int           `koanf:"document_cache_max_entry_bytes" doc:"Estimated size above which a parsed document is not cached (large documents gain little from caching)."`
	CORSOrigins                []string      `koanf:"cors_origins" doc:"Allowed CORS origins for /graphql (exact match, or '*' for any). Empty disables CORS headers entirely."`
	Compression                bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	APQ                        bool          `koanf:"apq" doc:"Enable Apollo automatic persisted queries: clients send a sha256 hash in the persistedQuery extension and register the document once on a miss (in-memory cache)."`
	PersistedQueriesPath       string        `koanf:"persisted_queries_path" doc:"JSON file mapping sha256 hex hashes to GraphQL documents, preloaded as persisted queries (never evicted)."`
	PersistedOnly              bool          `koanf:"persisted_only" doc:"Reject requests that do not reference a persisted query via the persistedQuery extension (requires apq or persisted_queries_path)."`
	ReadOnly                   bool          `koanf:"read_only" doc:"Reject every mutation operation (read-only mode) for maintenance, demos or defense in depth. Also toggleable at runtime when embedding pdbq as a library (App.SetReadOnly)."`
	DisableIntrospection       bool          `koanf:"disable_introspection" doc:"Reject __schema / __type introspection queries and leave the \"Did you mean\" suggestions out of validation errors (off by default). Enable in production to hide the schema shape; __typename keeps working."`

	Subscriptions                 bool          `koanf:"subscriptions" doc:"Serve PostGraphile-style simple subscriptions (Subscription.listen over LISTEN/NOTIFY) on /graphql over WebSocket, graphql-transport-ws or the legacy graphql-ws subprotocol. Off by default; while off the schema has no Subscription type."`
	SubscriptionsMaxPerConnection int           `koanf:"subscriptions_max_per_connection" doc:"Maximum operations running at once on one WebSocket connection, counted until each has fully stopped; a subscribe beyond it gets an error (or waits while completed operations are still stopping)."`
	SubscriptionsMaxConnections   int           `koanf:"subscriptions_max_connections" doc:"Maximum WebSocket connections at once; further upgrade requests are answered 503."`
	SubscriptionsMaxOperations    int           `koanf:"subscriptions_max_operations" doc:"Maximum operations running at once over all WebSocket connections; a subscribe beyond it gets an error."`
	SubscriptionsSubscribeRate    int           `koanf:"subscriptions_subscribe_rate" doc:"Subscribe messages accepted per second on one WebSocket connection, with bursts up to subscriptions_max_per_connection; faster clients are slowed down (their messages are read later). 0 disables the limit."`
	SubscriptionsTopicPrefix      string        `koanf:"subscriptions_topic_prefix" doc:"Prefix put before the listen topic to form the PostgreSQL channel, as in pg_notify('postgraphile:' || topic, payload). The channel must fit in 63 bytes."`
	SubscriptionsTopics           []string      `koanf:"subscriptions_topics" doc:"Topics clients may listen to, without the prefix; an entry ending in * allows every topic starting with the rest. Empty allows any topic of letters, digits and _ . : / - characters."`
	SubscriptionsKeepalive        time.Duration `koanf:"subscriptions_keepalive" doc:"Interval of the WebSocket ping (and legacy graphql-ws ka message) on subscription connections; a peer that does not answer within it is disconnected. 0 disables."`
	SubscriptionsBuffer           int           `koanf:"subscriptions_buffer" doc:"Notifications queued per subscription while its events are being resolved; a subscriber that falls further behind gets an error and its subscription ends."`
}

// Schema is confx.Schema plus expose_partitions and domain_scalars. It
// repeats the shared fields rather than embedding confx.Schema so composite
// literals keep naming them directly.
type Schema struct {
	Schemas          []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath        string   `koanf:"cache_path" doc:"Boot from this schema cache file instead of introspecting (see pdbq schema dump)."`
	Functions        bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
	ExposePartitions bool     `koanf:"expose_partitions" doc:"Expose the partitions of partitioned tables as tables of their own. Off by default: their rows are reachable through the partitioned parent."`
	DomainScalars    bool     `koanf:"domain_scalars" doc:"Expose each domain over a built-in scalar type as a custom scalar named after it (PostGraphile's behaviour), described by its comment and constraints. Disable to type domain columns and arguments as their base type."`
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
			Addr:                       ":8080",
			RequestTimeout:             30 * time.Second,
			ReadyzTimeout:              2 * time.Second,
			MaxBodyBytes:               1 << 20,
			MaxDepth:                   15,
			MaxCost:                    10000,
			MaxPageSize:                100,
			MaxQueryTokens:             15000,
			MaxInputDepth:              32,
			MaxSelections:              10000,
			MaxFieldComparisons:        100000,
			DocumentCacheSize:          1000,
			DocumentCacheBytes:         64 << 20,
			DocumentCacheMaxEntryBytes: 1 << 20,

			SubscriptionsMaxPerConnection: 100,
			SubscriptionsMaxConnections:   1000,
			SubscriptionsMaxOperations:    10000,
			SubscriptionsSubscribeRate:    100,
			SubscriptionsTopicPrefix:      "postgraphile:",
			SubscriptionsKeepalive:        15 * time.Second,
			SubscriptionsBuffer:           32,
		},
		Schema: Schema{
			Schemas:       []string{"public"},
			Functions:     true,
			DomainScalars: true,
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
		Metrics: confx.DefaultMetrics(),
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
	if c.Server.ReadyzTimeout <= 0 {
		errs = append(errs, "server.readyz_timeout must be > 0")
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
	for _, l := range []struct {
		name string
		v    int
	}{
		{"max_query_tokens", c.Server.MaxQueryTokens},
		{"max_input_depth", c.Server.MaxInputDepth},
		{"max_selections", c.Server.MaxSelections},
		{"max_field_comparisons", c.Server.MaxFieldComparisons},
		{"document_cache_bytes", c.Server.DocumentCacheBytes},
		{"document_cache_max_entry_bytes", c.Server.DocumentCacheMaxEntryBytes},
	} {
		if l.v < 1 {
			errs = append(errs, "server."+l.name+" must be >= 1")
		}
	}
	if c.Server.Subscriptions {
		errs = append(errs, validateSubscriptions(c.Server)...)
	}
	errs = append(errs, confx.ValidateLog(c.Log)...)
	if c.Metrics.Enabled {
		errs = append(errs, confx.ValidateMetrics(c.Metrics, c.Server.Addr)...)
		if c.Metrics.Addr == "" && reservedPaths[c.Metrics.Path] {
			errs = append(errs, fmt.Sprintf("metrics.path: %q is a pdbq route; choose another path or serve metrics on metrics.addr", c.Metrics.Path))
		}
	}
	return confx.InvalidConfig(errs)
}

// reservedPaths are the routes pdbq serves on the main listener, which
// metrics.path must not shadow.
var reservedPaths = map[string]bool{"/graphql": true, "/healthz": true, "/readyz": true, "/schema.graphql": true}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool { return c.Plugins.DisabledSet() }

func validateSubscriptions(s Server) []string {
	var errs []string
	if s.SubscriptionsMaxPerConnection < 1 {
		errs = append(errs, "server.subscriptions_max_per_connection must be >= 1")
	}
	if s.SubscriptionsMaxConnections < 1 {
		errs = append(errs, "server.subscriptions_max_connections must be >= 1")
	}
	if s.SubscriptionsMaxOperations < 1 {
		errs = append(errs, "server.subscriptions_max_operations must be >= 1")
	}
	if s.SubscriptionsSubscribeRate < 0 {
		errs = append(errs, "server.subscriptions_subscribe_rate must be >= 0")
	}
	if s.SubscriptionsBuffer < 1 {
		errs = append(errs, "server.subscriptions_buffer must be >= 1")
	}
	if s.SubscriptionsKeepalive < 0 {
		errs = append(errs, "server.subscriptions_keepalive must be >= 0")
	}
	if _, err := subscribe.NewTopicPolicy(s.SubscriptionsTopicPrefix, s.SubscriptionsTopics); err != nil {
		errs = append(errs, "server."+err.Error())
	}
	return errs
}
