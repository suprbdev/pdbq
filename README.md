# pdbq

[![CI](https://github.com/suprbdev/pdbq/actions/workflows/ci.yaml/badge.svg)](https://github.com/suprbdev/pdbq/actions/workflows/ci.yaml)
[![Release](https://img.shields.io/github/v/release/suprbdev/pdbq)](https://github.com/suprbdev/pdbq/releases/latest)
[![Go Reference](https://pkg.go.dev/badge/github.com/suprbdev/pdbq.svg)](https://pkg.go.dev/github.com/suprbdev/pdbq)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

Zero-boilerplate GraphQL API for PostgreSQL, in the spirit of PostGraphile:
point it at a live database and it introspects `pg_catalog`, generates a
GraphQL schema (types, relations, filters, pagination, CRUD mutations), and
compiles every GraphQL operation into **one** parameterized SQL statement —
no N+1, no dataloaders.

```console
$ pdbq serve --database.url postgres://localhost/mydb \
    --auth.mode none --roles.switch=false --server.graphiql
time=... msg=introspected tables=12 enums=3 functions=2 took=38ms
time=... msg=listening addr=:8080 graphiql=true
```

That first run has no authentication and runs every request as the connection
role. The defaults are the secure ones (JWT verification, one database role
per request, row-level security applies): see
[Secure setup](docs/quickstart.md#secure-setup).

## Highlights

- **Single-statement compilation** — nested selections become
  `json_build_object` trees, relations become `LEFT JOIN LATERAL`, lists
  become `json_agg`; PostgreSQL returns the response JSON directly.
- **RLS-aware by default** — each request runs in a transaction with
  `SET LOCAL ROLE` and claims exposed via `set_config('jwt.claims.*', ...)`
  (and optionally PostgREST's `request.jwt.claims` JSON), from a verified JWT
  or trusted gateway headers; requests without credentials are refused
  unless an anonymous role is configured.
- **Filters with an indexed-only policy** — only indexed columns are
  filterable out of the box (`filters.indexed_only: false` opens it up),
  with per-type operator sets (`likeInsensitive` for text, `@>` for arrays/jsonb, ...).
- **Relay connections everywhere** — every collection (`allUsers`, backward
  relations) is a cursor connection with `first`/`last`/`offset`/`before`/
  `after`, keyset-backed cursors, `totalCount` and `pageInfo`; every row type
  with a primary key implements `Node` with a global `nodeId` resolvable via
  `node(nodeId:)`.
- **Schema cache** — `pdbq schema dump` / `serve --schema.cache_path` boots
  without touching `pg_catalog`; `pdbq schema check` is a CI drift gate.
- **Watch mode** — a DDL event trigger (or poll fallback) re-introspects and
  atomically swaps the schema during development.
- **Plugins** — five small hook interfaces (catalog, inflection, schema,
  compile, request) with built-ins proving the design:
  `smart_comments` (tune the schema with PostGraphile-style `@omit`/`@name`/
  `@primaryKey`/... tags in database `COMMENT`s, see
  [docs/smart-comments.md](docs/smart-comments.md)), the opt-in `simple_names`
  (`users` and `author` instead of `allUsers` and `userByAuthorId`),
  `advanced_filters` (filter across relations and
  filter/order by computed columns), and `nested_mutations` (nested
  connect/create/update/delete/disconnect inputs on create and update
  mutations, compiled to multi-CTE statements in a forced transaction). The opt-in `aggregates` plugin adds
  `@graphile/pg-aggregates`-style `aggregates`/`groupedAggregates` on
  connections, see [docs/aggregates.md](docs/aggregates.md).
- **Subscriptions (opt-in)** — PostGraphile-style `listen(topic:)` over
  LISTEN/NOTIFY on a WebSocket (`graphql-transport-ws`), each event resolved
  under the subscriber's RLS identity, see
  [docs/subscriptions.md](docs/subscriptions.md).
- **Live queries (opt-in)** — PostGraphile-style `subscription { allPosts … }`
  (or `query @live`) re-sent whenever a table it read changes, re-run under
  each subscriber's RLS identity, see [docs/live.md](docs/live.md).
- **Schema contracts** — named schemas per audience (`public`, `partner`,
  `internal`) selected by `@contract` / `@behavior ±contract:<name>` smart
  comments, each served at its own `/<name>/graphql`, see
  [docs/contracts.md](docs/contracts.md).
- **Incremental delivery (opt-in)** — `@defer` and `@stream` over
  `multipart/mixed` and WebSocket, every payload read from one snapshot, see
  [docs/incremental.md](docs/incremental.md).
- **Pipe-friendly CLI** — `echo '{allUsers {nodes {email}}}' | pdbq query -` prints
  JSON and exits non-zero on GraphQL errors.

## Installation

Prebuilt binaries for Linux, macOS and Windows (amd64/arm64) are on the
[releases page](https://github.com/suprbdev/pdbq/releases/latest), with a
`checksums.txt` (signed with cosign), a CycloneDX SBOM and build provenance
attestations, see [SECURITY.md](SECURITY.md#verifying-releases):

```console
$ VERSION=$(curl -fsSL https://api.github.com/repos/suprbdev/pdbq/releases/latest | sed -n 's/.*"tag_name": *"v\([^"]*\)".*/\1/p')
$ curl -fsSL https://github.com/suprbdev/pdbq/releases/download/v$VERSION/pdbq_${VERSION}_linux_amd64.tar.gz | tar xz
$ ./pdbq --version
```

Or pull the (multi-arch, distroless) Docker image:

```console
$ docker run --rm ghcr.io/suprbdev/pdbq:latest --version
```

Or build from source:

```console
$ go install github.com/suprbdev/pdbq/cmd/pdbq@latest
```

## Quickstart

```console
$ docker compose up          # Postgres + fixture + pdbq
$ open http://localhost:8080 # GraphiQL (port mapping via compose.override.yaml, see docs/quickstart.md)
```

Or against your own database:

```console
$ pdbq config init          # writes pdbq.yaml: no auth, connection role (dev only)
$ $EDITOR pdbq.yaml         # set database.url
$ pdbq serve
```

`config init` writes a starter that runs without credentials; configure `auth`
and `roles` before production. [docs/quickstart.md](docs/quickstart.md) has the
full tour, including roles, a JWT and a row-level security policy.

Coming from PostGraphile? See
[docs/migrating-from-postgraphile.md](docs/migrating-from-postgraphile.md).

## Stability

pdbq follows semantic versioning. Configuration keys, the CLI, the HTTP and
GraphQL surface, [error codes](docs/errors.md) and metrics are covered by the 1.0 compatibility
promise; a few features (live queries, `@defer`/`@stream`, contracts, nested and
partial mutations, rasters and others) and the Go embedding and plugin API are
**experimental** and outside it. Details, the deprecation policy and the
supported PostgreSQL versions (14 to 19) are in
[docs/compatibility.md](docs/compatibility.md). Report vulnerabilities as
described in [SECURITY.md](SECURITY.md). See [CONTRIBUTING.md](CONTRIBUTING.md)
and [CHANGELOG.md](CHANGELOG.md).

## Documentation

| Doc | Contents |
|---|---|
| [docs/features.md](docs/features.md) | Feature support checklist (what works, what's planned) |
| [docs/quickstart.md](docs/quickstart.md) | First run, first queries |
| [docs/configuration.md](docs/configuration.md) | Config layering; full reference in [examples/pdbq.example.yaml](examples/pdbq.example.yaml) |
| [docs/cli.md](docs/cli.md) | All subcommands, scripting patterns |
| [docs/upgrading.md](docs/upgrading.md) | Breaking changes and config migration from 0.x to 1.0 |
| [docs/compatibility.md](docs/compatibility.md) | Versioning, what 1.0 freezes, experimental features, deprecation, supported versions |
| [docs/migrating-from-postgraphile.md](docs/migrating-from-postgraphile.md) | Moving from PostGraphile v4 or v5 |
| [docs/rls.md](docs/rls.md) | Roles, JWT/header claims, policies |
| [docs/filtering.md](docs/filtering.md) | Filter operators, indexed-only policy |
| [docs/errors.md](docs/errors.md) | Error format, the `extensions.code` vocabulary, HTTP statuses, `pg:<SQLSTATE>` and `errors.detail` |
| [docs/mutations.md](docs/mutations.md) | Table and custom mutations, payload fields (`query`, relations, edges), what a payload sees |
| [docs/temporal.md](docs/temporal.md) | PostgreSQL 18 temporal keys, PERIOD and NOT ENFORCED foreign keys |
| [docs/postgis.md](docs/postgis.md) | PostGIS: GeoJSON scalar, spatial filters, distance ordering, opt-in typed geometry |
| [docs/aggregates.md](docs/aggregates.md) | Opt-in aggregates: `aggregates`, `groupedAggregates`/`having`, ordering and filtering by related aggregates, spatial aggregates |
| [docs/domains.md](docs/domains.md) | Domains as custom scalars (`schema.domains`) |
| [docs/interval.md](docs/interval.md) | `interval` as a structured `Interval` object and `IntervalInput` |
| [docs/contrib.md](docs/contrib.md) | Full-text search, pg_trgm, unaccent, hstore, ltree, citext, ranges, inet, pgvector: types, filters, rank, similarity and vector distance ordering |
| [docs/plugins.md](docs/plugins.md) | Hook surfaces, writing a plugin, built-ins as worked examples |
| [docs/caching.md](docs/caching.md) | Schema cache and CI/CD gates |
| [docs/deployment.md](docs/deployment.md) | Docker image, health checks, production checklist |
| [docs/contracts.md](docs/contracts.md) | Schema contracts: per-audience schemas from `@contract` tags, endpoints, security model |
| [docs/metrics.md](docs/metrics.md) | Prometheus `/metrics`: enabling, separate listener, metric reference |
| [docs/subscriptions.md](docs/subscriptions.md) | Opt-in `listen` subscriptions over LISTEN/NOTIFY and WebSocket: protocol, auth, topics, limits, cost |
| [docs/live.md](docs/live.md) | Opt-in live queries: schema, change triggers, throttle, dedupe, caps, metrics |
| [docs/incremental.md](docs/incremental.md) | Opt-in `@defer` and `@stream`: transports, payload shapes, snapshot consistency, limits |

## Development

```console
$ make test        # unit + golden tests (no database needed)
$ make test-e2e    # end-to-end suite against a disposable Postgres
$ make bench       # schema-build and compile benchmarks
$ make fuzz        # fuzz the filter-input -> SQL path
$ make dev         # live stack with watch mode
```

Golden tests are the compiler's regression net: each `.graphql` document in
`internal/compile/testdata/` has its compiled SQL snapshotted next to it
(regenerate with `go test ./internal/compile/ -update`).
