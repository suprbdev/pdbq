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
$ pdbq serve --database.url postgres://localhost/mydb --server.graphiql
time=... msg=introspected tables=12 enums=3 functions=2 took=38ms
time=... msg=listening addr=:8080 graphiql=true
```

## Highlights

- **Single-statement compilation** — nested selections become
  `jsonb_build_object` trees, relations become `LEFT JOIN LATERAL`, lists
  become `jsonb_agg`; PostgreSQL returns the response JSON directly.
- **RLS-aware by default** — each request runs in a transaction with
  `SET LOCAL ROLE` and claims exposed via `set_config('pdbq.claims.*', ...)`,
  from a verified JWT or trusted gateway headers.
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
  compile, request) with four built-ins proving the design:
  `smart-comments` (tune the schema with PostGraphile-style `@omit`/`@name`/
  `@primaryKey`/... tags in database `COMMENT`s, see
  [docs/smart-comments.md](docs/smart-comments.md)), `simple-names` (`users`
  instead of `allUsers`), `advanced-filters` (filter across relations and
  filter/order by computed columns), and `nested-mutations` (nested
  `create`/`connect` inputs compiled to multi-CTE statements in a forced
  transaction).
- **Pipe-friendly CLI** — `echo '{users {email}}' | pdbq query -` prints
  JSON and exits non-zero on GraphQL errors.

## Installation

Prebuilt binaries for Linux, macOS and Windows (amd64/arm64) are on the
[releases page](https://github.com/suprbdev/pdbq/releases/latest), with a
`checksums.txt` for verification:

```console
$ curl -fsSL https://github.com/suprbdev/pdbq/releases/latest/download/pdbq_0.1.0_linux_amd64.tar.gz | tar xz
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
$ pdbq config init          # writes a starter pdbq.yaml
$ pdbq serve
```

See [docs/quickstart.md](docs/quickstart.md) for the full tour.

## Documentation

| Doc | Contents |
|---|---|
| [docs/features.md](docs/features.md) | Feature support checklist (what works, what's planned) |
| [docs/quickstart.md](docs/quickstart.md) | First run, first queries |
| [docs/configuration.md](docs/configuration.md) | Config layering; full reference in [examples/pdbq.example.yaml](examples/pdbq.example.yaml) |
| [docs/cli.md](docs/cli.md) | All subcommands, scripting patterns |
| [docs/rls.md](docs/rls.md) | Roles, JWT/header claims, policies |
| [docs/filtering.md](docs/filtering.md) | Filter operators, indexed-only policy |
| [docs/plugins.md](docs/plugins.md) | Hook surfaces, writing a plugin, built-ins as worked examples |
| [docs/caching.md](docs/caching.md) | Schema cache and CI/CD gates |
| [docs/deployment.md](docs/deployment.md) | Docker image, health checks, production checklist |

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
