// Package config defines pdbq's configuration model and its loading rules:
// YAML file < environment (PDBQ_*) < command-line flags.
//
// Every field carries a `koanf` tag (its YAML/env path) and a `doc` tag; the
// annotated reference YAML (`pdbq config example`) is generated from these
// tags so documentation can never drift from the structs.
package config

import (
	"fmt"
	"runtime"
	"strings"
	"time"

	flag "github.com/spf13/pflag"
	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbcore/confx"
	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbq/internal/live"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	"github.com/suprbdev/pdbq/internal/plugins/contrib"
	"github.com/suprbdev/pdbq/internal/plugins/nestedmutations"
	"github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/plugins/simplenames"
	"github.com/suprbdev/pdbq/internal/plugins/smartcomments"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

type Config struct {
	Database  Database   `koanf:"database"`
	Server    Server     `koanf:"server"`
	Schema    Schema     `koanf:"schema"`
	Filters   Filters    `koanf:"filters"`
	Auth      Auth       `koanf:"auth"`
	Roles     Roles      `koanf:"roles"`
	Claims    Claims     `koanf:"claims"`
	TX        TX         `koanf:"transactions"`
	Watch     Watch      `koanf:"watch"`
	Errors    Errors     `koanf:"errors"`
	Plugins   Plugins    `koanf:"plugins"`
	Log       Log        `koanf:"log"`
	Metrics   Metrics    `koanf:"metrics"`
	Contracts []Contract `koanf:"contracts" doc:"Schema contracts: named GraphQL schemas for different audiences, each a subset of the full schema selected by @contract / @behavior ±contract:<name> smart comments and served at <path>/graphql. Entries are {name, default: include|exclude, path (default /<name>)}, or bare names. When set, only the contracts are served (give one path: / to serve it at /graphql). Requires the smart_comments plugin."`
}

// The shared sections (koanf paths and doc strings) live in pdbcore/confx;
// Server, Schema (with expose_partitions and domain_scalars) and TX (with
// per_request) are pdbq's own.
type (
	Database = confx.Database
	Filters  = confx.Filters
	Auth     = confx.Auth
	JWT      = confx.JWT
	Headers  = confx.Headers
	Roles    = confx.Roles
	Claims   = confx.Claims
	Watch    = confx.Watch
	Errors   = confx.Errors
	Plugins  = confx.Plugins[PluginSettings]
	Log      = confx.Log
	Metrics  = confx.Metrics
	Contract = confx.Contract
)

// PluginSettings is the plugins.settings section: one typed section per
// configurable built-in plugin.
type PluginSettings struct {
	AdvancedFilters advancedfilters.Settings `koanf:"advanced_filters" doc:"advanced_filters plugin: relation filters, relational ordering and computed-column filtering and ordering."`
	Postgis         postgis.Settings         `koanf:"postgis" doc:"postgis plugin: spatial filters, accessors, projection arguments and distance ordering."`
	Contrib         contrib.Settings         `koanf:"contrib" doc:"contrib plugin: full-text, pg_trgm, hstore, ltree, range, inet, pgvector, cube and earthdistance surfaces."`
	Aggregates      aggregates.Settings      `koanf:"aggregates" doc:"aggregates plugin: connection, grouped, relational-order and relational-filter aggregates (opt-in)."`
	NestedMutations nestedmutations.Settings `koanf:"nested_mutations" doc:"nested_mutations plugin: nested connect/create/update/delete/disconnect inputs on create and update mutations."`
}

// DefaultPluginSettings returns every plugin's default settings.
func DefaultPluginSettings() PluginSettings {
	return PluginSettings{
		AdvancedFilters: advancedfilters.DefaultSettings(),
		Postgis:         postgis.DefaultSettings(),
		Contrib:         contrib.DefaultSettings(),
		Aggregates:      aggregates.DefaultSettings(),
		NestedMutations: nestedmutations.DefaultSettings(),
	}
}

// Validate returns the problems of the plugin settings.
func (p PluginSettings) Validate() []string {
	return append(p.Aggregates.Validate(), p.NestedMutations.Validate()...)
}

// BuiltinPluginNames lists the built-in plugins in priority order: the
// names plugins.disabled accepts.
var BuiltinPluginNames = []string{
	smartcomments.Name,
	simplenames.Name,
	advancedfilters.Name,
	postgis.Name,
	contrib.Name,
	aggregates.Name,
	nestedmutations.Name,
}

// movedKeys are pdbq's renamed keys, on top of confx.MovedKeys.
var movedKeys = map[string]string{
	"plugins.settings.advanced-filters": "plugins.settings.advanced_filters",
	"plugins.settings.nested-mutations": "plugins.settings.nested_mutations",
}

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
	MaxHeadlineDocument        int           `koanf:"max_headline_document" doc:"Characters of a document a full-text headline (<field>Headline) reads: text is cut to its first max_headline_document characters, a longer json/jsonb document gets a null headline. ts_headline's work grows with the document, and the cost estimate charges each headline by this bound."`
	MaxQueryTokens             int           `koanf:"max_query_tokens" doc:"Maximum lexical tokens (names, punctuation, values, comments) in a GraphQL document; parsing stops at the first token beyond it. Bounds the parse and validation work of one request."`
	MaxInputDepth              int           `koanf:"max_input_depth" doc:"Maximum nesting of input values (list and input object literals, and variable values of list and input object types), e.g. filter not/and/or chains."`
	MaxSelections              int           `koanf:"max_selections" doc:"Maximum selections (fields, fragment spreads, inline fragments) in a GraphQL document with every fragment spread expanded; rejects fragment bombs before they are walked."`
	MaxIntrospectionBytes      int           `koanf:"max_introspection_bytes" doc:"Maximum estimated JSON size in bytes of the __schema / __type fields of one operation; aliases multiply every type and field they select, so larger introspection responses are rejected while they are built."`
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

	IncrementalDelivery        bool          `koanf:"incremental_delivery" doc:"Serve the defer and stream directives of GraphQL incremental delivery: the schema declares them, a query using them is answered over HTTP as multipart/mixed (Accept: multipart/mixed) and over WebSocket as several next messages, every payload read from one repeatable read snapshot. Off by default; while off the schema declares neither directive, stream is rejected and defer is ignored (one result)."`
	IncrementalMaxDeferred     int           `koanf:"incremental_max_deferred" doc:"Maximum deferred fragments and streamed fields in one operation; each one runs its own statements."`
	IncrementalStreamBatchSize int           `koanf:"incremental_stream_batch_size" doc:"Items per payload of a streamed list after its initialCount items; each batch is one statement."`
	IncrementalWriteTimeout    time.Duration `koanf:"incremental_write_timeout" doc:"Longest one payload of an incremental HTTP response may take to reach a client that stopped reading; the response is then aborted and its transaction rolled back, releasing the pooled connection. request_timeout still bounds the whole response."`
	IncrementalMaxResponses    int           `koanf:"incremental_max_responses" doc:"Incremental responses (HTTP and WebSocket) holding a transaction at once, over every contract; a request beyond it is answered by one result, with the defer and stream directives ignored. 0 means half of database.max_conns (at least 1)."`

	Subscriptions                 bool          `koanf:"subscriptions" doc:"Serve PostGraphile-style simple subscriptions (Subscription.listen over LISTEN/NOTIFY) on /graphql over WebSocket, graphql-transport-ws or the legacy graphql-ws subprotocol. Off by default; while off the schema has no Subscription type."`
	SubscriptionsMaxPerConnection int           `koanf:"subscriptions_max_per_connection" doc:"Maximum operations running at once on one WebSocket connection, counted until each has fully stopped; a subscribe beyond it gets an error (or waits while completed operations are still stopping)."`
	SubscriptionsMaxConnections   int           `koanf:"subscriptions_max_connections" doc:"Maximum WebSocket connections at once; further upgrade requests are answered 503."`
	SubscriptionsMaxOperations    int           `koanf:"subscriptions_max_operations" doc:"Maximum operations running at once over all WebSocket connections; a subscribe beyond it gets an error."`
	SubscriptionsSubscribeRate    int           `koanf:"subscriptions_subscribe_rate" doc:"Subscribe messages accepted per second on one WebSocket connection, with bursts up to subscriptions_max_per_connection; faster clients are slowed down (their messages are read later). 0 disables the limit."`
	SubscriptionsTopicPrefix      string        `koanf:"subscriptions_topic_prefix" doc:"Prefix put before the listen topic to form the PostgreSQL channel, as in pg_notify('postgraphile:' || topic, payload). The channel must fit in 63 bytes."`
	SubscriptionsTopics           []string      `koanf:"subscriptions_topics" doc:"Topics clients may listen to, without the prefix; an entry ending in * allows every topic starting with the rest. Empty allows any topic of letters, digits and _ . : / - characters."`
	SubscriptionsKeepalive        time.Duration `koanf:"subscriptions_keepalive" doc:"Interval of the WebSocket ping (and legacy graphql-ws ka message) on subscription connections; a peer that does not answer within it is disconnected. 0 disables."`
	SubscriptionsBuffer           int           `koanf:"subscriptions_buffer" doc:"Notifications queued per subscription while its events are being resolved; a subscriber that falls further behind gets an error and its subscription ends."`

	Live                 bool          `koanf:"live" doc:"Serve live queries (requires subscriptions): every Query field is repeated on the Subscription root, and a query operation marked with the live directive is live too; the result is re-sent whenever a table it read changes, re-run under the subscriber's identity. Change detection uses per-table triggers notifying live_channel. Off by default; while off the schema is unchanged."`
	LiveChannel          string        `koanf:"live_channel" doc:"LISTEN channel the change triggers notify with the changed table's OID; it also names the triggers (<channel>, <channel>_truncate) and their function (<channel>_notify). Letters, digits and underscores."`
	LiveInstallTriggers  bool          `koanf:"live_install_triggers" doc:"Install the change triggers at startup (and after watch-mode rebuilds) on the tables of the exposed schemas and the tables their views read; needs table ownership. Disable to install them yourself (see docs/live.md)."`
	LiveThrottle         time.Duration `koanf:"live_throttle" doc:"Shortest time between two runs of one live query; changes in between are coalesced into one run at the end of the interval."`
	LiveConcurrency      int           `koanf:"live_concurrency" doc:"Live query runs in flight at once across the server; further runs wait, so a burst of changes cannot take every pool connection."`
	LiveMaxPerConnection int           `koanf:"live_max_per_connection" doc:"Maximum live queries running at once on one WebSocket connection (they also count against subscriptions_max_per_connection)."`
	LiveMaxOperations    int           `koanf:"live_max_operations" doc:"Maximum live queries running at once over all WebSocket connections (they also count against subscriptions_max_operations)."`
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
	Mutations        bool   `koanf:"mutations" doc:"Wrap every mutation in a transaction."`
	PerRequest       bool   `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Isolation        string `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries       int    `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
	PartialMutations bool   `koanf:"partial_mutations" doc:"Let a request opt in (extensions.partialMutations) to running each mutation root field in its own savepoint, so a failing field is reported without rolling back the others. Requests asking for it while this is off are rejected."`
}

// IncrementalResponses returns server.incremental_max_responses, or when it
// is 0 half of the pool size (database.max_conns, or pgxpool's default of
// the larger of 4 and the CPU count when that is 0), at least 1.
func (c *Config) IncrementalResponses() int {
	if n := c.Server.IncrementalMaxResponses; n > 0 {
		return n
	}
	pool := int(c.Database.MaxConns)
	if pool <= 0 {
		pool = max(4, runtime.NumCPU())
	}
	return max(1, pool/2)
}

// DefaultConfig returns the documented defaults.
func DefaultConfig() Config {
	return Config{
		Database: Database{
			MaxConns:         10,
			ConnectTimeout:   10 * time.Second,
			StatementTimeout: 25 * time.Second,
		},
		Server: Server{
			Addr:                       ":8080",
			RequestTimeout:             30 * time.Second,
			ReadyzTimeout:              2 * time.Second,
			MaxBodyBytes:               1 << 20,
			MaxDepth:                   15,
			MaxCost:                    10000,
			MaxPageSize:                100,
			MaxHeadlineDocument:        pgcontrib.MaxHeadlineDocument,
			MaxQueryTokens:             15000,
			MaxInputDepth:              32,
			MaxSelections:              10000,
			MaxIntrospectionBytes:      16 << 20,
			MaxFieldComparisons:        100000,
			DocumentCacheSize:          1000,
			DocumentCacheBytes:         64 << 20,
			DocumentCacheMaxEntryBytes: 1 << 20,
			IncrementalMaxDeferred:     16,
			IncrementalStreamBatchSize: 10,
			IncrementalWriteTimeout:    10 * time.Second,

			SubscriptionsMaxPerConnection: 100,
			SubscriptionsMaxConnections:   1000,
			SubscriptionsMaxOperations:    10000,
			SubscriptionsSubscribeRate:    100,
			SubscriptionsTopicPrefix:      "postgraphile:",
			SubscriptionsKeepalive:        15 * time.Second,
			SubscriptionsBuffer:           32,

			LiveChannel:          live.DefaultChannel,
			LiveInstallTriggers:  true,
			LiveThrottle:         500 * time.Millisecond,
			LiveConcurrency:      4,
			LiveMaxPerConnection: 10,
			LiveMaxOperations:    1000,
		},
		Schema: Schema{
			Schemas:       []string{"public"},
			Functions:     true,
			DomainScalars: true,
		},
		Filters: Filters{IndexedOnly: true},
		Auth:    confx.DefaultAuth(ClaimHeaderPrefix),
		Roles:   confx.DefaultRoles(),
		Claims:  confx.DefaultClaims(),
		TX:      TX{Mutations: true, Isolation: "read_committed"},
		Watch:   Watch{PollInterval: 5 * time.Second, Channel: "pdbq_ddl"},
		Errors:  Errors{Detail: "prod"},
		Plugins: Plugins{Settings: DefaultPluginSettings()},
		Log:     Log{Level: "info", Format: "text"},
		Metrics: confx.DefaultMetrics(),
	}
}

// ClaimHeaderPrefix is the default auth.headers.prefix.
const ClaimHeaderPrefix = "X-Pdbq-Claim-"

// Load builds the effective config: defaults, then optional YAML file, then
// PDBQ_* env vars, then flags (highest precedence), and validates it. Pass
// only the flags the user set (confx.ChangedFlags) so unset flags do not
// shadow YAML/env. Loading is strict: unknown keys in the file, and unknown
// PDBQ_* variables inside a config section, are errors naming the key they
// moved to or the closest valid key.
func Load(path string, flags *flag.FlagSet) (Config, error) {
	cfg, err := LoadUnvalidated(path, flags)
	if err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// LoadUnvalidated is Load without the final Validate, for callers that
// override keys before validating (see [Config.ConnectionIdentity]).
func LoadUnvalidated(path string, flags *flag.FlagSet) (Config, error) {
	cfg := DefaultConfig()
	if err := confx.Load("PDBQ_", path, flags, &cfg, confx.WithMoved(movedKeys)); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ConnectionIdentity sets auth.mode none and roles.switch false: every
// request runs as the connection role with no credentials. The CLI
// commands that run without an HTTP request (pdbq query, schema, plugins)
// use it.
func (c *Config) ConnectionIdentity() {
	c.Auth.Mode = auth.ModeNone
	c.Roles.Switch = false
}

// Validate checks cross-field invariants.
func (c *Config) Validate() error {
	var errs []string
	errs = append(errs, confx.ValidateErrors(c.Errors)...)
	errs = append(errs, confx.ValidateIdentity(c.Auth, c.Roles, c.Claims)...)
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
	if c.Server.MaxHeadlineDocument < 1 {
		errs = append(errs, "server.max_headline_document must be >= 1")
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
		{"max_introspection_bytes", c.Server.MaxIntrospectionBytes},
		{"max_field_comparisons", c.Server.MaxFieldComparisons},
		{"document_cache_bytes", c.Server.DocumentCacheBytes},
		{"document_cache_max_entry_bytes", c.Server.DocumentCacheMaxEntryBytes},
	} {
		if l.v < 1 {
			errs = append(errs, "server."+l.name+" must be >= 1")
		}
	}
	if c.Server.IncrementalDelivery {
		if c.Server.IncrementalMaxDeferred < 1 {
			errs = append(errs, "server.incremental_max_deferred must be >= 1")
		}
		if c.Server.IncrementalStreamBatchSize < 1 {
			errs = append(errs, "server.incremental_stream_batch_size must be >= 1")
		}
		if c.Server.IncrementalWriteTimeout <= 0 {
			errs = append(errs, "server.incremental_write_timeout must be > 0")
		}
		if c.Server.IncrementalMaxResponses < 0 {
			errs = append(errs, "server.incremental_max_responses must be >= 0")
		}
	}
	if c.Server.Subscriptions {
		errs = append(errs, validateSubscriptions(c.Server)...)
	}
	if c.Server.Live {
		errs = append(errs, validateLive(c.Server)...)
	}
	errs = append(errs, confx.ValidateLog(c.Log)...)
	if c.Metrics.Enabled {
		errs = append(errs, confx.ValidateMetrics(c.Metrics, c.Server.Addr)...)
		if c.Metrics.Addr == "" && reservedPaths[c.Metrics.Path] {
			errs = append(errs, fmt.Sprintf("metrics.path: %q is a pdbq route; choose another path or serve metrics on metrics.addr", c.Metrics.Path))
		}
		for _, ct := range c.Contracts {
			if p := ct.URLPath(""); c.Metrics.Addr == "" && p != "/" && (c.Metrics.Path == p || strings.HasPrefix(c.Metrics.Path, p+"/")) {
				errs = append(errs, fmt.Sprintf("metrics.path: %q is under the path of contract %q; choose another path or serve metrics on metrics.addr", c.Metrics.Path, ct.Name))
			}
		}
	}
	if len(c.Contracts) > 0 {
		errs = append(errs, confx.ValidateContracts(c.Contracts, "", true)...)
		if c.DisabledPlugins()[smartcomments.Name] {
			errs = append(errs, "contracts require the smart_comments plugin: it removes the columns a contract hides (remove smart_comments from plugins.disabled)")
		}
	}
	errs = append(errs, confx.ValidatePluginNames(c.Plugins.Disabled, BuiltinPluginNames)...)
	errs = append(errs, c.Plugins.Settings.Validate()...)
	return confx.InvalidConfig(errs)
}

// Contract returns the configured contract named name; "" is the full
// schema and is always found.
func (c *Config) Contract(name string) (Contract, error) {
	if name == "" {
		return Contract{}, nil
	}
	ct, ok := confx.FindContract(c.Contracts, name)
	if !ok {
		return Contract{}, fmt.Errorf("config: unknown contract %q", name)
	}
	return ct, nil
}

// reservedPaths are the routes pdbq serves on the main listener, which
// metrics.path must not shadow.
var reservedPaths = map[string]bool{"/graphql": true, "/healthz": true, "/readyz": true, "/schema.graphql": true}

// DisabledPlugins returns the disabled-plugin set for registry filtering.
func (c *Config) DisabledPlugins() map[string]bool { return c.Plugins.DisabledSet() }

func validateLive(s Server) []string {
	var errs []string
	if !s.Subscriptions {
		errs = append(errs, "server.live requires server.subscriptions")
	}
	if !live.ValidChannel(s.LiveChannel) {
		errs = append(errs, fmt.Sprintf("server.live_channel: %q must be letters, digits and underscores, not starting with a digit, at most %d bytes", s.LiveChannel, live.MaxChannelBytes))
	}
	if s.LiveThrottle < 0 {
		errs = append(errs, "server.live_throttle must be >= 0")
	}
	if s.LiveConcurrency < 1 {
		errs = append(errs, "server.live_concurrency must be >= 1")
	}
	if s.LiveMaxPerConnection < 1 {
		errs = append(errs, "server.live_max_per_connection must be >= 1")
	}
	if s.LiveMaxOperations < 1 {
		errs = append(errs, "server.live_max_operations must be >= 1")
	}
	return errs
}

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
