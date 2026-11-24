# Configuration

pdbq layers configuration from three sources, listed here from highest
precedence to lowest:

1. **Command-line flags** — named after config paths: `--database.url`,
   `--roles.switch=false`.
2. **Environment variables** — `PDBQ_` prefix, `_` separates levels, `__`
   preserves an underscore inside a key: `PDBQ_DATABASE_URL`,
   `PDBQ_LIMITS_MAX__DEPTH=20`, `PDBQ_AUTH_JWT_SECRET=...`,
   `PDBQ_PLUGINS_SETTINGS_ADVANCED__FILTERS_RELATIONS=false`. Values are
   typed like the file's: `true`/`false`, numbers, durations (`25s`) and
   comma-separated lists (`PDBQ_ROLES_ALLOWED=app_user,admin`).
3. **YAML file** — `--config path.yaml`, defaulting to `./pdbq.yaml` when
   present.

Loading is **strict**: a key in the YAML file that pdbq does not know is an
error, and so is a `PDBQ_*` variable whose first segment names (or nearly
names) a config section, such as `PDBQ_AUTH_JWT_SECRT` or the removed
`PDBQ_RLS_ENABLED`. The error names the key a removed key moved to
(`rls.enabled` → `roles.switch`) or the closest valid key ("did you mean").
Other variables with the prefix, such as `PDBQ_TEST_DATABASE_URL`, are
ignored. Upgrading from 0.x: see [Upgrading](upgrading.md).

The **complete annotated reference** lives at
[`examples/pdbq.example.yaml`](../examples/pdbq.example.yaml). It is
generated from the config structs (`pdbq config example`), so it cannot
drift from the implementation — regenerate with `make example-config`.

## Commands

```console
$ pdbq config init      # write a minimal starter pdbq.yaml (no auth: configure before production)
$ pdbq config validate  # check the effective config, exit non-zero on error
$ pdbq config example   # print the full annotated reference
```

## The knobs

Every mutation runs in a transaction. Sections shared with pdbr use the
same names: `limits.*` (`max_body_bytes`, `max_depth`, `max_cost`,
`max_page_size`, `max_headline_document`), `transactions.partial.*`,
`schema.domains` and `aggregates.enabled`.

| Key | Default | Notes |
|---|---|---|
| `database.url` | — | required |
| `database.max_conns` | 10 | maximum pooled connections |
| `database.connect_timeout` | `10s` | timeout for establishing a connection |
| `database.statement_timeout` | `25s` | per-statement timeout on every request (`0` disables); kept below `server.request_timeout` so a slow statement fails with SQLSTATE `57014` (code `pg:57014`) before the request is cut off |
| `server.addr` | `:8080` | |
| `server.graphiql` | `false` | GraphiQL playground at `/` (enable for dev) |
| `server.introspection` | `true` | answer `__schema`/`__type` queries, serve the SDL at `/schema.graphql` and add "Did you mean …" suggestions to validation errors; `false` hides the schema shape so guessed names don't reveal real ones (`__typename` keeps working). On by default like pdbr's `openapi.enabled`; turn it off in production |
| `server.readyz_timeout` | `2s` | deadline of the pool ping behind `GET /readyz`; must be `> 0` |
| `server.request_timeout` | `30s` | overall HTTP request timeout; an expired request is answered `504` with code `timeout` ([errors.md](errors.md)) |
| `limits.max_body_bytes` | 1048576 | maximum request body size (1 MiB); a larger body is answered `413` with code `body_too_large` |
| `limits.max_depth` | 15 | maximum selection depth per operation |
| `limits.max_cost` | 100000 | estimated cost per operation: each field costs 1, a list multiplies its selections, a connection its `nodes`/`edges`, by `first`/`last`, or by `max_page_size` when neither is given (the page it fetches); `totalCount`, `pageInfo` and aggregates count once; recursive relations can tune the estimate with `@costMultiplier` (see [smart comments](smart-comments.md#cost-estimation-for-recursive-relations)) |
| `limits.max_page_size` | 100 | largest accepted `first`/`last` (a larger value errors); also the default page size when neither is given |
| `limits.max_headline_document` | 65536 | characters of the document a full-text headline reads (`left(doc, n)`); each headline costs 10 per started 8192 characters of it, per row (see [contrib](contrib.md#full-text-headlines)) |
| `limits.max_query_tokens` | 15000 | lexical tokens per GraphQL document (names, punctuation, values, comments); parsing stops at the first one beyond it, bounding the parse and validation work of one request |
| `limits.max_input_depth` | 32 | nesting of input values: list and input object literals, and variables of list or input object types (`not`/`and`/`or` filter chains, nested mutation inputs); JSON scalar values are not counted |
| `limits.max_selections` | 10000 | selections (fields, fragment spreads, inline fragments) of a document with every fragment spread expanded; rejects fragment bombs before anything walks them |
| `limits.max_introspection_bytes` | 16777216 | estimated JSON size of the `__schema` / `__type` fields of one operation; aliases multiply every type and field they select, so a larger response is rejected while it is built (the standard GraphiQL/Apollo query is far below it) |
| `limits.max_field_comparisons` | 100000 | field pairs validation compares to check that fields sharing a response key at one position (fragments expanded) can be merged; the check is quadratic in such repeated fields |
| `server.document_cache.size` | 1000 | parsed and validated documents cached by query text (LRU; validation errors are cached too); `0` disables; dropped with the schema on a watch-mode swap |
| `server.document_cache.bytes` | 67108864 | estimated memory the document cache retains (query text plus parsed and validated AST, 64 MiB); least recently used documents are evicted beyond it |
| `server.document_cache.max_entry_bytes` | 1048576 | estimated size above which a document is not cached (1 MiB) |
| `server.cors_origins` | `[]` | allowed CORS origins (exact match, or `*`); empty disables CORS headers |
| `server.compression` | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `persisted_queries.apq` | `false` | Apollo automatic persisted queries (in-memory, register-on-miss) |
| `persisted_queries.path` | — | JSON file of sha256 → document, preloaded as persisted queries |
| `persisted_queries.only` | `false` | reject requests that don't reference a persisted query (requires `apq` or `path`) |
| `server.read_only` | `false` | reject every mutation (maintenance, demos, defense in depth) |
| `incremental.enabled` | `false` | serve `@defer` and `@stream`: declared in the schema, queries delivered as `multipart/mixed` over HTTP (`Accept: multipart/mixed`) and as several `next` messages over WebSocket, every payload read from one repeatable read snapshot; off leaves the SDL without directives, rejects `@stream` and delivers `@defer` inline — see [Incremental delivery](incremental.md) |
| `incremental.max_deferred` | 16 | `@defer` fragments plus `@stream` fields one operation may apply; each runs its own statements |
| `incremental.stream_batch_size` | 10 | items per payload of a streamed list after its `initialCount` items; each batch is one statement |
| `incremental.write_timeout` | `10s` | longest one payload of an incremental HTTP response may wait for a client that stopped reading; the response is then aborted and its transaction rolled back (also capped by `server.request_timeout`) |
| `incremental.max_responses` | 5 | incremental responses holding a transaction at once (HTTP and WebSocket, every contract); beyond it a query is answered by one result; must be `>= 1` and below `database.max_conns` |
| `subscriptions.enabled` | `false` | serve `Subscription.listen` over WebSocket on `/graphql` (`graphql-transport-ws`, legacy `graphql-ws`); off leaves the schema without a `Subscription` type — see [Subscriptions](subscriptions.md) |
| `subscriptions.max_per_connection` | 100 | operations running at once per WebSocket, counted until each has stopped; one more gets an `error` message, or waits while completed operations are still stopping |
| `subscriptions.max_connections` | 1000 | WebSocket connections at once; further upgrades get `503` |
| `subscriptions.max_operations` | 10000 | operations running at once over all WebSocket connections; one more gets an `error` message |
| `subscriptions.subscribe_rate` | 100 | subscribe messages started per second per WebSocket, in bursts of up to `subscriptions.max_per_connection`; a faster client's messages are read later; must be `>= 1` |
| `subscriptions.topic_prefix` | `postgraphile:` | channel = prefix + topic, as in `pg_notify('postgraphile:' \|\| topic, payload)`; the channel must fit in 63 bytes |
| `subscriptions.topics` | `[]` | topic allowlist (without the prefix); `foo:*` allows every topic starting with `foo:`; empty allows any topic of `[A-Za-z0-9_.:/-]` |
| `subscriptions.keepalive` | `15s` | WebSocket ping interval (plus `ka` messages on legacy `graphql-ws`); a peer silent for an interval is disconnected; `0` disables |
| `subscriptions.buffer` | 32 | notifications queued per subscription; a subscriber further behind gets an `error` and its subscription ends |
| `live.enabled` | `false` | serve live queries (requires `subscriptions.enabled`): every `Query` field repeated on `Subscription`, plus `query @live`; results re-sent when a table they read changes, re-run under the subscriber's identity; off leaves the schema unchanged — see [Live queries](live.md) |
| `live.channel` | `pdbq_live` | LISTEN channel the change triggers notify (payload: table OID); also names the triggers (`<channel>`, `<channel>_truncate`) and their function (`<channel>_notify`); `[A-Za-z0-9_]`, at most 54 bytes |
| `live.install_triggers` | `true` | install the change triggers at startup and after watch-mode rebuilds (needs table ownership); `false` to install them yourself |
| `live.throttle` | `500ms` | shortest time between two runs of one live query; changes in between coalesce into one run |
| `live.concurrency` | 4 | live query runs in flight at once across the server; further runs wait |
| `live.max_per_connection` | 10 | live queries per WebSocket (also counted in `subscriptions.max_per_connection`); one more gets an `error` message |
| `live.max_operations` | 1000 | live queries over all WebSocket connections (also counted in `subscriptions.max_operations`); one more gets an `error` message |
| `schema.schemas` | `[public]` | schema allowlist |
| `schema.cache_path` | — | boot from `pdbq schema dump` output |
| `schema.functions` | `true` | expose PostgreSQL functions as custom queries/mutations |
| `schema.expose_partitions` | `false` | expose partitions of partitioned tables as tables of their own (their rows are always reachable through the parent) |
| `schema.domains` | `true` | each domain over a built-in scalar type becomes a custom scalar named after it (PostGraphile's behaviour); `false` types domains as their base type. See [Domains](domains.md) |
| `filters.indexed_only` | `true` | see [filtering.md](filtering.md) |
| `filters.allow_columns` | `{}` | per-table extra filterable columns, overriding `indexed_only` |
| `auth.mode` | `jwt` | credential source: `jwt` (bearer JWT), `headers` (claims from a trusted gateway) or `none` (no credentials); see [Row-level security](rls.md) |
| `auth.jwt.secret` | — | HMAC secret verifying HS256/384/512 tokens and signing minted tokens; jwt mode needs it, `secret_file` or `jwks_url` |
| `auth.jwt.secret_file` | — | file holding the HMAC secret (trailing newlines stripped), instead of `secret`; read at startup |
| `auth.jwt.jwks_url` | — | JWKS endpoint for RS*/ES* tokens; takes precedence over the secret for verification |
| `auth.jwt.jwks_cache_ttl` | `1h` | JWKS key cache lifetime; an unknown `kid` refreshes early (at most once a minute) |
| `auth.jwt.issuer` | — | expected `iss`; empty skips the check (also embedded in minted tokens) |
| `auth.jwt.audience` | — | expected `aud`; empty skips the check (also embedded in minted tokens) |
| `auth.jwt.leeway` | `0s` | clock skew tolerated on `exp`, `nbf` and `iat` |
| `auth.jwt.role_claim_path` | `role` | path of the role claim: `app_metadata.role`, `realm_access.roles[0]`, `"https://example.com/role"` |
| `auth.headers.prefix` | `X-Pdbq-Claim-` | claim header prefix in headers mode (`X-Pdbq-Claim-User-Id` is claim `user_id`) |
| `auth.headers.claims` | `[]` | claims accepted from headers (the role claim always is); empty accepts any |
| `auth.headers.role_claim` | `role` | claim carrying the role in headers mode |
| `auth.headers.trusted_proxies` | `[]` | IPs or CIDR blocks allowed to send claim headers; **required** in headers mode |
| `auth.mint_type` | — | composite type (e.g. `public.jwt`) whose function results are minted into HS256 JWTs; needs `auth.jwt.secret` or `secret_file` |
| `roles.switch` | `true` | run each request as its resolved role (`SET LOCAL ROLE`); `false` runs as the connection role, still verifying credentials and setting claims (logs a warning) |
| `roles.anonymous` | — | role of requests without credentials; empty refuses them with 401 (and is required in auth mode `none` while `roles.switch` is on) |
| `roles.default` | — | role of authenticated requests without a role claim; empty falls back to `roles.anonymous` |
| `roles.allowed` | `[]` | roles a role claim may select; empty allows any (logs a warning); `default` and `anonymous` are always allowed |
| `claims.prefix` | `jwt.claims` | per-claim settings: claim `user_id` is `current_setting('jwt.claims.user_id', true)`; empty sets none |
| `claims.json_setting` | — | setting receiving every claim as one JSON object (`{}` without credentials), e.g. `request.jwt.claims` |
| `transactions.per_request` | `false` | one tx for the whole request instead of one per operation |
| `transactions.isolation` | `read_committed` | or `repeatable_read`, `serializable`; `read_committed` uses the server's `default_transaction_isolation` (read committed unless changed): queries run in the pipeline's implicit transaction, mutations in a plain `BEGIN` |
| `transactions.max_retries` | 0 | automatic retries on serialization failure/deadlock (40001/40P01) |
| `transactions.partial.enabled` | `false` | let a request opt in (`extensions.partialMutations`) to running each mutation root field in its own savepoint, so a failing field does not roll back the others; see [Mutations](mutations.md#partial-success). Same key as pdbr's partial bulk writes |
| `transactions.partial.max_items` | 1000 | most mutation root fields a partial request may have (each is a statement and a savepoint); more is rejected; must be `>= 1` |
| `watch.enabled` | `false` | dev only; refuses to combine with cache |
| `watch.poll_interval` | `5s` | fallback polling when event triggers can't be installed |
| `watch.channel` | `pdbq_ddl` | NOTIFY channel used by the DDL event trigger |
| `errors.detail` | `prod` | `dev` exposes full PG error detail; `strict` hides even constraint-violation messages; status and `extensions.code` never change ([errors.md](errors.md)) |
| `plugins.disabled` | `[]` | built-in plugin names to disable: `smart_comments`, `simple_names`, `advanced_filters`, `postgis`, `contrib`, `aggregates`, `nested_mutations`; any other name is an error (with a "did you mean" hint) |
| `plugins.settings.simple_names.enabled` | `false` | short names (`users`, `user(id:)`, `updateUser`, `author`, `posts`) instead of PostGraphile v4's (`allUsers`, `userById`, `updateUserById`, `userByAuthorId`, `postsByAuthorId`); see [Plugins](plugins.md#worked-example-1-simple_names) |
| `plugins.settings.advanced_filters.relations` | `true` | relation filters (forward, reverse `some`/`none`/`every`, `<rel>Exists`); see [Filtering](filtering.md) |
| `plugins.settings.advanced_filters.computed` | `true` | filtering and ordering by computed columns (not index-backed: bypasses `filters.indexed_only`) |
| `plugins.settings.advanced_filters.relation_order` | `true` | one-level relational orderBy values (`REL__COLUMN_ASC`); off too when `relations` is off |
| `plugins.settings.advanced_filters.relations_opt_in` | `false` | only foreign keys tagged `@filterable` get relation filters and orderings |
| `plugins.settings.advanced_filters.computed_opt_in` | `false` | only computed-column functions tagged `@filterable` get filter and orderBy surface |
| `aggregates.enabled` | `false` | aggregates on every table's connections; when false only `@aggregates`-tagged tables get them; the `aggregates` section configures the `aggregates` plugin (disable it with `plugins.disabled`). Same gate name as pdbr; see [Aggregates](aggregates.md) |
| `aggregates.grouped` | `true` | `groupedAggregates(groupBy:, having:)` with its `<Type>GroupBy` enum and `<Type>HavingInput` |
| `aggregates.order` | `true` | `<REL>_COUNT_*` and `<REL>_<AGGREGATE>_<COLUMN>_*` orderBy values over reverse relations |
| `aggregates.filters` | `true` | `aggregates` field in `advanced_filters` to-many relation filters |
| `aggregates.spatial` | `true` | `stExtent`/`stCentroid` over geometry and geography columns (false also drops the next row) |
| `aggregates.spatial_union_collect` | `false` | `stUnion`/`stCollect`, whose results grow with the row set (10 × `scan_cost` each) |
| `aggregates.scan_cost` | `10` | cost-estimate surcharge per aggregate field, aggregate orderBy value and relation-aggregate comparison; must be `>= 0` |
| `plugins.settings.contrib.filters` | `true` | full-text, trigram, unaccent, hstore, ltree, range, inet and cube filter operators, the vector, cube and earthdistance distance thresholds, plus `<field>Rank` fields; see [Contrib types](contrib.md) |
| `plugins.settings.contrib.ordering` | `true` | `_RANK_`/`_SIMILARITY_`/pgvector, cube and earthdistance distance orderBy values, the distance fields and the `similarTo:`/`nearVector:`/`nearPoint:` arguments; see [Contrib types](contrib.md) |
| `plugins.settings.nested_mutations.max_depth` | `3` | nesting levels of nested mutation fields per root field; must be `>= 1` |
| `plugins.settings.nested_mutations.max_operations` | `50` | nested operations per root field (each list item counts once); more is a client error; must be `>= 1` |
| `plugins.settings.postgis.filters` | `true` | `GeometryFilterOps` on spatial columns and `RasterFilterOps` on raster columns; see [PostGIS](postgis.md) |
| `plugins.settings.postgis.derived` | `true` | derived accessors (`<field>Area`, `<field>Centroid`, …) and the raster accessors |
| `plugins.settings.postgis.ordering` | `true` | distance ordering, `<field>Distance` fields and the `near:` argument |
| `plugins.settings.postgis.projection` | `true` | the `buffer:`, `simplify:`, `transform:` and `precision:` projection arguments |
| `plugins.settings.postgis.typed_geometry` | `false` | spatial columns, computed columns, function results and composite attributes read as typed geometry objects (`GeometryPoint { x y }`, …) instead of the `GeoJSON` scalar; see [PostGIS](postgis.md#typed-geometry) |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `text` | `text` or `json`; other values are rejected |
| `metrics.enabled` | `false` | serve Prometheus metrics; see [Metrics](metrics.md) |
| `metrics.path` | `/metrics` | URL path of the metrics endpoint; must not be `/graphql`, `/healthz`, `/readyz` or `/schema.graphql` when served on the main listener |
| `metrics.addr` | — | separate listen address (e.g. `:9090`) to keep metrics off the public listener; empty serves them on `server.addr` |
| `contracts` | `[]` | schema contracts: `{name, default: include\|exclude, path}` entries (or bare names), each a subset of the schema served at `<path>/graphql`; when set only the contracts are served; see [Schema contracts](contracts.md) |
