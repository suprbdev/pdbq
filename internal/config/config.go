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
	Database         Database         `koanf:"database"`
	Server           Server           `koanf:"server"`
	Limits           Limits           `koanf:"limits"`
	PersistedQueries PersistedQueries `koanf:"persisted_queries"`
	Subscriptions    Subscriptions    `koanf:"subscriptions"`
	Live             Live             `koanf:"live"`
	Incremental      Incremental      `koanf:"incremental"`
	Schema           Schema           `koanf:"schema"`
	Filters          Filters          `koanf:"filters"`
	Aggregates       Aggregates       `koanf:"aggregates" doc:"Aggregates (aggregates plugin): aggregates and groupedAggregates on connections, relation-aggregate filters and ordering, spatial aggregates."`
	Auth             Auth             `koanf:"auth"`
	Roles            Roles            `koanf:"roles"`
	Claims           Claims           `koanf:"claims"`
	TX               TX               `koanf:"transactions"`
	Watch            Watch            `koanf:"watch"`
	Errors           Errors           `koanf:"errors"`
	Plugins          Plugins          `koanf:"plugins"`
	Log              Log              `koanf:"log"`
	Metrics          Metrics          `koanf:"metrics"`
	Contracts        []Contract       `koanf:"contracts" doc:"Schema contracts: named GraphQL schemas for different audiences, each a subset of the full schema selected by @contract / @behavior ±contract:<name> smart comments and served at <path>/graphql. Entries are {name, default: include|exclude, path (default /<name>)}, or bare names. When set, only the contracts are served (give one path: / to serve it at /graphql). Requires the smart_comments plugin."`
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

	// Aggregates is the aggregates section: the aggregates plugin's
	// settings.
	Aggregates = aggregates.Settings
)

// PluginSettings is the plugins.settings section: one typed section per
// configurable built-in plugin. The aggregates plugin is configured by the
// top-level aggregates section.
type PluginSettings struct {
	AdvancedFilters advancedfilters.Settings `koanf:"advanced_filters" doc:"advanced_filters plugin: relation filters, relational ordering and computed-column filtering and ordering."`
	Postgis         postgis.Settings         `koanf:"postgis" doc:"postgis plugin: spatial filters, accessors, projection arguments and distance ordering."`
	Contrib         contrib.Settings         `koanf:"contrib" doc:"contrib plugin: full-text, pg_trgm, hstore, ltree, range, inet, pgvector, cube and earthdistance surfaces."`
	NestedMutations nestedmutations.Settings `koanf:"nested_mutations" doc:"nested_mutations plugin: nested connect/create/update/delete/disconnect inputs on create and update mutations."`
}

// DefaultPluginSettings returns every plugin's default settings.
func DefaultPluginSettings() PluginSettings {
	return PluginSettings{
		AdvancedFilters: advancedfilters.DefaultSettings(),
		Postgis:         postgis.DefaultSettings(),
		Contrib:         contrib.DefaultSettings(),
		NestedMutations: nestedmutations.DefaultSettings(),
	}
}

// Validate returns the problems of the plugin settings.
func (p PluginSettings) Validate() []string {
	return p.NestedMutations.Validate()
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

// MovedKeys maps pdbq keys renamed before 1.0 to their replacement (on top
// of confx.MovedKeys), so loading an old config names the new key.
var MovedKeys = map[string]string{
	"plugins.settings.advanced-filters": "plugins.settings.advanced_filters",
	"plugins.settings.nested-mutations": "plugins.settings.nested_mutations",
	"plugins.settings.aggregates":       "aggregates",

	"server.disable_introspection": "server.introspection",
	"server.expose_schema":         "server.introspection",

	"server.document_cache_size":            "server.document_cache.size",
	"server.document_cache_bytes":           "server.document_cache.bytes",
	"server.document_cache_max_entry_bytes": "server.document_cache.max_entry_bytes",

	"server.max_body_bytes":          "limits.max_body_bytes",
	"server.max_depth":               "limits.max_depth",
	"server.max_cost":                "limits.max_cost",
	"server.max_page_size":           "limits.max_page_size",
	"server.max_headline_document":   "limits.max_headline_document",
	"server.max_query_tokens":        "limits.max_query_tokens",
	"server.max_input_depth":         "limits.max_input_depth",
	"server.max_selections":          "limits.max_selections",
	"server.max_introspection_bytes": "limits.max_introspection_bytes",
	"server.max_field_comparisons":   "limits.max_field_comparisons",

	"server.apq":                    "persisted_queries.apq",
	"server.persisted_queries_path": "persisted_queries.path",
	"server.persisted_only":         "persisted_queries.only",

	"server.subscriptions":                    "subscriptions.enabled",
	"server.subscriptions_max_per_connection": "subscriptions.max_per_connection",
	"server.subscriptions_max_connections":    "subscriptions.max_connections",
	"server.subscriptions_max_operations":     "subscriptions.max_operations",
	"server.subscriptions_subscribe_rate":     "subscriptions.subscribe_rate",
	"server.subscriptions_topic_prefix":       "subscriptions.topic_prefix",
	"server.subscriptions_topics":             "subscriptions.topics",
	"server.subscriptions_keepalive":          "subscriptions.keepalive",
	"server.subscriptions_buffer":             "subscriptions.buffer",

	"server.live":                    "live.enabled",
	"server.live_channel":            "live.channel",
	"server.live_install_triggers":   "live.install_triggers",
	"server.live_throttle":           "live.throttle",
	"server.live_concurrency":        "live.concurrency",
	"server.live_max_per_connection": "live.max_per_connection",
	"server.live_max_operations":     "live.max_operations",

	"server.incremental_delivery":          "incremental.enabled",
	"server.incremental_max_deferred":      "incremental.max_deferred",
	"server.incremental_stream_batch_size": "incremental.stream_batch_size",
	"server.incremental_write_timeout":     "incremental.write_timeout",
	"server.incremental_max_responses":     "incremental.max_responses",

	"schema.domain_scalars":          "schema.domains",
	"transactions.partial_mutations": "transactions.partial.enabled",
}

// Server is the HTTP server section.
type Server struct {
	Addr           string        `koanf:"addr" doc:"Listen address for the HTTP server."`
	GraphiQL       bool          `koanf:"graphiql" doc:"Serve the GraphiQL playground at / (off by default; enable for development)."`
	Introspection  bool          `koanf:"introspection" doc:"Answer __schema / __type introspection queries, serve the generated SDL at /schema.graphql and add \"Did you mean\" suggestions to validation errors. On by default, like pdbr's OpenAPI document; it reveals every table, column and relation to unauthenticated callers, so disable it in production to hide the schema shape (__typename keeps working)."`
	RequestTimeout time.Duration `koanf:"request_timeout" doc:"Overall HTTP request timeout."`
	ReadyzTimeout  time.Duration `koanf:"readyz_timeout" doc:"Timeout of the database ping behind GET /readyz; an unreachable or slow database answers 503. /healthz stays a liveness check."`
	CORSOrigins    []string      `koanf:"cors_origins" doc:"Allowed CORS origins for /graphql (exact match, or '*' for any). Empty disables CORS headers entirely."`
	Compression    bool          `koanf:"compression" doc:"Gzip responses for clients that send Accept-Encoding: gzip (off by default)."`
	ReadOnly       bool          `koanf:"read_only" doc:"Reject every mutation operation (read-only mode) for maintenance, demos or defense in depth. Also toggleable at runtime when embedding pdbq as a library (App.SetReadOnly)."`
	DocumentCache  DocumentCache `koanf:"document_cache" doc:"Cache of parsed and validated GraphQL documents, keyed by query text."`
}

// DocumentCache is the server.document_cache section.
type DocumentCache struct {
	Size          int `koanf:"size" doc:"Number of parsed and validated documents cached (least recently used evicted first, within bytes). 0 disables the cache."`
	Bytes         int `koanf:"bytes" doc:"Estimated memory the document cache may retain (query text plus parsed and validated AST); least recently used documents are evicted beyond it."`
	MaxEntryBytes int `koanf:"max_entry_bytes" doc:"Estimated size above which a parsed document is not cached (large documents gain little from caching)."`
}

// Limits is the limits section: the per-request bounds. max_body_bytes,
// max_depth, max_cost, max_page_size and max_headline_document are named
// as in pdbr.
type Limits struct {
	MaxBodyBytes          int64 `koanf:"max_body_bytes" doc:"Maximum accepted request body size in bytes."`
	MaxDepth              int   `koanf:"max_depth" doc:"Maximum GraphQL selection depth per operation."`
	MaxCost               int   `koanf:"max_cost" doc:"Maximum estimated cost per operation: each selected field costs 1 (plus its surcharges), and a list multiplies its selections by the rows it can fetch: its first/last, else max_page_size (the page a connection or list fetches when given neither), and 10 for a list without page arguments. @costMultiplier smart comments tune the estimate."`
	MaxPageSize           int   `koanf:"max_page_size" doc:"Maximum rows per page; first/last above this are rejected and it is the default page size when neither is given."`
	MaxHeadlineDocument   int   `koanf:"max_headline_document" doc:"Characters of a document a full-text headline (<field>Headline) reads: text is cut to its first max_headline_document characters, a longer json/jsonb document gets a null headline. ts_headline's work grows with the document, and the cost estimate charges each headline by this bound."`
	MaxQueryTokens        int   `koanf:"max_query_tokens" doc:"Maximum lexical tokens (names, punctuation, values, comments) in a GraphQL document; parsing stops at the first token beyond it. Bounds the parse and validation work of one request."`
	MaxInputDepth         int   `koanf:"max_input_depth" doc:"Maximum nesting of input values (list and input object literals, and variable values of list and input object types), e.g. filter not/and/or chains."`
	MaxSelections         int   `koanf:"max_selections" doc:"Maximum selections (fields, fragment spreads, inline fragments) in a GraphQL document with every fragment spread expanded; rejects fragment bombs before they are walked."`
	MaxIntrospectionBytes int   `koanf:"max_introspection_bytes" doc:"Maximum estimated JSON size in bytes of the __schema / __type fields of one operation; aliases multiply every type and field they select, so larger introspection responses are rejected while they are built."`
	MaxFieldComparisons   int   `koanf:"max_field_comparisons" doc:"Maximum field pairs validation compares to check that fields sharing a response key at one position (fragments expanded) can be merged; that check is quadratic in such repeated fields."`
}

// PersistedQueries is the persisted_queries section.
type PersistedQueries struct {
	APQ  bool   `koanf:"apq" doc:"Enable Apollo automatic persisted queries: clients send a sha256 hash in the persistedQuery extension and register the document once on a miss (in-memory cache)."`
	Path string `koanf:"path" doc:"JSON file mapping sha256 hex hashes to GraphQL documents, preloaded as persisted queries (never evicted)."`
	Only bool   `koanf:"only" doc:"Reject requests that do not reference a persisted query via the persistedQuery extension (requires apq or path)."`
}

// Subscriptions is the subscriptions section.
type Subscriptions struct {
	Enabled          bool          `koanf:"enabled" doc:"Serve PostGraphile-style simple subscriptions (Subscription.listen over LISTEN/NOTIFY) on /graphql over WebSocket, graphql-transport-ws or the legacy graphql-ws subprotocol. Off by default; while off the schema has no Subscription type."`
	MaxPerConnection int           `koanf:"max_per_connection" doc:"Maximum operations running at once on one WebSocket connection, counted until each has fully stopped; a subscribe beyond it gets an error (or waits while completed operations are still stopping)."`
	MaxConnections   int           `koanf:"max_connections" doc:"Maximum WebSocket connections at once; further upgrade requests are answered 503."`
	MaxOperations    int           `koanf:"max_operations" doc:"Maximum operations running at once over all WebSocket connections; a subscribe beyond it gets an error."`
	SubscribeRate    int           `koanf:"subscribe_rate" doc:"Subscribe messages accepted per second on one WebSocket connection, with bursts up to max_per_connection; faster clients are slowed down (their messages are read later)."`
	TopicPrefix      string        `koanf:"topic_prefix" doc:"Prefix put before the listen topic to form the PostgreSQL channel, as in pg_notify('postgraphile:' || topic, payload). The channel must fit in 63 bytes."`
	Topics           []string      `koanf:"topics" doc:"Topics clients may listen to, without the prefix; an entry ending in * allows every topic starting with the rest. Empty allows any topic of letters, digits and _ . : / - characters."`
	Keepalive        time.Duration `koanf:"keepalive" doc:"Interval of the WebSocket ping (and legacy graphql-ws ka message) on subscription connections; a peer that does not answer within it is disconnected. 0 disables."`
	Buffer           int           `koanf:"buffer" doc:"Notifications queued per subscription while its events are being resolved; a subscriber that falls further behind gets an error and its subscription ends."`
}

// Live is the live section (live queries).
type Live struct {
	Enabled          bool          `koanf:"enabled" doc:"Serve live queries (requires subscriptions.enabled): every Query field is repeated on the Subscription root, and a query operation marked with the live directive is live too; the result is re-sent whenever a table it read changes, re-run under the subscriber's identity. Change detection uses per-table triggers notifying channel. Off by default; while off the schema is unchanged."`
	Channel          string        `koanf:"channel" doc:"LISTEN channel the change triggers notify with the changed table's OID; it also names the triggers (<channel>, <channel>_truncate) and their function (<channel>_notify). Letters, digits and underscores."`
	InstallTriggers  bool          `koanf:"install_triggers" doc:"Install the change triggers at startup (and after watch-mode rebuilds) on the tables of the exposed schemas and the tables their views read; needs table ownership. Disable to install them yourself (see docs/live.md)."`
	Throttle         time.Duration `koanf:"throttle" doc:"Shortest time between two runs of one live query; changes in between are coalesced into one run at the end of the interval."`
	Concurrency      int           `koanf:"concurrency" doc:"Live query runs in flight at once across the server; further runs wait, so a burst of changes cannot take every pool connection."`
	MaxPerConnection int           `koanf:"max_per_connection" doc:"Maximum live queries running at once on one WebSocket connection (they also count against subscriptions.max_per_connection)."`
	MaxOperations    int           `koanf:"max_operations" doc:"Maximum live queries running at once over all WebSocket connections (they also count against subscriptions.max_operations)."`
}

// Incremental is the incremental section (defer and stream).
type Incremental struct {
	Enabled         bool          `koanf:"enabled" doc:"Serve the defer and stream directives of GraphQL incremental delivery: the schema declares them, a query using them is answered over HTTP as multipart/mixed (Accept: multipart/mixed) and over WebSocket as several next messages, every payload read from one repeatable read snapshot. Off by default; while off the schema declares neither directive, stream is rejected and defer is ignored (one result)."`
	MaxDeferred     int           `koanf:"max_deferred" doc:"Maximum deferred fragments and streamed fields in one operation; each one runs its own statements."`
	StreamBatchSize int           `koanf:"stream_batch_size" doc:"Items per payload of a streamed list after its initialCount items; each batch is one statement."`
	WriteTimeout    time.Duration `koanf:"write_timeout" doc:"Longest one payload of an incremental HTTP response may take to reach a client that stopped reading; the response is then aborted and its transaction rolled back, releasing the pooled connection. server.request_timeout still bounds the whole response."`
	MaxResponses    int           `koanf:"max_responses" doc:"Incremental responses (HTTP and WebSocket) holding a transaction at once, over every contract; a request beyond it is answered by one result, with the defer and stream directives ignored. Must stay below database.max_conns so other requests still get a connection."`
}

// Schema is confx.Schema plus expose_partitions and domains. It repeats the
// shared fields rather than embedding confx.Schema so composite literals
// keep naming them directly.
type Schema struct {
	Schemas          []string `koanf:"schemas" doc:"PostgreSQL schemas to expose."`
	CachePath        string   `koanf:"cache_path" doc:"Boot from this schema cache file instead of introspecting (see pdbq schema dump)."`
	Functions        bool     `koanf:"functions" doc:"Expose PostgreSQL functions as custom queries/mutations."`
	ExposePartitions bool     `koanf:"expose_partitions" doc:"Expose the partitions of partitioned tables as tables of their own. Off by default: their rows are reachable through the partitioned parent."`
	Domains          bool     `koanf:"domains" doc:"Expose each domain over a built-in scalar type as a custom scalar named after it (PostGraphile's behaviour), described by its comment and constraints. Disable to type domain columns and arguments as their base type."`
}

// Shared returns the confx.Schema part of s.
func (s Schema) Shared() confx.Schema {
	return confx.Schema{Schemas: s.Schemas, CachePath: s.CachePath, Functions: s.Functions}
}

// TX is the transactions section. Mutations always run in a transaction.
type TX struct {
	Isolation  string  `koanf:"isolation" doc:"Transaction isolation level: read_committed, repeatable_read, serializable."`
	MaxRetries int     `koanf:"max_retries" doc:"Automatic retries of a transactional operation after a serialization failure or deadlock (SQLSTATE 40001/40P01). 0 disables; the whole operation re-runs, which is safe because the failed attempt rolled back."`
	PerRequest bool    `koanf:"per_request" doc:"Use one transaction for the whole request instead of one per operation."`
	Partial    Partial `koanf:"partial" doc:"Partial success: a request opts in (extensions.partialMutations) to running each mutation root field in its own savepoint, so a failing field is reported without rolling back the others."`
}

// Partial is the transactions.partial section, named as in pdbr.
type Partial struct {
	Enabled  bool `koanf:"enabled" doc:"Accept the extensions.partialMutations opt-in. Requests asking for it while this is off are rejected."`
	MaxItems int  `koanf:"max_items" doc:"Most mutation root fields an operation running with partialMutations may have; each one is its own statement and savepoint, so this bounds the round trips one request makes. Larger operations are rejected."`
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
			Addr:           ":8080",
			Introspection:  true,
			RequestTimeout: 30 * time.Second,
			ReadyzTimeout:  2 * time.Second,
			DocumentCache: DocumentCache{
				Size:          1000,
				Bytes:         64 << 20,
				MaxEntryBytes: 1 << 20,
			},
		},
		Limits: Limits{
			MaxBodyBytes:          1 << 20,
			MaxDepth:              15,
			MaxCost:               100000,
			MaxPageSize:           100,
			MaxHeadlineDocument:   pgcontrib.MaxHeadlineDocument,
			MaxQueryTokens:        15000,
			MaxInputDepth:         32,
			MaxSelections:         10000,
			MaxIntrospectionBytes: 16 << 20,
			MaxFieldComparisons:   100000,
		},
		Subscriptions: Subscriptions{
			MaxPerConnection: 100,
			MaxConnections:   1000,
			MaxOperations:    10000,
			SubscribeRate:    100,
			TopicPrefix:      "postgraphile:",
			Keepalive:        15 * time.Second,
			Buffer:           32,
		},
		Live: Live{
			Channel:          live.DefaultChannel,
			InstallTriggers:  true,
			Throttle:         500 * time.Millisecond,
			Concurrency:      4,
			MaxPerConnection: 10,
			MaxOperations:    1000,
		},
		Incremental: Incremental{
			MaxDeferred:     16,
			StreamBatchSize: 10,
			WriteTimeout:    10 * time.Second,
			MaxResponses:    5,
		},
		Schema: Schema{
			Schemas:   []string{"public"},
			Functions: true,
			Domains:   true,
		},
		Filters:    Filters{IndexedOnly: true},
		Aggregates: aggregates.DefaultSettings(),
		Auth:       confx.DefaultAuth(ClaimHeaderPrefix),
		Roles:      confx.DefaultRoles(),
		Claims:     confx.DefaultClaims(),
		TX:         TX{Isolation: "read_committed", Partial: Partial{MaxItems: 1000}},
		Watch:      Watch{PollInterval: 5 * time.Second, Channel: "pdbq_ddl"},
		Errors:     Errors{Detail: "prod"},
		Plugins:    Plugins{Settings: DefaultPluginSettings()},
		Log:        Log{Level: "info", Format: "text"},
		Metrics:    confx.DefaultMetrics(),
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
	if err := confx.Load("PDBQ_", path, flags, &cfg, confx.WithMoved(MovedKeys)); err != nil {
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
	errs = append(errs, confx.ValidateTX(confx.TX{Isolation: c.TX.Isolation, MaxRetries: c.TX.MaxRetries})...)
	if c.TX.Partial.MaxItems < 1 {
		errs = append(errs, "transactions.partial.max_items must be >= 1")
	}
	errs = append(errs, confx.ValidateWatch(c.Watch, c.Schema.CachePath)...)
	errs = append(errs, confx.ValidateSchema(c.Schema.Shared())...)
	if c.PersistedQueries.Only && !c.PersistedQueries.APQ && c.PersistedQueries.Path == "" {
		errs = append(errs, "persisted_queries.only requires persisted_queries.apq or persisted_queries.path")
	}
	if c.Server.ReadyzTimeout <= 0 {
		errs = append(errs, "server.readyz_timeout must be > 0")
	}
	if c.Limits.MaxBodyBytes < 1 {
		errs = append(errs, "limits.max_body_bytes must be >= 1")
	}
	if c.Server.DocumentCache.Size < 0 {
		errs = append(errs, "server.document_cache.size must be >= 0")
	}
	for _, l := range []struct {
		name string
		v    int
	}{
		{"server.document_cache.bytes", c.Server.DocumentCache.Bytes},
		{"server.document_cache.max_entry_bytes", c.Server.DocumentCache.MaxEntryBytes},
		{"limits.max_depth", c.Limits.MaxDepth},
		{"limits.max_cost", c.Limits.MaxCost},
		{"limits.max_page_size", c.Limits.MaxPageSize},
		{"limits.max_headline_document", c.Limits.MaxHeadlineDocument},
		{"limits.max_query_tokens", c.Limits.MaxQueryTokens},
		{"limits.max_input_depth", c.Limits.MaxInputDepth},
		{"limits.max_selections", c.Limits.MaxSelections},
		{"limits.max_introspection_bytes", c.Limits.MaxIntrospectionBytes},
		{"limits.max_field_comparisons", c.Limits.MaxFieldComparisons},
	} {
		if l.v < 1 {
			errs = append(errs, l.name+" must be >= 1")
		}
	}
	if c.Incremental.Enabled {
		errs = append(errs, validateIncremental(c.Incremental, c.Database.MaxConns)...)
	}
	if c.Subscriptions.Enabled {
		errs = append(errs, validateSubscriptions(c.Subscriptions)...)
	}
	if c.Live.Enabled {
		errs = append(errs, validateLive(c.Live, c.Subscriptions.Enabled)...)
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
	errs = append(errs, c.Aggregates.Validate()...)
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

func validateIncremental(in Incremental, maxConns int32) []string {
	var errs []string
	if in.MaxDeferred < 1 {
		errs = append(errs, "incremental.max_deferred must be >= 1")
	}
	if in.StreamBatchSize < 1 {
		errs = append(errs, "incremental.stream_batch_size must be >= 1")
	}
	if in.WriteTimeout <= 0 {
		errs = append(errs, "incremental.write_timeout must be > 0")
	}
	if in.MaxResponses < 1 {
		errs = append(errs, "incremental.max_responses must be >= 1")
	} else if maxConns > 0 && in.MaxResponses >= int(maxConns) {
		errs = append(errs, fmt.Sprintf("incremental.max_responses (%d) must be below database.max_conns (%d)", in.MaxResponses, maxConns))
	}
	return errs
}

func validateLive(l Live, subscriptions bool) []string {
	var errs []string
	if !subscriptions {
		errs = append(errs, "live.enabled requires subscriptions.enabled")
	}
	if !live.ValidChannel(l.Channel) {
		errs = append(errs, fmt.Sprintf("live.channel: %q must be letters, digits and underscores, not starting with a digit, at most %d bytes", l.Channel, live.MaxChannelBytes))
	}
	if l.Throttle < 0 {
		errs = append(errs, "live.throttle must be >= 0")
	}
	if l.Concurrency < 1 {
		errs = append(errs, "live.concurrency must be >= 1")
	}
	if l.MaxPerConnection < 1 {
		errs = append(errs, "live.max_per_connection must be >= 1")
	}
	if l.MaxOperations < 1 {
		errs = append(errs, "live.max_operations must be >= 1")
	}
	return errs
}

func validateSubscriptions(s Subscriptions) []string {
	var errs []string
	if s.MaxPerConnection < 1 {
		errs = append(errs, "subscriptions.max_per_connection must be >= 1")
	}
	if s.MaxConnections < 1 {
		errs = append(errs, "subscriptions.max_connections must be >= 1")
	}
	if s.MaxOperations < 1 {
		errs = append(errs, "subscriptions.max_operations must be >= 1")
	}
	if s.SubscribeRate < 1 {
		errs = append(errs, "subscriptions.subscribe_rate must be >= 1")
	}
	if s.Buffer < 1 {
		errs = append(errs, "subscriptions.buffer must be >= 1")
	}
	if s.Keepalive < 0 {
		errs = append(errs, "subscriptions.keepalive must be >= 0")
	}
	if _, err := subscribe.NewTopicPolicy(s.TopicPrefix, s.Topics); err != nil {
		errs = append(errs, "subscriptions."+err.Error())
	}
	return errs
}
