# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in [docs/compatibility.md](docs/compatibility.md).

Entries up to and including the latest 0.x release below were seeded from the
GitHub release notes; the Unreleased section is maintained by hand and
becomes the release notes of the next tag.

## [Unreleased]

### Added

- `persisted_queries.max_bytes` (default 8 MiB) and
  `persisted_queries.max_entry_bytes` (default 64 KiB) bound the APQ store
  by memory (least recently used evicted first) instead of a count of 1024
  documents of any size.

### Changed

- **Breaking:** a list returned by a set-returning function (a `SETOF` root
  function or computed column) holds at most `limits.max_page_size` items
  and the cost estimate charges it that many rows; it was unbounded and
  costed as 10 rows, so `limits.max_cost` did not bound its work
  ([upgrading](docs/upgrading.md#set-returning-function-lists-hold-at-most-a-page)).
- **Breaking:** `server.request_timeout` must be at least `1s`; `0` silently
  disabled the timeout.
- **Breaking:** `/schema.graphql` requires the credentials `/graphql`
  requires: without an anonymous role, a request without valid credentials
  is answered `401`, as an introspection query is.
- **Breaking:** an EWKT filter operand or `near:` reference whose SRID
  differs from the column's typmod SRID is rejected as `invalid_argument`
  instead of failing in PostGIS with `pg:XX000`.
- APQ documents are registered only after the request authenticated and the
  document parsed, passed the document limits and validated; unauthenticated
  clients can no longer fill the store.
- Under a transaction (role switching, claims, `transactions.per_request`),
  a database error in one query root field no longer fails the later fields
  with `pg:25P02` or adds a pathless `internal` error logged at `ERROR`: the
  query runs again with each root field in its own savepoint.
- Removed config keys (`transactions.mutations`,
  `plugins.settings.nested_mutations.force_transactions`) are reported with
  the reason they were removed, and `transactions.isolation` errors name
  the accepted values ([upgrading](docs/upgrading.md#config-messages-and-embedding-api-pdbcore-api-hygiene)).
- `PDBQ_*` variables that double the underscore right after a section
  (`PDBQ_ROLES__ALLOWED`) are errors with a did-you-mean hint instead of
  being ignored; `database.connect_timeout`, `database.max_conns`,
  `database.statement_timeout`, `auth.jwt.jwks_cache_ttl` (with a JWKS URL)
  and `watch.poll_interval` (with the watch on) are validated.
- `pdbq.LoadConfig` takes command-line values as `map[string]any` instead of
  a `*pflag.FlagSet`.
- The `@omit` / `@behavior` checks use pdbcore's shared smart-tag
  vocabulary, so pdbq and pdbr warn about the same comments.
- Built against pdbcore's API hygiene changes (see its changelog).

### Fixed

- A live boot no longer alphabetises `schema.schemas` in place; type names
  resolve through the schemas in their configured order, as on a
  schema-cache boot.

## [0.30.0] - 2026-11-24

### Added

- SECURITY.md, CONTRIBUTING.md, CHANGELOG.md and a versioning, deprecation and
  supported-versions policy ([docs/compatibility.md](docs/compatibility.md)).
- Release artifacts are accompanied by a CycloneDX SBOM, a keyless cosign
  signature of the checksums and GitHub build provenance attestations.
- Migration guide from PostGraphile v4 and v5
  ([docs/migrating-from-postgraphile.md](docs/migrating-from-postgraphile.md)).
- `pdbq query --role <name>` and `--claims '<json object>'` run an operation
  under a request's role and claims (default: the connection role without
  claims); `App.Execute` is the matching Go API.
- Every error carries a machine-readable `extensions.code` from one closed
  vocabulary shared with pdbr: HTTP error bodies, GraphQL errors and
  WebSocket messages ([docs/errors.md](docs/errors.md)).

### Changed

- **Breaking:** PostGraphile v4 naming is the default (`allUsers`,
  `userById`, `updateUserById`, forward relation `userByAuthorId`, backward
  `postsByAuthorId`). `simple_names` is opt-in through
  `plugins.settings.simple_names.enabled`; it names a forward relation over
  a single `_id`/`Id` column after the column (`author`) and a backward
  relation after the child table (`posts`) when the child has one foreign
  key to the parent. Names never depend on collisions: a relation named like
  another field of its type is a schema build error (rename it with
  `@fieldName`/`@foreignFieldName`), and a payload relation named like a
  payload field is left out of that payload. See
  [docs/upgrading.md](docs/upgrading.md#default-naming).
- **Breaking:** table mutations take PostGraphile v4's shapes: one `input`
  argument per mutation, of a type named after the field, holding
  `clientMutationId`, the key or filter and the row values
  (`createUser(input: {user})`, `updateUserById(input: {id, userPatch})`,
  `deleteUserById(input: {id})`). The row values are `UserInput` (was
  `UserCreateInput`) and `UserPatch` (was `UserUpdateInput`), without
  `clientMutationId`; a function's row argument takes `UserRowInput` (was
  `UserInput`). Bulk mutations are `createManyUsers`, `updateManyUsers` and
  `deleteManyUsers`, so uncountable table names no longer collide, and a
  table mutation name or type collision is a schema build error. See
  [docs/upgrading.md](docs/upgrading.md#mutation-shapes).
- **Breaking:** filter, orderBy and reference-argument names are unified.
  Scalar operator inputs are `*Filter` (`StringFilter`, `GeoJSONFilter`,
  was `*FilterOps`); `having` takes `and`/`or`/`not` and
  `HavingBigIntFilter`/`HavingBigFloatFilter`; existence filters are
  `<relation>Exists` in both directions; aggregate relation orderings use
  `__` (`POSTS_BY_AUTHOR_ID__COUNT_DESC`); every per-column reference is a
  field of one `near: <Type>NearInput` argument (replacing
  `near: {geometry}`, `nearVector:`, `nearPoint:`, `similarTo:` and
  `hstoreKey:`), and a distance or similarity ordering or score without its
  reference is an error. The `@behavior` tokens `-filterBy`/`-orderBy` are
  honoured (`-filter`/`-order` log a deprecation warning), `@filterable` and
  `@sortable` are separate under `advanced_filters`' opt-in modes, and
  unknown `@omit` actions and `@behavior` tokens log a build warning. See
  [docs/upgrading.md](docs/upgrading.md#filter-orderby-and-smart-tag-names).
- `go install github.com/suprbdev/pdbq/cmd/pdbq@vX.Y.Z` builds report the
  module version in `pdbq --version`.
- `pdbq query` exits 2 on GraphQL errors only after closing the pool
  (it used to call `os.Exit` and skip the cleanup).
- The quickstarts use the current `auth`/`roles` config shape and a first
  command that passes validation on defaults.
- Live queries, incremental delivery, contracts, nested mutations, partial
  mutations, typed geometry, rasters, cube/earthdistance, bit-vector
  distances, `ts_headline`, index-aware contrib ordering, polymorphic
  function resolution and the Go embedding/plugin API are marked
  experimental ([docs/compatibility.md](docs/compatibility.md)).
- **Breaking:** database error codes are `pg:<SQLSTATE>` (was the bare
  SQLSTATE); the APQ miss code is `persisted_query_not_found` (was
  `PERSISTED_QUERY_NOT_FOUND`); a keyed update or delete, root or nested,
  matching no row is `not_found` (nested ones were SQLSTATE `22P02`).
- **Breaking:** `server.request_timeout` answers `504` with a JSON body and
  code `timeout` (was `503` from `http.TimeoutHandler`); a refused role is
  `403 role_not_allowed` (was `401`); a body above `limits.max_body_bytes`
  is `413 body_too_large` (was `400`); a hash-only request with persisted
  queries off gets `PersistedQueryNotSupported` (was `400 missing query`);
  unknown paths and methods answer a JSON `404` / `405`.

For the changes between 0.x and 1.0 see [docs/upgrading.md](docs/upgrading.md).

## [0.29.0] - 2026-11-23

### Breaking: identity and config (pre-1.0)

This release reshapes the configuration ahead of 1.0. See `docs/upgrading.md` ("0.x → 1.0") for the full before → after table.

- **`rls.*` is split into `auth`, `roles` and `claims`**: `auth: {mode: jwt|headers|none, jwt: {...}, headers: {prefix: X-Pdbq-Claim-, claims, role_claim, trusted_proxies}, mint_type}`, `roles: {switch, anonymous, default, allowed}`, `claims: {prefix, json_setting}`. Env vars move from `PDBQ_RLS_*` to `PDBQ_AUTH_*` / `PDBQ_ROLES_*` / `PDBQ_CLAIMS_*`.
- **Fail closed**: headers mode requires `auth.headers.trusted_proxies`; with `roles.anonymous` empty (the new default) requests without credentials get 401 with a `WWW-Authenticate` challenge; unknown auth modes are errors. `roles.switch: false` runs as the connection role but still verifies credentials.
- **Claims**: the per-claim prefix defaults to `jwt.claims` (PostGraphile-compatible; set `claims.prefix: pdbq.claims` to keep existing policies); `claims.json_setting` (e.g. `request.jwt.claims`) also sets all claims as one JSON value for PostgREST/Supabase-style policies.
- **Strict config**: unknown keys are rejected with a moved-to or did-you-mean hint; plugin names are snake_case (`smart_comments`, `simple_names`, `advanced_filters`, `nested_mutations`); plugin settings are typed and also read from env; `log.format` must be `text` or `json`.
- **Defaults**: `database.statement_timeout` 25s (below the 30s request timeout).

### Breaking: catalog format 10

- Identity `BY DEFAULT` columns are now writable (only `GENERATED ALWAYS` identity and generated columns are read-only).
- Overloaded functions are ordered deterministically (schema, name, argument types), so the kept overload is stable.
- PostgreSQL below 14 is refused at connect.
- Schema caches are rebuilt (format 10); cache files record their writer; a minor PostgreSQL upgrade no longer reports drift.

### Fixes

- CORS adds to `Vary` instead of replacing it and exposes the response headers browsers need; `Bearer` is case-insensitive; cardinality violations (SQLSTATE class 21) are 400 instead of 500; the CLI works on a default config.

### Changes
* feat(schema)!: catalog format 10, writable identity BY DEFAULT columns (#120)
* feat(auth)!: split auth, roles and claims config; strict config (#121)

## [0.28.0] - 2026-11-23

### Security

- **Nested rows leaked hidden columns (medium).** Table rows held inside other values (arrays of rows, composite attributes, `RETURNS TABLE`/OUT columns, columns of a row type) were rendered whole as a `String`, including `@omit`/contract-hidden columns, raster bytes and rows of fully omitted tables. They are now `<Type>Row` objects with only the table's visible columns; values of hidden tables' row types get no field. Row-typed columns are no longer filterable, orderable or groupable.
- **Integrity errors naming hidden objects.** Unique/expression/partial indexes, check and exclusion constraints that reference a hidden column now get the generic message (pdbcore v0.16.0 records the columns each constraint references); constraints over visible columns keep PostgreSQL's message.
- **Headline CPU bound.** Headline documents are cut to `server.max_headline_document` characters (default 65536) and priced by that length (80 per row at the default instead of a flat 10).
- PostGIS catalog views (`geometry_columns`, …) and other extension-owned relations are no longer exposed (pdbcore v0.16.0).

### Fixes

- **Index operator classes** (PostgreSQL 19 sweep): under `filters.indexed_only`, contrib surface now requires an index with a matching operator class — an L2 HNSW index no longer enables cosine/inner-product/L1 ordering, btree_gist GiST indexes no longer enable trigram operators.
- **`NOT NULL … NOT VALID` (PostgreSQL 18+)**: such columns are nullable in the schema, since old rows may hold NULL.

### Breaking changes

- Row-typed values change from `String` to `<Type>Row` objects; row-typed columns lose filter/order/group surface.
- Contrib orderings/operators can disappear under `indexed_only` when the index's operator class doesn't match.
- Catalog cache format 9: existing schema caches are rebuilt.

### Changes
* feat(contrib): match index operator classes under indexed_only (#114)
* fix(security): project nested rows through visible columns; bound headlines (#115)

## [0.27.0] - 2026-11-23

### Highlights

- **GeoJSON precision and buffer** (pdbcore v0.14.0): spatial fields take `precision: Int` (0–15 decimal digits, bound) and `buffer: Float` (metres on geography, SRID units on geometry; negative shrinks). Applied as buffer → simplify → transform → precision; typed geometry x/y follow the precision too.
- **Full-text headlines**: `<field>Headline(document:, options:)` on tsvector columns with a rank field returns `ts_headline` snippets for the matching document column; options (`maxWords`, `minWords`, `shortWord`, `maxFragments`, markup) are validated and bound, evaluated only on returned rows and priced at 10 per row.
- **cube** (pdbcore v0.15.0): `Cube` scalar, `contains` / `containedBy` / `overlaps`, `l2` / `l1` / `chebyshev` distance ordering, scores and index-friendly `<metric>DistanceLessThan` thresholds against `nearVector:`.
- **earthdistance**: `earth` columns as `LatLon { lat, lon }`; `earthDistanceLessThan` (GiST `earth_box` prefilter), `_EARTH_DISTANCE` ordering (KNN on PostgreSQL 17+) and `<field>EarthDistance` in metres against `nearPoint:`.
- **Raster columns**: header accessors (`<field>Width`, `Height`, `NumBands`, `Srid`, `PixelWidth`, `PixelHeight`, `Envelope`), `<field>Value(point:, band:)`, `<field>SummaryStats(band:)`, and `RasterFilterOps { isNull, intersects }`. Raw raster bytes are never exposed (use a computed column to opt in).

### Notes

- Before PostgreSQL 17 the primary-key tiebreaker prevents KNN index scans for distance ordering (cube, earth, pgvector); results are correct but sorted.

### Changes
* feat(postgis): GeoJSON precision and buffer; feat(contrib): full-text headlines (#108)
* feat(contrib): cube and earthdistance; feat(postgis): raster columns (#109)

## [0.26.0] - 2026-11-23

### Security

- **Stalled incremental responses (High when `server.incremental_delivery` is on).** A client that stopped reading a multipart `@defer`/`@stream` response held a pooled connection in an open transaction indefinitely; a handful of such clients exhausted the pool. Every payload write now has a deadline (`server.incremental_write_timeout`, 10s, never past `server.request_timeout`); a missed deadline aborts the response and releases the connection. `server.incremental_max_responses` (default half of `database.max_conns`) caps concurrent incremental responses across HTTP, WebSocket and contracts; requests over the cap get a single JSON result.
- **Hidden names in integrity errors.** With `errors.detail: prod`, class-23 errors keep PostgreSQL's message only when every table, column and foreign key it names is visible in the serving schema (contract / `@omit`); otherwise a generic message with the same SQLSTATE is returned.
- **Polymorphic functions and hidden types** (pdbcore v0.13.1): `@anyelement` / `@anycompatible` no longer resolve to a contract-hidden or fully omitted table's type, nor to types outside the introspected catalog.
- `distinctOn` offset cursors and offset-mode `pageInfo` now bind the offset instead of writing it into the SQL text.

### Highlights

- **hstore / ltree / fuzzystrmatch / intarray** (pdbcore v0.13.0): `<field>Value(key:)` with filtering and `hstoreKey:` ordering, `containsDefinedKey`; ltree `<field>Nlevel`, `Parent`, `Lca`, `Subpath`; `levenshteinDistanceLessThan`, Levenshtein ordering and `<field>LevenshteinDistance` against `similarTo:`, `soundexEqualTo` / `metaphoneEqualTo` / `doubleMetaphoneEqualTo`; intarray `matchesQueryInt` and `<field>Icount`.
- **Typed geometry for function results** (`typed_geometry`): computed columns, query/mutation function results, OUT parameters and composite attributes get typed geometry objects (`GeometryInterface` for untyped function results, concrete types for typmod'd composite attributes).

### Fixes

- Partial mutations: "no row matched" errors carry `path` and use the field alias.

### Breaking changes

- Integrity errors for hidden objects, and for tables outside `schema.schemas`, now use generic messages in `prod` error mode.
- Polymorphic functions tagged with hidden or unknown types are no longer exposed.

### Changes
* feat(postgis): typed geometry for function results and composite attributes (#101)
* feat(contrib): hstore and ltree accessors, fuzzystrmatch, intarray (#102)
* fix(security): bound stalled incremental responses, hidden names in integrity errors, bound distinctOn offset, partial mutation error paths (#107)

## [0.25.0] - 2026-11-23

### Highlights

- **Selectable scores** (pdbcore v0.12.0): spatial columns gain `<field>Distance` (from the list's `near:` geometry; `useSpheroid: true` for metres on geometry), trigram columns gain `<field>Similarity` / `WordSimilarity` / `StrictWordSimilarity` (against `similarTo:`), vector columns gain `<field>L2Distance` / `NegativeInnerProduct` / `CosineDistance` / `L1Distance` (against `nearVector:`). Scores read the reference of the list or connection the row came through; `null` otherwise.
- **pgvector distance thresholds**: `VectorDistanceFilterOps` (`l2DistanceLessThan`, `cosineDistanceLessThan`, …) with `{vector, distance}` operands.
- **Bit vectors** (pgvector 0.7+): `<field>HammingDistance` / `JaccardDistance` fields and `_HAMMING_DISTANCE_` / `_JACCARD_DISTANCE_` ordering.

### Breaking changes

- SDL grows `<field>Distance` on spatial columns when ordering is enabled; `nearVector:` / `similarTo:` also appear on lists whose columns only have scores.

### Changes
* feat(contrib): selectable distance and similarity scores (#100)

## [0.24.0] - 2026-11-23

### Highlights

- **pgvector**: `vector`/`halfvec` columns as `[Float!]` (`sparsevec` as `String`); nearest-neighbour ordering `<COL>_L2_DISTANCE_ASC|DESC`, `_NEGATIVE_INNER_PRODUCT_`, `_COSINE_DISTANCE_`, `_L1_DISTANCE_` with a `nearVector:` argument; keyset cursors work. Note an HNSW index scan skips rows whose vector is NULL — filter with `isNull: false`.
- **unaccent** filter operators (`unaccentEqualTo`, `unaccentLike`, `unaccentIncludes`, `unaccentStartsWith`) where unindexable operators are allowed.
- **PostGIS**: `bboxIntersects2D` filter (connection-filter-postgis parity); geography `perimeter` in metres and spheroidal centroids.

### Breaking changes

- Geography `perimeter` was in degrees and centroids planar; values change.

### Changes
* feat(contrib): pgvector ordering, unaccent operators, bboxIntersects2D (#99)

## [0.23.0] - 2026-11-23

### Highlights

- **Live queries** (`server.live`, requires `server.subscriptions`; off by default): every `Query` root field is mirrored on `Subscription` (PostGraphile `--live` style), and `query @live { … }` works over graphql-transport-ws / graphql-ws. pdbq finds the tables a query reads with `EXPLAIN` under the subscriber's identity (views and RLS-referenced tables included), installs opt-in statement-level NOTIFY triggers (`server.live_install_triggers`), re-runs affected queries under each subscriber's RLS identity, throttles (`server.live_throttle`, 500ms), bounds concurrency (`server.live_concurrency`) and skips unchanged results. Caps `server.live_max_per_connection` / `server.live_max_operations`; new metrics `pdbq_live_*`. See docs/live.md.
- **Opt-in savepoints for partial success**: with `transactions.partial_mutations: true`, a mutation sent with `"extensions": {"partialMutations": true}` runs each root field in its own SAVEPOINT; a failed field returns `null` plus its error while the others commit. Default stays all-or-nothing.
- **Functions with table-row and polymorphic arguments**: row / row-array / composite-array arguments become `<Type>Input` objects (bound via `jsonb_populate_record`); volatile functions with a leading row argument become custom mutations. Polymorphic functions are exposed when tagged `@anyelement <type>` / `@anycompatible <type>`; untagged ones are skipped with one log line (previously they appeared as broken `String` arguments).

### Breaking changes

- Untagged polymorphic functions are no longer exposed.
- A null element in a composite-array argument now passes NULL (golden `composite_mutation` changed).

### Changes
* feat(mutations): opt-in savepoints for partial success (#96)
* feat(subscriptions): live queries (#98)
* feat(functions): composite and polymorphic arguments (#97)

## [0.22.0] - 2026-11-23

### Highlights

- **Faster connections and nested relations**: the page is cut in a subquery before relation laterals join onto it, and `LIMIT` is compiled as a literal integer (capped by `server.max_page_size`) so Postgres reuses a cached generic plan. In the pg-vs-pdbq benchmark, nested 3-level throughput roughly doubles (3.1k → 5.8k RPS) and cursor walks go from 16k to 25k RPS (p99 21.7 → 2.7 ms).
- **`deleted<Type>NodeId`** on delete payloads (PostGraphile v4 parity), computed in the same statement; present when the table has a primary key.
- Functions returning an anonymous `record` (no OUT parameters) are still skipped, now with one Info log line each pointing at OUT parameters / `RETURNS TABLE`.

### Notes

- `LIMIT` is now the one value written into the SQL text rather than bound; each distinct page size is its own prepared statement.

### Changes
* perf(compile): page before relation laterals; literal LIMIT (#89)
* feat(mutations): deletedNodeId on delete payloads (#91)

## [0.21.0] - 2026-11-23

### Highlights

- **Schema contracts** (pdbcore v0.8.0): named schema variants per audience (`contracts:` config). Each contract is served at its own path (`P/graphql` over POST/GET/WebSocket, `P/schema.graphql`, GraphiQL at `P/`) from a schema built on that contract's catalog view; membership via `@contract a, b` and `@behavior +contract:x` / `-contract:x` smart comments. Contracts only remove surface; hidden columns are omitted everywhere. Watch mode rebuilds every contract; metrics carry a `contract` label; `pdbq schema print --contract X`. See docs/contracts.md.
- **Incremental delivery** (`server.incremental_delivery`, off by default): `@defer` and `@stream` over `multipart/mixed` (current spec shape, plus Apollo Client 3's `deferSpec=20220824`) and graphql-ws. All payloads of a request read one `REPEATABLE READ` snapshot; `@stream` pages forward connections on keyed tables. New keys `server.incremental_max_deferred`, `server.incremental_stream_batch_size`. See docs/incremental.md.

### Fixes

- Offset-mode connection cursors (tables without a primary key) counted `offset` twice, so `after:` skipped rows.
- Nested-mutation request state no longer leaks between built schemas (watch-mode rebuilds could swap it under running executors).

### Breaking changes

- With `contracts:` configured, only the contracts' endpoints are served (use path `/` for a full-API contract).
- Offset-mode cursor values change for requests with `offset` > 0.

### Changes
* feat(contracts): serve schema contracts per audience (#83)
* feat(exec): defer and stream incremental delivery (#84)
* fix(compile): offset-mode cursors count the offset once (#87)
* test(server): wait for pending LISTENs in the subscription churn test (#88)

## [0.20.0] - 2026-11-23

### Highlights

- **Introspection response cap**: `server.max_introspection_bytes` (default 16 MiB) bounds alias-amplified introspection responses (budget shared across the operation's introspection root fields).
- **Watch mode**: pdbcore v0.7.1 no longer misses DDL committed while the watcher starts; `App.WatchReady()` exposes readiness and `watch: ready` is logged.

### Changes
* fix: bound introspection response size; stabilize TestWatchHotSwap (#79)
* chore(deps): bump pdbcore to v0.7.1 (#80)

## [0.19.0] - 2026-11-23

### Security (denial of service)

- **Subscription churn** could exhaust server memory (one host reached 119 GB): completed operations kept running while their slot was freed. Slots are now held until the operation's goroutine exits; new caps `server.subscriptions_max_connections` (1000), `server.subscriptions_max_operations` (10000) and a per-connection subscribe rate `server.subscriptions_subscribe_rate` (100/s); cancelled subscribes no longer leak hub waiters; pending LISTENs are capped.
- **Exponential fragment expansion**: a ~1 KB document of self-doubling fragments could exhaust memory or CPU. Documents are walked with fragments expanded within `server.max_selections` (10000); cost/depth checks compute each fragment once and stop early.
- **Quadratic validation**: parsing uses a token limit (`server.max_query_tokens`, 15000); literals and variables are bounded by `server.max_input_depth` (32); overlapping-field comparisons are bounded by `server.max_field_comparisons` (100000).
- **Document cache sizing**: entries are weighed by retained memory (text + AST); the cache is capped by `server.document_cache_bytes` (64 MiB) and documents over `server.document_cache_max_entry_bytes` (1 MiB) aren't cached.

### Breaking changes

- Documents exceeding the new limits are rejected.

### Changes
* fix(server)!: bound subscription churn, fragment expansion and parser cost (#76)

## [0.18.0] - 2026-11-23

### Security

- **Claim spoofing in headers auth mode (fixed, pdbcore v0.7.0).** Variant claim header spellings (`X-Pdbq-Claim-User-Id` vs `X-Pdbq-Claim-User_Id`) could override claims set by a trusted gateway (reproduced end to end: reading another user's drafts). Each claim now has exactly one header spelling; malformed, repeated or colliding claim headers get 401; optional allowlist `rls.auth.header_claims`. Gateways must strip every client-supplied header with the claim prefix, in any letter case.
- **CSRF (fixed).** `POST /graphql` requires `Content-Type: application/json` (415 otherwise); `GET`/`HEAD` run queries only (405 for mutations and subscriptions, including persisted ones).
- **`@omit` enforcement (fixed).** Hidden columns and tables leaked through PostGIS derived accessors, functions returning hidden rows, `distinctOn`, unique lookups/by-key mutations, upserts overwriting `@omit update` columns, and nested mutations. All omit decisions now go through one place and are checked on every surface. `@omit read` now also implies omit filter and order (intentional divergence from PostGraphile) so hidden values can't be bisected out with range filters.
- **Schema leak with introspection disabled (fixed).** Validation errors no longer include "Did you mean …" suggestions.
- **JWKS hardening** (pdbcore v0.7.0): shared background fetches with backoff, stale keys during IdP outages, no JWKS URL in client errors.
- **Custom mutation payloads** are read in a second statement after the function runs, so relations and `query` see the function's writes.

Denial-of-service limits (subscription churn, fragment expansion, document-cache sizing, parser cost) follow in the next release.

### Breaking changes

- POST bodies must be JSON; mutations over GET are rejected.
- Headers-mode requests with malformed/repeated/colliding claim headers get 401.
- Columns with `@omit read` disappear from filters, conditions, orderBy, distinctOn and aggregates; keys containing hidden columns no longer generate lookups, by-key mutations or keyset cursors; functions returning rows of `@omit` tables are not exposed.

### Changes
* feat(subscriptions): LISTEN/NOTIFY simple subscriptions over graphql-ws (#56)
* feat(mutations)!: PostGraphile payloads for custom and table mutations (#57)
* fix(mutations): read custom mutation payloads after the function runs (#59)
* fix(server)!: CSRF-safe /graphql, no suggestions without introspection (#72)
* fix(smart-comments)!: enforce the omit smart comment on every surface (#73)
* fix(smart-comments)!: omit read implies omit filter and order; bump pdbcore to v0.7.0 (#75)

## [0.17.0] - 2026-11-23

### Highlights

- **Nested mutations, PostGraphile nested-mutations parity**: per-relation nested fields on create *and* update inputs with `connectBy<Key>` / `connectByNodeId`, `create`, `updateBy<Key>` / `updateByNodeId`, `deleteBy<Key>` / `deleteByNodeId`, `disconnectBy<Key>` / `disconnectByNodeId`, recursing up to `max_depth`. Still one SQL statement per root field, atomic even without a transaction (must-match guards), RLS and column grants respected, `max_operations` (default 50) caps request size. See `docs/plugins.md`.
- **`interval` is a structured `Interval` type** (`years months days hours minutes seconds`, PostGraphile parity) with `IntervalInput`, filters, aggregates and ordering.
- **`/readyz` pings the database** (`server.readyz_timeout`, default 2s) and returns 503 when it's unreachable; `/healthz` stays a liveness check.
- golangci-lint configuration is committed and enforced in CI.

### Breaking changes

- Nested mutation inputs changed: `connect` → `connectBy<Key>`; `<Parent>ConnectInput` → `<Parent>ConnectBy<Key>Input`; nested input types are `<Type><Rel>NestedInput` (create) and `<Type><Rel>NestedPatchInput` (update); forward update/delete only reach the currently linked row; failed must-match operations report `… matched 0 rows`; bulk create/update/upsert reject nested fields.
- `interval` values are `Interval` objects instead of strings.
- `/readyz` is no longer a liveness alias.

### Changes
* chore(lint): commit golangci-lint config (#47)
* feat(schema): interval as a structured Interval type (#48)
* feat(server): /readyz pings the database (#49)
* test(readyz): drop the unused errcheck nolint directive (#51)
* ci: run PG 14 and 19 on PRs, full matrix and docker on main (#52)
* ci: install postgresql-client only when psql is missing (#53)
* feat(nested-mutations)!: connect, update, delete and disconnect (#50)

## [0.16.0] - 2026-11-23

### Highlights

- **Prometheus `/metrics`** (off by default: `metrics.enabled`, optional separate listener `metrics.addr`): HTTP requests and latency by route class, DB statement duration/errors/retries, connection-pool stats, watch rebuilds, compile time, document-cache hits, cost-limit rejections and field errors. Bring your own registry with `pdbq.WithMetrics`. See `docs/metrics.md`.
- **Domains as named scalars** (PostGraphile parity): each domain over a built-in type becomes a custom scalar (`@name` renames it) whose description lists the domain comment and its NOT NULL / CHECK constraints; values, inputs and filters behave like the base type. See `docs/domains.md`.
- Domain-typed function arguments and results, and list values for domain-array columns, now work (they used to fall back to `String` or fail to encode).

### Breaking changes

- Schemas with domains change: columns, arguments and results typed with a domain now use the domain's scalar (e.g. `score: Percent` instead of `score: Int`). Set `schema.domain_scalars: false` to keep base types.
- Schema caches from earlier versions are rejected (catalog format 8); regenerate with `pdbq schema dump`.

### Changes
* docs: drop version tiers from the feature checklist (#42)
* feat(metrics): expose Prometheus /metrics (#43)
* feat(schema): expose domains as named scalars (#44)

## [0.15.0] - 2026-11-23

### Highlights (performance)

- **One round trip per transactional request** (`pgexec.QueryTx`): RLS identity, claims and every root field are pipelined — +54–123% req/s under RLS (single-row query 25.7k → 52.1k req/s, p50 563 → 250 µs). Multi-statement mutations keep the interactive transaction for exact semantics.
- **Responses built with `json_*` instead of `jsonb_*`** — 44–54% less DB time on nested responses (~2x req/s on `users(100){posts(10)}`); GeoJSON output ~2.3x faster (pdbcore v0.5.0 json helpers).
- **Keyset pagination** on non-PK `orderBy` is bounded by an index condition — deep pages went from ~30 ms to ~0.1 ms.
- **Schema build is linear** in table count — 3000 tables 19.4 s → 2.8 s; hub-table schemas with aggregates on went from 24 s to 1.6 s at 200 tables.
- pdbcore v0.5.0: indexed catalog lookups.

### Fixes

- Mutation payloads now see their own changes through relations (created rows in `totalCount`, updated values, deleted rows gone), including under RLS.
- Connection field aliases (`{c: totalCount}`) and selection order are respected.
- `App.Close` no longer hangs while watch mode is running.
- Range type constructor functions are no longer exposed as fields.

### Behaviour changes

- Response object keys follow selection order; `json` values are returned as stored; large `float8` values use PostgreSQL's float output (e.g. `1e+100`). Bodies are ~5% larger before compression.
- `transactions.isolation: read_committed` (and unset) runs at the server's `default_transaction_isolation` instead of an explicit `READ COMMITTED` (identical unless that default was changed).

### Changes
* Index schema IR lookups: linear schema build (#35)
* Stop the watcher before closing the pool in App.Close (#37)
* Bound keyset scans on non-PK orderBy with an index condition (#36)
* Build response JSON with json_build_object/json_agg (#38)
* Let mutation payloads see their own rows through relations (#39)
* Run transactional operations as one pipelined batch (pgexec.QueryTx) (#40)
* Adopt pdbcore v0.5.0 (#41)

## [0.14.0] - 2026-11-23

### Security

**RLS bypass through role "none" (fixed, via pdbcore v0.4.0).** Earlier versions accepted the role name `none` from a role claim (JWT or headers mode) when `rls.allowed_roles` was empty or listed it. PostgreSQL treats `SET ROLE none` as `RESET ROLE`, so such a request ran as the database connection's login role — often the table owner or a superuser — and row-level security did not apply. Role names `none`, `public` and `pg_*` are now rejected at config load (`rls.anonymous_role`, `rls.default_role`, `rls.allowed_roles`) and per request (HTTP 401 before the database is touched). Deployments should set `rls.allowed_roles` explicitly and should not connect as a superuser or table owner. Upgrading is recommended for all users of v0.13.0 and earlier.

### Highlights

- **Aggregates plugin** (opt-in, `@graphile/pg-aggregates` parity): `aggregates` on connections, `groupedAggregates(groupBy, having)`, ordering and filtering by related aggregates, spatial `stExtent`/`stCentroid` (and `stUnion`/`stCollect` behind `spatial_union_collect`). See `docs/aggregates.md`.
- **Performance**: validated GraphQL documents are cached (`server.document_cache_size`, default 1000) — up to ~8x faster per request on repeated queries; responses are written without re-encoding (root fields now follow selection order, as the spec requires).
- **Nested mutations**: payloads see rows created by the same mutation (parents, children, grandchildren); grandchild nested creates are no longer silently dropped; one connect input per referenced key.
- Forward relations whose short name collides with a column field fall back to `<table>By<Columns>` instead of disappearing.
- Custom range types map to structured range types; watch mode ignores unrelated DDL and skips rebuilds when the catalog is unchanged.
- PostGIS operand SQL is a single value-independent expression (stable prepared statements).

### Breaking changes

- Relations previously exposed as `<column>Row` (name collision workaround) are now `<table>By<Columns>`.
- Response JSON keeps PostgreSQL's spacing (`"id": 1`) and root fields follow selection order instead of alphabetical order.
- Parents referenced through more than one key gain extra `<Parent>ConnectBy<Cols>Input` types; the secondary-key foreign keys use them.
- An empty object inside `or` now matches everything (it used to be dropped and narrow the result).
- Schema caches from earlier versions are rejected (catalog format 7); regenerate with `pdbq schema dump`.

### Changes
* nested-mutations: one connect input per referenced key (#19)
* Fall back to <table>By<Columns> on forward relation name collision (#21)
* nested-mutations: payload sees rows created by the chain (#28)
* Cache validated documents; write responses without re-encoding (#31)
* Add opt-in aggregates plugin (pg-aggregates parity, spatial aggregates) (#30)
* Adopt pdbcore v0.4.0 (security: reject role none) (#33)

## [0.13.0] - 2026-11-23

### Highlights

- **Contrib extensions** (`contrib` plugin, PostGraphile-compatible names): `FullText` with `matches`/`matchesPlain`/`matchesPhrase`/`matchesWebsearch`, `<field>Rank` and `_RANK` ordering; pg_trgm `similarTo`/`wordSimilarTo`/`strictWordSimilarTo` and similarity ordering via `similarTo:`; `KeyValueHash` (hstore); ltree operators; structured ranges/multiranges (`IntRange`, `DateRange`, … with `isEmpty`); `InternetAddress`/`CidrAddress`; case-insensitive citext everywhere, including when the extension schema is off `search_path`. See `docs/contrib.md`.
- **PostgreSQL 18 temporal keys**: PERIOD foreign keys join by range overlap (child→parent becomes a connection), `WITHOUT OVERLAPS` keys get a point-in-time lookup (`at:`) instead of an exact lookup, NOT ENFORCED foreign keys. See `docs/temporal.md`.
- **Functions**: omitted arguments use their `DEFAULT`, VARIADIC arguments take lists, `OUT`/`RETURNS TABLE` functions return `<Function>Record` types, composite returns use the composite type, geometry/composite/enum/json arguments are encoded correctly.
- **PostGIS**: bare WKT and GeoJSON operands take the column's SRID; PostGIS input errors are client errors.
- Partitions are hidden by default (`schema.expose_partitions`).
- CI covers PostgreSQL 14–19.

### Breaking changes

- Range, hstore and inet columns are no longer `String`: ranges are objects (`IntRange { start { value inclusive } end isEmpty }`), hstore is `KeyValueHash`, inet/cidr are `InternetAddress`/`CidrAddress`.
- Generated names change for tables whose plural ends in `-ses` (e.g. `purchases` → `Purchase`, previously `Purchas`), and `spatial_ref_sys` list fields are now `allSpatialRefSyses`.
- Omitting a function argument that has no default is an error (it used to be passed as `NULL`).
- Composite function arguments take `<Type>Input`; composite and record results (including computed columns returning rows) are object types instead of scalars.
- Domain-over-domain columns map to the base scalar (e.g. `Int` instead of `String`).
- Partition tables are not exposed unless `schema.expose_partitions: true`.
- Temporal (`WITHOUT OVERLAPS`) keys no longer generate exact-tuple lookups or upserts.
- Schema caches from earlier versions are rejected; regenerate with `pdbq schema dump`.

### Changes
* Adopt pdbcore v0.3.0: function defaults, variadic and OUT params, partitions, column SRIDs (#14)
* Support PG18 temporal keys, PERIOD and NOT ENFORCED foreign keys (#16)
* Contrib filter surfaces: full-text, trigram, hstore, ltree, ranges, inet (#15)

## [0.12.0] - 2026-11-22

### Changes
* ci: test e2e against PostgreSQL 14-19 (#5)
* Cast geography columns to geometry for ops without a geography overload (#7)
* Expose pdbcore v0.2.0 PostGIS operators and accessors (#10)
* Add opt-in typed geometry objects (#13)

### New Contributors
* @archlemon made their first contribution in https://github.com/suprbdev/pdbq/pull/5

## [0.11.0] - 2026-09-23

Initial release.

## [0.10.0] - 2026-08-05

Initial release.

## [0.9.3] - 2026-08-02

Initial release.

## [0.9.2] - 2026-08-02

Initial release.

## [0.9.1] - 2026-08-01

Initial release.

## [0.9.0] - 2026-07-27

Initial release.

## [0.8.2] - 2026-07-26

Initial release.

## [0.8.1] - 2026-07-26

Initial release.

## [0.8.0] - 2026-07-26

Initial release.

## [0.7.0] - 2026-07-25

Initial release.

## [0.6.0] - 2026-07-25

Initial release.

## [0.5.0] - 2026-07-25

Initial release.

## [0.4.0] - 2026-07-25

Initial release.

## [0.3.1] - 2026-07-24

Initial release.

## [0.3.0] - 2026-07-23

Initial release.

## [0.2.0] - 2026-07-21

Initial release.

## [0.1.2] - 2026-07-19

Initial release.

## [0.1.1] - 2026-07-18

Initial release.

## [0.1.0] - 2026-07-17

Initial release.

[Unreleased]: https://github.com/suprbdev/pdbq/compare/v0.29.0...HEAD
[0.29.0]: https://github.com/suprbdev/pdbq/compare/v0.28.0...v0.29.0
[0.28.0]: https://github.com/suprbdev/pdbq/compare/v0.27.0...v0.28.0
[0.27.0]: https://github.com/suprbdev/pdbq/compare/v0.26.0...v0.27.0
[0.26.0]: https://github.com/suprbdev/pdbq/compare/v0.25.0...v0.26.0
[0.25.0]: https://github.com/suprbdev/pdbq/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/suprbdev/pdbq/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/suprbdev/pdbq/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/suprbdev/pdbq/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/suprbdev/pdbq/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/suprbdev/pdbq/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/suprbdev/pdbq/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/suprbdev/pdbq/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/suprbdev/pdbq/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/suprbdev/pdbq/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/suprbdev/pdbq/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/suprbdev/pdbq/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/suprbdev/pdbq/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/suprbdev/pdbq/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/suprbdev/pdbq/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/suprbdev/pdbq/compare/v0.9.3...v0.10.0
[0.9.3]: https://github.com/suprbdev/pdbq/compare/v0.9.2...v0.9.3
[0.9.2]: https://github.com/suprbdev/pdbq/compare/v0.9.1...v0.9.2
[0.9.1]: https://github.com/suprbdev/pdbq/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/suprbdev/pdbq/compare/v0.8.2...v0.9.0
[0.8.2]: https://github.com/suprbdev/pdbq/compare/v0.8.1...v0.8.2
[0.8.1]: https://github.com/suprbdev/pdbq/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/suprbdev/pdbq/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/suprbdev/pdbq/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/suprbdev/pdbq/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suprbdev/pdbq/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suprbdev/pdbq/compare/v0.3.1...v0.4.0
[0.3.1]: https://github.com/suprbdev/pdbq/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/suprbdev/pdbq/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suprbdev/pdbq/compare/v0.1.2...v0.2.0
[0.1.2]: https://github.com/suprbdev/pdbq/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/suprbdev/pdbq/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/suprbdev/pdbq/releases/tag/v0.1.0
