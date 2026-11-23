# Configuration

pdbq layers configuration from three sources, listed here from highest
precedence to lowest:

1. **Command-line flags** — named after config paths: `--database.url`,
   `--rls.enabled=false`.
2. **Environment variables** — `PDBQ_` prefix, `_` separates levels, `__`
   preserves an underscore inside a key: `PDBQ_DATABASE_URL`,
   `PDBQ_SERVER_MAX__DEPTH=20`, `PDBQ_RLS_AUTH_JWT__SECRET=...`.
3. **YAML file** — `--config path.yaml`, defaulting to `./pdbq.yaml` when
   present.

The **complete annotated reference** lives at
[`examples/pdbq.example.yaml`](../examples/pdbq.example.yaml). It is
generated from the config structs (`pdbq config example`), so it cannot
drift from the implementation — regenerate with `make example-config`.

## Commands

```console
$ pdbq config init      # write a minimal starter pdbq.yaml
$ pdbq config validate  # check the effective config, exit non-zero on error
$ pdbq config example   # print the full annotated reference
```

## The knobs

| Key | Default | Notes |
|---|---|---|
| `database.url` | — | required |
| `database.max_conns` | 10 | maximum pooled connections |
| `database.connect_timeout` | `10s` | timeout for establishing a connection |
| `database.statement_timeout` | `30s` | per-statement timeout on every request (`0` disables) |
| `server.addr` | `:8080` | |
| `server.graphiql` | `false` | GraphiQL playground at `/` (enable for dev) |
| `server.expose_schema` | `false` | SDL at `/schema.graphql` (reveals full schema) |
| `server.readyz_timeout` | `2s` | deadline of the pool ping behind `GET /readyz`; must be `> 0` |
| `server.request_timeout` | `30s` | overall HTTP request timeout |
| `server.max_body_bytes` | 1048576 | maximum request body size (1 MiB) |
| `server.max_depth` / `max_cost` | 15 / 10000 | per-operation limits; recursive relations can tune the cost estimate with `@costMultiplier` (see [smart comments](smart-comments.md#cost-estimation-for-recursive-relations)) |
| `server.max_page_size` | 100 | largest accepted `first`/`last` (a larger value errors); also the default page size when neither is given |
| `server.max_query_tokens` | 15000 | lexical tokens per GraphQL document (names, punctuation, values, comments); parsing stops at the first one beyond it, bounding the parse and validation work of one request |
| `server.max_input_depth` | 32 | nesting of input values: list and input object literals, and variables of list or input object types (`not`/`and`/`or` filter chains, nested mutation inputs); JSON scalar values are not counted |
| `server.max_selections` | 10000 | selections (fields, fragment spreads, inline fragments) of a document with every fragment spread expanded; rejects fragment bombs before anything walks them |
| `server.max_introspection_bytes` | 16777216 | estimated JSON size of the `__schema` / `__type` fields of one operation; aliases multiply every type and field they select, so a larger response is rejected while it is built (the standard GraphiQL/Apollo query is far below it) |
| `server.max_field_comparisons` | 100000 | field pairs validation compares to check that fields sharing a response key at one position (fragments expanded) can be merged; the check is quadratic in such repeated fields |
| `server.document_cache_size` | 1000 | parsed and validated documents cached by query text (LRU; validation errors are cached too); `0` disables; dropped with the schema on a watch-mode swap |
| `server.document_cache_bytes` | 67108864 | estimated memory the document cache retains (query text plus parsed and validated AST, 64 MiB); least recently used documents are evicted beyond it |
| `server.document_cache_max_entry_bytes` | 1048576 | estimated size above which a document is not cached (1 MiB) |
| `server.cors_origins` | `[]` | allowed CORS origins (exact match, or `*`); empty disables CORS headers |
| `server.compression` | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `server.apq` | `false` | Apollo automatic persisted queries (in-memory, register-on-miss) |
| `server.persisted_queries_path` | — | JSON file of sha256 → document, preloaded as persisted queries |
| `server.persisted_only` | `false` | reject requests that don't reference a persisted query (requires `apq` or `persisted_queries_path`) |
| `server.read_only` | `false` | reject every mutation (maintenance, demos, defense in depth) |
| `server.disable_introspection` | `false` | reject `__schema`/`__type` queries and leave "Did you mean …" suggestions out of validation errors, so guessed names don't reveal real ones (`__typename` keeps working) |
| `server.incremental_delivery` | `false` | serve `@defer` and `@stream`: declared in the schema, queries delivered as `multipart/mixed` over HTTP (`Accept: multipart/mixed`) and as several `next` messages over WebSocket, every payload read from one repeatable read snapshot; off leaves the SDL without directives, rejects `@stream` and delivers `@defer` inline — see [Incremental delivery](incremental.md) |
| `server.incremental_max_deferred` | 16 | `@defer` fragments plus `@stream` fields one operation may apply; each runs its own statements |
| `server.incremental_stream_batch_size` | 10 | items per payload of a streamed list after its `initialCount` items; each batch is one statement |
| `server.incremental_write_timeout` | `10s` | longest one payload of an incremental HTTP response may wait for a client that stopped reading; the response is then aborted and its transaction rolled back (also capped by `server.request_timeout`) |
| `server.incremental_max_responses` | 0 | incremental responses holding a transaction at once (HTTP and WebSocket, every contract); 0 means half of `database.max_conns`, at least 1; beyond it a query is answered by one result |
| `server.subscriptions` | `false` | serve `Subscription.listen` over WebSocket on `/graphql` (`graphql-transport-ws`, legacy `graphql-ws`); off leaves the schema without a `Subscription` type — see [Subscriptions](subscriptions.md) |
| `server.subscriptions_max_per_connection` | 100 | operations running at once per WebSocket, counted until each has stopped; one more gets an `error` message, or waits while completed operations are still stopping |
| `server.subscriptions_max_connections` | 1000 | WebSocket connections at once; further upgrades get `503` |
| `server.subscriptions_max_operations` | 10000 | operations running at once over all WebSocket connections; one more gets an `error` message |
| `server.subscriptions_subscribe_rate` | 100 | subscribe messages started per second per WebSocket, in bursts of up to `subscriptions_max_per_connection`; a faster client's messages are read later; `0` disables |
| `server.subscriptions_topic_prefix` | `postgraphile:` | channel = prefix + topic, as in `pg_notify('postgraphile:' \|\| topic, payload)`; the channel must fit in 63 bytes |
| `server.subscriptions_topics` | `[]` | topic allowlist (without the prefix); `foo:*` allows every topic starting with `foo:`; empty allows any topic of `[A-Za-z0-9_.:/-]` |
| `server.subscriptions_keepalive` | `15s` | WebSocket ping interval (plus `ka` messages on legacy `graphql-ws`); a peer silent for an interval is disconnected; `0` disables |
| `server.subscriptions_buffer` | 32 | notifications queued per subscription; a subscriber further behind gets an `error` and its subscription ends |
| `server.live` | `false` | serve live queries (requires `server.subscriptions`): every `Query` field repeated on `Subscription`, plus `query @live`; results re-sent when a table they read changes, re-run under the subscriber's identity; off leaves the schema unchanged — see [Live queries](live.md) |
| `server.live_channel` | `pdbq_live` | LISTEN channel the change triggers notify (payload: table OID); also names the triggers (`<channel>`, `<channel>_truncate`) and their function (`<channel>_notify`); `[A-Za-z0-9_]`, at most 54 bytes |
| `server.live_install_triggers` | `true` | install the change triggers at startup and after watch-mode rebuilds (needs table ownership); `false` to install them yourself |
| `server.live_throttle` | `500ms` | shortest time between two runs of one live query; changes in between coalesce into one run |
| `server.live_concurrency` | 4 | live query runs in flight at once across the server; further runs wait |
| `server.live_max_per_connection` | 10 | live queries per WebSocket (also counted in `subscriptions_max_per_connection`); one more gets an `error` message |
| `server.live_max_operations` | 1000 | live queries over all WebSocket connections (also counted in `subscriptions_max_operations`); one more gets an `error` message |
| `schema.schemas` | `[public]` | schema allowlist |
| `schema.cache_path` | — | boot from `pdbq schema dump` output |
| `schema.functions` | `true` | expose PostgreSQL functions as custom queries/mutations |
| `schema.expose_partitions` | `false` | expose partitions of partitioned tables as tables of their own (their rows are always reachable through the parent) |
| `schema.domain_scalars` | `true` | each domain over a built-in scalar type becomes a custom scalar named after it (PostGraphile's behaviour); `false` types domains as their base type. See [Domains](domains.md) |
| `filters.indexed_only` | `true` | see [filtering.md](filtering.md) |
| `filters.allow_columns` | `{}` | per-table extra filterable columns, overriding `indexed_only` |
| `rls.enabled` | `true` | see [rls.md](rls.md) for the `rls.*` and `rls.auth.*` keys |
| `transactions.mutations` | `true` | wrap each mutation in a tx (a custom mutation whose payload is read by a second statement runs in one regardless; see [Mutations](mutations.md#what-the-payload-sees)) |
| `transactions.per_request` | `false` | one tx for the whole request instead of one per operation |
| `transactions.isolation` | `read_committed` | or `repeatable_read`, `serializable`; `read_committed` uses the server's `default_transaction_isolation` (read committed unless changed): queries run in the pipeline's implicit transaction, mutations in a plain `BEGIN` |
| `transactions.max_retries` | 0 | automatic retries on serialization failure/deadlock (40001/40P01) |
| `transactions.partial_mutations` | `false` | let a request opt in (`extensions.partialMutations`) to running each mutation root field in its own savepoint, so a failing field does not roll back the others; see [Mutations](mutations.md#partial-success) |
| `watch.enabled` | `false` | dev only; refuses to combine with cache |
| `watch.poll_interval` | `5s` | fallback polling when event triggers can't be installed |
| `watch.channel` | `pdbq_ddl` | NOTIFY channel used by the DDL event trigger |
| `errors.detail` | `prod` | `dev` exposes full PG error detail; `strict` hides even constraint-violation messages |
| `plugins.disabled` | `[]` | e.g. `[simple-names]` |
| `plugins.settings.<name>` | `{}` | per-plugin config |
| `plugins.settings.aggregates.enabled` | `false` | aggregates on every table's connections; when false only `@aggregates`-tagged tables get them; see [Aggregates](aggregates.md) |
| `plugins.settings.aggregates.grouped` | `true` | `groupedAggregates(groupBy:, having:)` with its `<Type>GroupBy` enum and `<Type>HavingInput` |
| `plugins.settings.aggregates.order` | `true` | `<REL>_COUNT_*` and `<REL>_<AGGREGATE>_<COLUMN>_*` orderBy values over reverse relations |
| `plugins.settings.aggregates.filters` | `true` | `aggregates` field in `advanced-filters` to-many relation filters |
| `plugins.settings.aggregates.spatial` | `true` | `stExtent`/`stCentroid` over geometry and geography columns (false also drops the next row) |
| `plugins.settings.aggregates.spatial_union_collect` | `false` | `stUnion`/`stCollect`, whose results grow with the row set (10 × `scan_cost` each) |
| `plugins.settings.aggregates.scan_cost` | `10` | cost-estimate surcharge per aggregate field, aggregate orderBy value and relation-aggregate comparison |
| `plugins.settings.contrib.filters` | `true` | full-text, trigram, unaccent, hstore, ltree, range and inet filter operators plus `<field>Rank` fields; see [Contrib types](contrib.md) |
| `plugins.settings.contrib.ordering` | `true` | `_RANK_`/`_SIMILARITY_`/pgvector distance orderBy values and the `similarTo:`/`nearVector:` arguments; see [Contrib types](contrib.md) |
| `plugins.settings.nested-mutations.force_transactions` | `true` | run operations using nested mutation fields in a transaction even when `transactions.mutations` is false; see [Plugins](plugins.md#worked-example-4-nested-mutations) |
| `plugins.settings.nested-mutations.max_depth` | `3` | nesting levels of nested mutation fields per root field |
| `plugins.settings.nested-mutations.max_operations` | `50` | nested operations per root field (each list item counts once); more is a client error |
| `plugins.settings.postgis.typed_geometry` | `false` | spatial columns, computed columns, function results and composite attributes read as typed geometry objects (`GeometryPoint { x y }`, …) instead of the `GeoJSON` scalar; see [PostGIS](postgis.md#typed-geometry) |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `text` | `text` or `json` |
| `metrics.enabled` | `false` | serve Prometheus metrics; see [Metrics](metrics.md) |
| `metrics.path` | `/metrics` | URL path of the metrics endpoint; must not be `/graphql`, `/healthz`, `/readyz` or `/schema.graphql` when served on the main listener |
| `metrics.addr` | — | separate listen address (e.g. `:9090`) to keep metrics off the public listener; empty serves them on `server.addr` |
| `contracts` | `[]` | schema contracts: `{name, default: include\|exclude, path}` entries (or bare names), each a subset of the schema served at `<path>/graphql`; when set only the contracts are served; see [Schema contracts](contracts.md) |
