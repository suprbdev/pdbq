# Feature support

Checklist of PostgreSQL and GraphQL features pdbq supports. Checked = implemented and tested today; unchecked = planned or explicitly deferred (tier noted where one applies: v1.x = next minor releases, v2 = major-version track, stretch = nice-to-have). Use this as the reference when adding features: tick the box in the same PR that ships it.

## Introspection (pg_catalog)

- [x] Tables (including partitioned tables)
- [x] Views (read-only)
- [x] Materialized views (read-only)
- [x] Columns: type, nullability, defaults, identity/generated detection
- [x] Primary keys
- [x] Unique constraints
- [x] Foreign keys (including multi-column)
- [x] Indexes (name, columns, uniqueness, method) — drives the filter policy
- [x] Enums
- [x] Functions (args, return type, set-returning, volatility)
- [x] Comments on tables, columns, enums, functions (exposed as GraphQL descriptions), and constraints (smart comments)
- [x] RLS-enabled status per table
- [x] Granted privileges per table (SELECT/INSERT/UPDATE/DELETE gate generation)
- [x] Schema allowlist (`schema.schemas`)
- [x] Composite types read into the catalog
- [x] Composite-type columns mapped to GraphQL object types (matching `<Type>Input` on mutations; composite arrays included)
- [ ] Domains surfaced as named scalars (currently resolved to base type) (v1.x)
- [ ] Function argument default values / variadic args (v1.x)
- [ ] Functions with `OUT`/`INOUT` parameters or anonymous record returns (v1.x)

## Type mapping (Postgres → GraphQL)

- [x] `int2`/`int4` → `Int`
- [x] `int8` → `BigInt` (string-serialized, no precision loss)
- [x] `float4`/`float8` → `Float`
- [x] `numeric`/`money` → `BigFloat` (string-serialized)
- [x] `bool` → `Boolean`
- [x] `text`/`varchar`/`char`/`citext`/`name` → `String`
- [x] `uuid` → `UUID`
- [x] `json`/`jsonb` → `JSON`
- [x] `timestamp`/`timestamptz` → `Datetime`
- [x] `date` → `Date`, `time`/`timetz` → `Time`
- [x] `bytea` → `String` (base64 output)
- [x] Arrays of any mapped type → GraphQL lists
- [x] Enums → GraphQL enum types (labels mapped to spec-valid names, round-tripped on input and output)
- [x] `inet`/`cidr`/`macaddr`, `interval`, ranges, `ltree`, `tsvector` → `String` fallback
- [ ] `interval` as structured scalar (v1.x)
- [ ] Range types with structured bounds (v1.x; range filter operators tracked under Filtering & ordering)
- [ ] `hstore` (v1.x)
- [ ] `ltree` operators (v1.x)
- [ ] Full-text search: `tsvector`/`tsquery` filter ops (v1.x)
- [ ] PostGIS geometry/geography with GeoJSON scalars (stretch)

## Queries

- [x] Relay connection per table (`allUsers`): `nodes`, `edges { cursor node }`, `totalCount`, `pageInfo`, with `first`/`last`/`offset`/`before`/`after`
- [x] Keyset (index-backed) cursors — cursor = nodeId (`base64(["Type", pk...])`); PK-less tables fall back to offset cursors
- [x] Node interface / global object identification (`nodeId: ID!`, Relay `node(nodeId:)`)
- [x] Single-row lookup by primary key (`userById`)
- [x] Single-row lookup per unique constraint (`userByEmail`)
- [x] Forward relations (FK → parent object field)
- [x] Backward relations (FK → child connection field with full connection args)
- [x] Arbitrarily nested relation selections, compiled to one SQL statement (lateral joins, no N+1)
- [x] Field aliases and `__typename`
- [x] Named + inline fragments
- [x] GraphQL variables with coercion and default values
- [x] Multiple root fields per operation
- [x] Depth limit and cost limit per operation
- [ ] Aggregate fields (count/sum/avg on lists) — stretch
- [ ] GraphQL subscriptions / live queries — non-goal for v1 (hook surfaces designed with this in mind; ship later)

## Filtering & ordering

- [x] `<Type>Filter` input per table with `and` / `or` / `not` combinators, arbitrarily nested
- [x] Scalar operators: `eq ne in notIn isNull lt lte gt gte`
- [x] Text operators: `like ilike startsWith endsWith` (operands escaped)
- [x] Array operators: `contains containedBy overlaps`
- [x] `jsonb` operators: `contains containedBy hasKey pathExists pathMatch`
- [x] Enum filtering with GraphQL enum values
- [x] Indexed-only policy (default): only indexed/PK/unique columns filterable and orderable
- [x] Per-table column allowlist override (`filters.allow_columns`)
- [x] Global policy switch (`filters.indexed_only: false`)
- [x] `orderBy` enums (`EMAIL_ASC`/`EMAIL_DESC`), multi-column, PK tiebreaker always appended
- [x] Filters/ordering on backward relation fields, not just root lists
- [x] `jsonb` path operators (`@?`/`@@` via `pathExists`/`pathMatch`)
- [ ] Range operators (`&&`, `@>` element, `<<`, `>>`) (v1.x, alongside structured range types)
- [x] Filtering across relations (`posts: {some: {title: {eq: ...}}}`) — built-in `advanced-filters` plugin: forward FK takes the parent's filter, reverse FK a `{some|none|every}` wrapper, compiled to EXISTS subqueries
- [x] Filtering/ordering on computed columns — built-in `advanced-filters` plugin (single-row-argument stable functions; not index-backed)
- [x] `distinctOn` (column-enum arg on connections → `SELECT DISTINCT ON`; distinct-aware `totalCount`; first/offset pagination only)

## Mutations

- [x] `create<Type>(input:)` per insertable table
- [x] `update<Type>ByPk(patch:)` per updatable table with PK
- [x] `delete<Type>ByPk` per deletable table with PK
- [x] Payload types with the mutated row selectable (relations included, compiled against the DML CTE)
- [x] Generated/identity columns excluded from inputs; defaulted columns optional
- [x] Privilege-gated generation (no INSERT grant → no create mutation)
- [x] Not-found update/delete → GraphQL error, transaction rolled back
- [x] Nested mutations via built-in plugin: `create`/`connect` of FK parents, nested `create` of children, multi-CTE single statement, bounded depth, forced transaction
- [x] Update/delete by unique constraints (`updateUserByEmail`, `deleteUserByEmail`, mirroring the lookup surface)
- [x] Upsert (`ON CONFLICT`) — `upsert<Type>By<Unique>(input:)` per non-generated key target; provided non-key columns update from `EXCLUDED`
- [x] Bulk mutations (`createUsers(input: [...])`, `updateUsers(filter:, patch:)`, `deleteUsers(filter:)`; payload = mutated rows + `affectedCount`)
- [x] `clientMutationId` passthrough (Relay classic)
- [ ] Nested `connect` on reverse relations and nested update/delete/disconnect (v1.x)

## Functions as fields

- [x] Stable/immutable functions → Query fields
- [x] Volatile functions → Mutation fields
- [x] Scalar arguments mapped from GraphQL args (named args only)
- [x] Scalar returns (via `to_jsonb`)
- [x] `SETOF <table>` returns → list of the table's object type with full selection support
- [x] `SETOF <scalar>` returns → scalar lists
- [x] Single-row table returns with selection support
- [x] `void` returns → `Boolean`
- [x] EXECUTE-privilege gating
- [x] Computed columns (stable/immutable functions whose first argument is a row type → fields on that type; extra scalar args become field args; set-returning/volatile deferred)
- [x] Set-returning computed columns (`SETOF <scalar>`/`SETOF <table>` row-type functions → list fields with full selection support)
- [ ] Functions with table-valued arguments or polymorphic types (v1.x)
- [ ] Custom mutations returning payload types with relations (v1.x)

## RLS & auth

- [x] On by default; per-operation `SET LOCAL ROLE` (never plain `SET` on pooled connections)
- [x] Claims via transaction-scoped `set_config('pdbq.claims.*', ...)`
- [x] JWT claim source (HS256/384/512, issuer/audience checks)
- [x] Trusted-header claim source for behind-gateway deployments
- [x] Anonymous role for unauthenticated requests
- [x] Role claim configurable; default role fallback
- [x] Role name validation before interpolation into `SET ROLE`
- [x] `rls.enabled: false` escape hatch with loud startup warning
- [x] JWKS / RS256 asymmetric JWT verification (`rls.auth.jwks_url` + `jwks_cache_ttl`; RS256/384/512 + ES256/384/512, kid-matched, rotation-aware cache)
- [x] Per-request role allowlist (`rls.allowed_roles`; default/anonymous roles always allowed)
- [x] Claim → GraphQL context exposure for plugins beyond `op.Claims` (`exec.OperationFromContext`/`ClaimsFromContext` — the operation rides the request context into CompileHooks)

## Transactions & execution

- [x] Every mutation in a transaction by default (`transactions.mutations`)
- [x] One transaction per operation; mutations abort remaining root fields on error
- [x] Queries transaction-free unless RLS context requires one
- [x] Plugins can force a transaction (`op.ForceTx`)
- [x] Isolation level config (read_committed / repeatable_read / serializable)
- [x] Statement timeout per connection (`database.statement_timeout`)
- [x] PG error → GraphQL error mapping with `errors.detail: dev|prod` (constraint violations pass through in prod, internals sanitized)
- [x] One transaction per request spanning operations (`transactions.per_request`)
- [x] Automatic retry on serialization failures (`transactions.max_retries`; SQLSTATE 40001/40P01, whole-operation re-run)
- [ ] Savepoints for partial mutation recovery (v1.x)

## Server & protocol

- [x] `POST /graphql` (JSON body) and `GET /graphql` (query params)
- [x] GraphQL introspection (`__schema`, `__type`, `__typename`), resolved in-process and exempt from depth/cost limits
- [x] Embedded GraphiQL playground (opt-in, off by default)
- [x] `GET /healthz` / `/readyz`
- [x] `GET /schema.graphql` (SDL export; opt-in, off by default)
- [x] Request timeout, max body size
- [x] Atomic schema hot-swap (in-flight requests finish on the old schema)
- [ ] `/metrics` (Prometheus) endpoint (v1.x)
- [x] Persisted queries / APQ (`server.apq` Apollo protocol, `server.persisted_queries_path` allowlist file, `server.persisted_only` lockdown)
- [ ] `@defer` / `@stream` (v2)
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
- [x] Built-in: `simple-names`
- [x] Built-in: `nested-mutations`
- [x] Built-in: `advanced-filters` (relation filters + computed-column filtering/ordering; halves toggle via `plugins.settings.advanced-filters.{relations,computed}`)
- [ ] Out-of-process plugins (go-plugin gRPC or wasm) — v2 track; the existing hook interfaces are the contract either way
- [x] Built-in: smart-comments plugin (`docs/smart-comments.md`) — `@omit` (per-action on tables, columns, FKs, unique constraints, functions), `@name`, `@fieldName`/`@foreignFieldName`, `@deprecated`, `@notNull`/`@nullable`, `@filterable`/`@sortable`, and logical `@primaryKey`/`@unique`/`@foreignKey` on views; `advanced-filters` honors the tags per object (`@omit filter,order` on relations/computed functions) and gains `relations_opt_in`/`computed_opt_in` for explicit `@filterable` opt-in

## Ops & delivery

- [x] Multi-stage Dockerfile → static binary on distroless, nonroot
- [x] Compose dev stack (Postgres + fixture + watch mode) and test stack
- [x] Makefile: build/dev/test/test-e2e/bench/fuzz/lint/example-config/docker-build
- [x] CI: vet, unit+golden with `-race`, e2e against Postgres 16 service, docker build
- [x] Golden-file compiler tests (`-update` regeneration)
- [x] E2E suite incl. RLS matrix and nested-mutation rollback atomicity
- [x] Fuzz: filter-input → SQL invariance (SQL text independent of values)
- [x] Benchmarks: schema build, compile per query shape
- [ ] Load benchmarks (k6/vegeta) for RPS/latency against the compose stack, tracked over time (v1.x)
- [ ] Published container image / release automation (v1.x)
- [ ] golangci-lint config committed (Makefile falls back to `go vet`) (v1.x)
