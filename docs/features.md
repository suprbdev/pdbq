# Feature support

Checklist of PostgreSQL and GraphQL features pdbq supports. Checked = implemented and tested today; unchecked = planned or explicitly deferred. Use this as the reference when adding features: tick the box in the same PR that ships it.

## Experimental features

These shipped features are outside the 1.0 compatibility promise: they may change in a minor release. Each is also marked in its own documentation. The reasons and the full policy are in [Compatibility and versioning](compatibility.md#experimental-features). Everything else in this checklist is stable. This is a stability label, not a version tier.

- Live queries (`live.enabled`): Tables are found by `EXPLAIN`, so PL/pgSQL, matview refreshes, logical replication and single-partition `TRUNCATE` are blind spots; triggers need table ownership.
- Incremental delivery (`@defer`, `@stream`): The underlying RFC is still moving; two wire shapes exist; `@stream` works on forward primary-key connections only.
- Nested mutations, update/delete/disconnect especially (`nested_mutations`): The payload does not reflect referential actions, parent `EXISTS` SELECT policies can fail, and the shapes diverge from the upstream plugin.
- Partial success (`transactions.partial.*`): A savepoint per element; the report shape is new.
- Schema contracts (`contracts`): Need smart comments; integrity-message rewriting is heuristic.
- Typed geometry (`plugins.settings.postgis.typed_geometry`): Opt-in and recent.
- Rasters, cube and earthdistance, bit-vector distances, `ts_headline`: Raster filters are neither reprojected nor costed; cube and earth nearest-neighbour tie-breaking needs PostgreSQL 17.
- Index-aware contrib ordering: Operator classes are not introspected: any GIN index is assumed to be trigram and an HNSW index is assumed to serve every metric, so an ordering can end up as a sequential scan.
- `@anyelement` and `@anycompatible` polymorphic function resolution: Recent.
- Watch mode (`watch.enabled`): A development convenience, not a production feature.
- The Go embedding and plugin API: see [Plugin author guide](plugins.md).

## Introspection (pg_catalog)

- [x] Tables (including partitioned tables; partitions are left out unless `schema.expose_partitions` is set)
- [x] Views (read-only)
- [x] Materialized views (read-only)
- [x] Columns: type, nullability, defaults, identity/generated detection (a PostgreSQL 18 `NOT NULL ... NOT VALID` column stays nullable until validated)
- [x] Primary keys
- [x] Unique constraints
- [x] Foreign keys (including multi-column)
- [x] PostgreSQL 18 temporal keys (`WITHOUT OVERLAPS`), temporal foreign keys (`PERIOD`) and `NOT ENFORCED` foreign keys — see [Temporal keys](temporal.md)
- [x] Indexes (name, columns, uniqueness, method, operator classes) — drives the filter policy
- [x] Enums
- [x] Functions (args, return type, set-returning, volatility)
- [x] Comments on tables, columns, enums, functions (exposed as GraphQL descriptions), and constraints (smart comments)
- [x] RLS-enabled status per table
- [x] Granted privileges per table (SELECT/INSERT/UPDATE/DELETE gate generation)
- [x] Schema allowlist (`schema.schemas`)
- [x] Composite types read into the catalog
- [x] Composite-type columns mapped to GraphQL object types (matching `<Type>Input` on mutations; composite arrays included)
- [x] Domains surfaced as named scalars (`schema.domains`, on by default as in PostGraphile; described by the domain's comment and CHECK constraints; domains over domains, arrays of domains, composite attributes, function arguments and results) — see [Domains](domains.md)
- [x] Function argument default values (omitted arguments take the `DEFAULT`; shown in the argument description) and `VARIADIC` arguments (a list)
- [x] Functions with `OUT`/`INOUT`/`RETURNS TABLE` parameters (generated `<Function>Record` object types)
- [x] Functions returning an anonymous `record` without output parameters: skipped like PostGraphile, with one startup log line per function

## Type mapping (Postgres → GraphQL)

- [x] `int2`/`int4` → `Int`
- [x] `int8` → `BigInt` (string-serialized, no precision loss)
- [x] `float4`/`float8` → `Float`
- [x] `numeric`/`money` → `BigFloat` (string-serialized)
- [x] `bool` → `Boolean`
- [x] `text`/`varchar`/`char`/`citext`/`name` → `String` (`citext` comparisons stay case-insensitive with the extension off the search_path)
- [x] `uuid` → `UUID`
- [x] `json`/`jsonb` → `JSON`
- [x] `timestamp`/`timestamptz` → `Datetime`
- [x] `date` → `Date`, `time`/`timetz` → `Time`
- [x] `bytea` → `String` (base64 output)
- [x] Arrays of any mapped type → GraphQL lists
- [x] Enums → GraphQL enum types (labels mapped to spec-valid names, round-tripped on input and output)
- [x] `macaddr`, custom range types → `String` fallback
- [x] `interval` → `Interval { seconds minutes hours days months years }` with an `IntervalInput` (PostGraphile shape), in columns, arrays, composites, function arguments and results, filters and aggregates — see [Intervals](interval.md)
- [x] Range and multirange types with structured bounds: `IntRange { start { value inclusive } end { … } }` (PostGraphile shape; `BigIntRange`, `BigFloatRange`, `DateRange`, `DatetimeRange`), multiranges as lists — see [Contrib types](contrib.md)
- [x] `hstore` → `KeyValueHash` JSON object, with `containsKey containsAllKeys containsAnyKeys contains containedBy containsDefinedKey`; key lookup `<field>Value(key:)`, `<field>Value: {key, …StringFilterOps}` filters and `<COLUMN>_VALUE_*` ordering via `hstoreKey:` — see [Contrib types](contrib.md#hstore-values)
- [x] `ltree` / `ltree[]` with `ancestorOf descendantOf matchesLquery matchesAnyLquery matchesLtxtquery` (+ `*Any` forms); `<field>Nlevel` (also filter and `<COLUMN>_NLEVEL_*` ordering), `<field>Parent`, `<field>Lca(path:)`, `<field>Subpath(offset:, length:)`, and `<field>Lca` on `ltree[]` — see [Contrib types](contrib.md#ltree-paths)
- [x] `fuzzystrmatch`: `levenshteinDistanceLessThan soundexEqualTo metaphoneEqualTo doubleMetaphoneEqualTo` on text columns (unindexable, so only on `filters.allow_columns` columns under `indexed_only`), `<COLUMN>_LEVENSHTEIN_DISTANCE_*` ordering and `<field>LevenshteinDistance` against `similarTo:` — see [Contrib types](contrib.md#fuzzystrmatch)
- [x] `intarray`: `matchesQueryInt` (`query_int`) on `int4[]` columns (`IntListIntarrayFilterOps`, GIN/GiST-indexed or allowlisted), `<field>Icount` (also filter and ordering) — see [Contrib types](contrib.md#intarray)
- [x] `inet`/`cidr` → `InternetAddress`/`CidrAddress` with containment operators
- [x] Full-text search: `tsvector` → `FullText` with `matches matchesPlain matchesPhrase matchesWebsearch` (+ `config`), `<field>Rank` and `<COLUMN>_RANK_*` ordering, `<field>Headline(document:, options:)` snippets (`ts_headline`, bounded options, priced per row)
- [x] `pg_trgm`: `StringTrgmFilterOps` (`similarTo wordSimilarTo strictWordSimilarTo`), index-assisted `<COLUMN>_SIMILARITY_*` ordering via `similarTo:` and selectable `<field>Similarity`/`WordSimilarity`/`StrictWordSimilarity` scores against it
- [x] `unaccent`: accent- and case-insensitive `unaccentEqualTo unaccentLike unaccentIncludes unaccentStartsWith` on text columns (`StringUnaccentFilterOps`/`StringTrgmUnaccentFilterOps`; unindexable, so only on `filters.allow_columns` columns under `indexed_only`), working with the extension off the search_path
- [x] pgvector: `vector`/`halfvec` → `[Float!]`, `sparsevec` → `String` (inputs, outputs, function arguments and results), `isNull`-only filters, nearest-first `<COLUMN>_L2_DISTANCE_*`/`_NEGATIVE_INNER_PRODUCT_*`/`_COSINE_DISTANCE_*`/`_L1_DISTANCE_*` ordering via `nearVector:`, offered per HNSW/IVFFlat index under `indexed_only`, selectable `<field><Metric>` distance scores, `<distance>LessThan` threshold filters, and Hamming/Jaccard distances on `bit`/`varbit` columns — see [Contrib types](contrib.md#pgvector)
- [x] cube: `cube` → `Cube` JSON scalar (points as coordinate lists, boxes as corner pairs, text form in), `contains containedBy overlaps`, GiST-served `<distance>LessThan` thresholds, KNN `<COLUMN>_L2_DISTANCE_*`/`_L1_DISTANCE_*`/`_CHEBYSHEV_DISTANCE_*` ordering via `nearVector:` and `<field><Metric>` scores — see [Contrib types](contrib.md#cube)
- [x] earthdistance: `earth` columns → `LatLon { lat lon }` (input `LatLonInput`), `earthDistanceLessThan` radius filter, KNN `<COLUMN>_EARTH_DISTANCE_*` ordering (chord distance, keyset-safe) and `<field>EarthDistance` metres via `nearPoint:`; `<@>` statute-mile ordering, score and filter on admitted `point` columns — see [Contrib types](contrib.md#earthdistance)
- [x] PostGIS `geometry`/`geography` → `GeoJSON` scalar (GeoJSON out; GeoJSON or WKT/EWKT in), with spatial filters (bounding-box incl. directional/n-D/geometry-box `bboxIntersects2D`, DE-9IM, 3D, exact/ordering equality, radius), derived accessors (area, length, perimeter and centroid, native on geography, X/Y/Z/M, start/end point, WKT/EWKT, bounds, …; type-specific ones gated by the column's typmod), projection arguments (`buffer:`, `simplify:`, `transform:`, bound GeoJSON `precision:`), distance ordering and selectable `<field>Distance` from `near:` — see [PostGIS](postgis.md)
- [x] PostGIS rasters: no raw bytes (computed columns opt in), header-only `<field>Width`/`Height`/`NumBands`/`Srid`/`PixelWidth`/`PixelHeight`/`Envelope`, `<field>Value(point:, band:)` (`ST_Value`, reprojected point), `<field>SummaryStats(band:)`, `RasterFilterOps { isNull intersects }` (allowlist under `indexed_only`), priced per row by the cost estimator — see [PostGIS](postgis.md#rasters)
- [x] Opt-in typed geometry objects (`plugins.settings.postgis.typed_geometry`): `GeometryInterface`/`GeographyInterface`, per-subtype and Z/M/ZM types chosen from the column typmod, `@graphile/postgis`-compatible shape, compiled into the same statement
- [x] Typed geometry for computed columns, function results (query and mutation) and composite attributes: `GeometryInterface`/`GeographyInterface` where no typmod is known, concrete types from a composite attribute's typmod; arrays stay `GeoJSON`

## Queries

- [x] Relay connection per table (`allUsers`): `nodes`, `edges { cursor node }`, `totalCount`, `pageInfo`, with `first`/`last`/`offset`/`before`/`after`
- [x] Keyset (index-backed) cursors — cursor = nodeId (`base64(["Type", pk...])`); PK-less tables fall back to offset cursors
- [x] Node interface / global object identification (`nodeId: ID!`, Relay `node(nodeId:)`)
- [x] Single-row lookup by primary key (`userById`)
- [x] Single-row lookup per unique constraint (`userByEmail`)
- [x] Point-in-time lookup per temporal key (`roomBookingByRoomId(roomId:, at:)` → `valid_at @> $n`; no exact-tuple lookup or upsert for `WITHOUT OVERLAPS` keys)
- [x] Forward relations (FK → parent object field)
- [x] Backward relations (FK → child connection field with full connection args)
- [x] Temporal (`PERIOD`) foreign keys joined by overlap (`&&`) in every relation surface; the forward side is a connection (a child may reference several parent rows)
- [x] Arbitrarily nested relation selections, compiled to one SQL statement (lateral joins, no N+1)
- [x] Field aliases and `__typename`
- [x] Named + inline fragments
- [x] GraphQL variables with coercion and default values
- [x] Multiple root fields per operation
- [x] Depth limit and cost limit per operation (`limits.*`; a connection without `first`/`last` is priced at the page it fetches, `limits.max_page_size`, and its `totalCount`/`pageInfo`/aggregates once); document limits (tokens, input nesting, expanded selections, field-merge comparisons) checked before validation
- [x] Aggregate fields — built-in `aggregates` plugin (opt-in, `@graphile/pg-aggregates` shape): connection `aggregates` (`sum distinctCount min max average stddevSample stddevPopulation varianceSample variancePopulation`), `groupedAggregates(groupBy:, having:)` with `_TRUNCATED_TO_HOUR`/`_DAY` timestamp derivatives, `<REL>_COUNT_*`/`<REL>_<AGGREGATE>_<COLUMN>_*` ordering, `aggregates` in to-many relation filters, PostGIS `stExtent stCentroid` (plus opt-in `stUnion stCollect`); one statement, RLS and cost limits apply — see [Aggregates](aggregates.md)
- [x] Simple subscriptions (PostGraphile `--simple-subscriptions` parity): `subscription { listen(topic:, initialEvent:) { relatedNodeId relatedNode query } }` over LISTEN/NOTIFY, served over WebSocket (`graphql-transport-ws` and legacy `graphql-ws`); one shared LISTEN connection, events resolved under each subscriber's RLS identity; opt-in via `subscriptions.enabled` — see [Subscriptions](subscriptions.md)
- [x] Live queries (PostGraphile `--live` parity): every `Query` field repeated on `Subscription`, plus `query @live`; tables found by `EXPLAIN`ing the compiled statements under the subscriber's identity (views and RLS policy tables included), change detection by per-table `NOTIFY` triggers installed opt-in, re-run under each subscriber's RLS identity with throttle, a server-wide concurrency bound and result dedupe; per-connection and server caps; opt-in via `live.enabled` — see [Live queries](live.md)

## Filtering & ordering

- [x] `<Type>Filter` input per table with `and` / `or` / `not` combinators, arbitrarily nested
- [x] Scalar operators: `equalTo notEqualTo in notIn isNull lessThan lessThanOrEqualTo greaterThan greaterThanOrEqualTo`
- [x] Text operators: `like likeInsensitive startsWith endsWith` (operands escaped)
- [x] Array operators: `contains containedBy overlaps`
- [x] `jsonb` operators: `contains containedBy containsKey pathExists pathMatch`
- [x] Enum filtering with GraphQL enum values
- [x] Indexed-only policy (default): only indexed/PK/unique columns filterable and orderable
- [x] Per-table column allowlist override (`filters.allow_columns`)
- [x] Global policy switch (`filters.indexed_only: false`)
- [x] `orderBy` enums (`EMAIL_ASC`/`EMAIL_DESC`), multi-column, PK tiebreaker always appended
- [x] Filters/ordering on backward relation fields, not just root lists
- [x] `jsonb` path operators (`@?`/`@@` via `pathExists`/`pathMatch`)
- [x] Range operators (`overlaps contains containsElement containedBy strictlyLeftOf strictlyRightOf notExtendsRightOf notExtendsLeftOf adjacentTo`), on multiranges too
- [x] Contrib operator sets respect `filters.indexed_only` per index hint (no unindexable operators; trigram operators/ordering only on GIN/GiST-indexed columns; unaccent and fuzzystrmatch operators, vector/bit distance thresholds, the earthdistance `point` surface and the ltree, hstore and intarray value filters and orderings only on allowlisted columns; each vector distance ordering only on columns led by an HNSW/IVFFlat index built for its metric; index hints matched by operator class; cube and earth thresholds and orderings only on GiST-indexed columns)
- [x] Filtering across relations (`posts: {some: {title: {equalTo: ...}}}`) — built-in `advanced_filters` plugin: forward FK takes the parent's filter, reverse FK a `{some|none|every}` wrapper, compiled to EXISTS subqueries
- [x] Filtering on relation existence (`postsExist: true`, `authorExists: false`) — Boolean variant per relation, compiled to a bare `(NOT) EXISTS`
- [x] Filtering/ordering on computed columns — built-in `advanced_filters` plugin (single-row-argument stable functions; not index-backed)
- [x] `distinctOn` (column-enum arg on connections → `SELECT DISTINCT ON`; distinct-aware `totalCount`; first/offset pagination only)

## Mutations

- [x] `create<Type>(input:)` per insertable table
- [x] `update<Type>ByPk(patch:)` per updatable table with PK
- [x] `delete<Type>ByPk` per deletable table with PK
- [x] Payload types with the mutated row selectable (relations included, compiled against the DML CTE)
- [x] PostGraphile payload fields: `query` on every payload, plus the row's forward relations and `<row>Edge(orderBy:)` (cursor = the row's nodeId) on single-row payloads, compiled into the mutation's statement — see [Mutations](mutations.md)
- [x] Payload relations and `query` fields reading the mutated table see it as it stands after the statement (created and updated rows included, deleted rows excluded) via a visibility CTE, emitted only when the payload reads the table that way
- [x] Generated and identity `ALWAYS` columns excluded from inputs; identity `BY DEFAULT`, serial and defaulted columns optional
- [x] Privilege-gated generation (no INSERT grant → no create mutation)
- [x] Not-found update/delete → GraphQL error, transaction rolled back
- [x] Nested mutations via built-in plugin (postgraphile-plugin-nested-mutations shapes): `connectBy<Key>`/`connectByNodeId`, `create`, `updateBy<Key>`/`updateByNodeId`, `deleteBy<Key>`/`deleteByNodeId`, `disconnectBy<Key>`/`disconnectByNodeId` on forward and backward relations, on create and update mutations; multi-CTE single statement, bounded depth
- [x] Update/delete by unique constraints (`updateUserByEmail`, `deleteUserByEmail`, mirroring the lookup surface)
- [x] Upsert (`ON CONFLICT`) — `upsert<Type>By<Unique>(input:)` per non-generated key target; provided non-key columns update from `EXCLUDED`
- [x] Bulk mutations (`createUsers(input: [...])`, `updateUsers(filter:, patch:)`, `deleteUsers(filter:)`; payload = mutated rows + `affectedCount`)
- [x] `clientMutationId` passthrough (Relay classic)
- [x] `deleted<Type>NodeId` on delete payloads (PostGraphile v4 name, computed from the deleted row in the same statement; absent when the table has no `nodeId`)

## Functions as fields

- [x] Stable/immutable functions → Query fields
- [x] Volatile functions → Mutation fields (`fn(input: FnInput!): FnPayload!`)
- [x] Arguments mapped from GraphQL args (named args only; called in named notation, positional for variadic functions): scalars, enums, composites and table rows (`<Type>Input`), PostGIS values (GeoJSON/WKT), arrays
- [x] Omitted arguments take the function's `DEFAULT`; an explicit `null` passes SQL `NULL`; omitting an argument without a default is a GraphQL error
- [x] Scalar returns (via `to_jsonb`)
- [x] `SETOF <table>` returns → list of the table's object type with full selection support
- [x] `SETOF <scalar>` returns → scalar lists
- [x] Single-row table returns with selection support
- [x] Composite returns → the composite's object type (except `auth.mint_type`, minted into a token string)
- [x] Rows nested in other values (`<table>[]` results and columns, composite attributes, `RETURNS TABLE` columns and columns of a row type) → `<Type>Row`, the columns the table's type exposes (no `@omit read` or contract-hidden column, no raster), built with `json_build_object`; values of an omitted or contract-hidden table's row type have no field
- [x] `OUT`/`RETURNS TABLE` record returns → `<Function>Record` object type (`[<Function>Record!]!` for `SETOF`), compiled in the same statement
- [x] `void` returns → `Boolean`
- [x] EXECUTE-privilege gating
- [x] Computed columns (stable/immutable functions whose first argument is a row type → fields on that type; extra args become field args; volatile ones are custom mutations)
- [x] Set-returning computed columns (`SETOF <scalar>`/`SETOF <table>` row-type functions → list fields with full selection support)
- [x] Functions with table-valued arguments (a row type in any position, arrays of rows or composites → `<Type>Input`; a volatile function with a leading row argument is a custom mutation) and polymorphic types (resolved by the `@anyelement` / `@anycompatible` smart comments; otherwise skipped with one log line) — see [Mutations](mutations.md#function-arguments)
- [x] Custom mutations with PostGraphile payloads: the result field named after its type (`post`, `posts`, `integer`, the `OUT` parameter's name, none for `void`; `@resultFieldName` overrides), `clientMutationId`, `query`, and for a returned table row its forward relations and `<row>Edge`; a payload that reads tables (relations, `query`, computed columns) is read by a second statement after the call, in the same transaction, so it sees the function's writes; one statement otherwise — see [Mutations](mutations.md)

## RLS & auth

- [x] On by default; per-operation `SET LOCAL ROLE` (never plain `SET` on pooled connections)
- [x] Claims via transaction-scoped `set_config('jwt.claims.*', ...)` (`claims.prefix`), plus every claim as one JSON setting (`claims.json_setting`, e.g. PostgREST's `request.jwt.claims`)
- [x] JWT claim source (HS256/384/512, issuer/audience checks, `auth.jwt.secret_file`, `leeway`, role claim paths such as `app_metadata.role`; case-insensitive `Bearer`)
- [x] Trusted-header claim source for behind-gateway deployments (`auth.headers.trusted_proxies` required; untrusted peers refused)
- [x] Opt-in anonymous role for unauthenticated requests (`roles.anonymous`; otherwise 401 with `WWW-Authenticate`)
- [x] Role claim configurable; default role fallback
- [x] Role name validation before interpolation into `SET ROLE`
- [x] `roles.switch: false` escape hatch (connection role, credentials still verified) with a startup warning
- [x] JWKS / RS256 asymmetric JWT verification (`auth.jwt.jwks_url` + `jwks_cache_ttl`; RS256/384/512 + ES256/384/512, kid-matched, rotation-aware cache)
- [x] Per-request role allowlist (`roles.allowed`; default/anonymous roles always allowed)
- [x] Claim → GraphQL context exposure for plugins beyond `op.Claims` (`exec.OperationFromContext`/`ClaimsFromContext` — the operation rides the request context into CompileHooks)

## Transactions & execution

- [x] Every mutation in a transaction
- [x] One transaction per operation; mutations abort remaining root fields on error
- [x] Queries transaction-free unless RLS context requires one
- [x] Plugins can force a transaction (`op.ForceTx`)
- [x] Transactions sent as one pipelined batch (identity, root field statements, commit): one round trip for queries, two for single-statement mutations; multi-statement mutations (and custom mutations whose payload is read by a second statement) run statement by statement
- [x] Isolation level config (read_committed / repeatable_read / serializable)
- [x] Statement timeout per connection (`database.statement_timeout`)
- [x] PG error → GraphQL error mapping with `errors.detail: dev|prod` (constraint violations pass through in prod, internals sanitized)
- [x] One transaction per request spanning operations (`transactions.per_request`)
- [x] Opt-in partial success for mutations: each root field in its own savepoint, a failed field is reported and the rest commit (`transactions.partial.enabled` + request `extensions.partialMutations`) — see [Mutations](mutations.md#partial-success)
- [x] Automatic retry on serialization failures (`transactions.max_retries`; SQLSTATE 40001/40P01, whole-operation re-run)

## Server & protocol

- [x] `POST /graphql` (JSON body) and `GET /graphql` (query params)
- [x] CSRF prevention per GraphQL-over-HTTP: `POST` requires `Content-Type: application/json` (`415` otherwise); `GET` runs queries only (`405` with `Allow: POST` for mutations and subscriptions, persisted ones included)
- [x] GraphQL introspection (`__schema`, `__type`, `__typename`), resolved in-process and exempt from depth/cost limits
- [x] `server.introspection: false`: rejects `__schema`/`__type`, stops serving `/schema.graphql` and drops "Did you mean …" suggestions from validation errors
- [x] Embedded GraphiQL playground (opt-in, off by default)
- [x] `GET /healthz` (liveness) / `/readyz` (bounded pool ping, `server.readyz_timeout`, `503` when the database is unreachable)
- [x] `GET /schema.graphql` (SDL export; served with `server.introspection`, on by default)
- [x] Request timeout, max body size
- [x] Atomic schema hot-swap (in-flight requests finish on the old schema)
- [x] `/metrics` (Prometheus) endpoint (opt-in `metrics.enabled`, optional separate `metrics.addr`; see [metrics.md](metrics.md))
- [x] Persisted queries / APQ (`persisted_queries.apq` Apollo protocol, `persisted_queries.path` allowlist file, `persisted_queries.only` lockdown)
- [x] `@defer` / `@stream` incremental delivery (opt-in `incremental.enabled`): `multipart/mixed` over HTTP (`incrementalSpec=v0.2` and `deferSpec=20220824` shapes), `next` messages over WebSocket; deferred fragments and stream batches run as their own statements in one repeatable read snapshot under the request's RLS identity — see [Incremental delivery](incremental.md)
- [x] Schema contracts — named schema variants that hide tagged tables, columns, functions and relations per audience (e.g. `public`/`partner`/`internal`), each with its own SDL export (`schema print --contract`) and served endpoint; builds on the `@omit` machinery in `smart_comments` ([contracts.md](contracts.md))
- [x] CORS configuration (`server.cors_origins`: exact-match allowlist or `*`)
- [x] Response compression (gzip, opt-in via `server.compression`)

## Schema cache & watch mode

- [x] `pdbq schema dump` — versioned, hashed, gzipped catalog snapshot
- [x] `serve --schema.cache_path` — boot without touching pg_catalog
- [x] `pdbq schema check` — CI drift gate (non-zero exit on drift)
- [x] Format-version and corruption rejection on load
- [x] Watch mode: DDL event trigger + `LISTEN/NOTIFY` re-introspection
- [x] Poll-hash fallback when event triggers can't be installed
- [x] Watch + cache combination rejected at config validation
- [x] Drift diff detail in `schema check` output (per-object added/removed/changed lines, column-level for tables)

## CLI & config

- [x] `pdbq serve|query|schema|config|plugins` subcommands
- [x] `pdbq query` pipe-friendly: stdin queries, `--var k=v` (JSON-parsed), `--vars-file -`, `--operation`, distinct exit code for GraphQL errors
- [x] Config layering: flags > env (`PDBQ_*`, `__` escapes underscores) > YAML
- [x] `pdbq config example` — annotated reference generated from struct tags (committed at `examples/pdbq.example.yaml`, can't drift)
- [x] `pdbq config init|validate`
- [x] `pdbq plugins list` with hook surfaces
- [x] `pdbq schema print --json` (introspection-format output)
- [x] Shell completions shipped (`pdbq completion bash|zsh|fish|powershell`, plus fixed-vocabulary flag value completion)

## Plugin system

- [x] Compile-time plugins; ordered registry (priority + registration order)
- [x] `CatalogHook` — transform the introspected catalog
- [x] `InflectionHook` — override any generated name, middleware-chained
- [x] `SchemaHook` — mutate the schema IR before SDL generation
- [x] `CompileHook` — wrap SQL generation per root field
- [x] `RequestHook` — before/after operation, context + tx control
- [x] Enable/disable/configure per plugin via `plugins.*` config
- [x] Library embedding: `pdbq.New(cfg, pdbq.WithPlugins(...))`
- [x] Collision detection with warnings at schema build
- [x] Built-in: `simple_names`
- [x] Built-in: `nested_mutations`
- [x] Built-in: `contrib` (full-text, pg_trgm, unaccent, fuzzystrmatch, hstore, ltree, intarray, range, inet and cube operators; rank, similarity, Levenshtein, pgvector, cube and earthdistance distance ordering and scores, distance thresholds; ltree, hstore and intarray value fields, filters and ordering; `plugins.settings.contrib.{filters,ordering}`)
- [x] Built-in: `advanced_filters` (relation filters + computed-column filtering/ordering; halves toggle via `plugins.settings.advanced_filters.{relations,computed}`)
- [x] Built-in: `aggregates` (connection, grouped, relational-order and relational-filter aggregates; off unless `aggregates.enabled` or an `@aggregates` table tag; see [Aggregates](aggregates.md))
- [ ] Out-of-process plugins (go-plugin gRPC or wasm) — the existing hook interfaces are the contract either way
- [x] Built-in: smart_comments plugin (`docs/smart-comments.md`) — `@omit` (per-action on tables, columns, FKs, unique constraints, functions), `@name`, `@fieldName`/`@foreignFieldName`, `@deprecated`, `@notNull`/`@nullable`, `@filterable`/`@sortable`, and logical `@primaryKey`/`@unique`/`@foreignKey` on views; `advanced_filters` honors the tags per object (`@omit filter,order` on relations/computed functions) and gains `relations_opt_in`/`computed_opt_in` for explicit `@filterable` opt-in

## Ops & delivery

- [x] Multi-stage Dockerfile → static binary on distroless, nonroot
- [x] Compose dev stack (Postgres + fixture + watch mode) and test stack
- [x] Makefile: build/dev/test/test-e2e/bench/fuzz/lint/example-config/docker-build
- [x] CI: vet, unit+golden with `-race`, e2e against a Postgres 14-19 service matrix, docker build
- [x] Golden-file compiler tests (`-update` regeneration)
- [x] E2E suite incl. RLS matrix and nested-mutation rollback atomicity
- [x] Fuzz: filter-input → SQL invariance (SQL text independent of values)
- [x] Benchmarks: schema build, compile per query shape
- [ ] Load benchmarks (k6/vegeta) for RPS/latency against the compose stack, tracked over time
- [x] Published container image / release automation (tag-driven release workflow: binaries, checksums, multi-arch images)
- [x] golangci-lint config committed (`.golangci.yaml`; CI runs it)
