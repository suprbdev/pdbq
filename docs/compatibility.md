# Compatibility and versioning

This page is the compatibility promise of pdbq: what a minor or patch release
will not break, what is outside the promise, how things get deprecated and which
environments are supported. It takes effect with 1.0.0. Until then every 0.x
release may break anything listed here; the names are still being finalized and
[upgrading.md](upgrading.md) lists what changed. Release notes live in the
[changelog](../CHANGELOG.md).

## Versioning

pdbq follows [Semantic Versioning](https://semver.org/). Releases are tagged
`vMAJOR.MINOR.PATCH`.

- **Patch releases** contain bug fixes and security fixes only.
- **Minor releases** add features, add configuration keys, and may change
  experimental features. They do not break anything under "What 1.0 freezes".
- **Major releases** may break the frozen surface. Each one comes with a
  migration section in `upgrading.md`.

[pdbcore](https://github.com/suprbdev/pdbcore), the engine shared with
pdbr, stays at `v0.x` and has no external compatibility promise. pdbq's
version is independent of pdbcore's: pdbq pins an exact pdbcore release in
`go.mod`, and a pdbcore minor bump that changes your surface is a pdbq minor or
major release as appropriate and is described in the changelog.

## What 1.0 freezes

- **Configuration.** Every key of the YAML file, its environment variable
  (`PDBQ_*`) and its flag, with their semantics and defaults, and the strictness
  rules (unknown keys are errors).
- **Command line.** Subcommands, flags and exit codes (`pdbq serve`, `query`,
  `schema dump|check|print`, `config init|example`, `plugins list`, `--version`).
- **HTTP surface.** Endpoints (`/graphql`, `/schema.graphql`, `/healthz`,
  `/readyz`, `/metrics`, the per-contract paths), status codes, the
  authentication challenge headers, CORS behaviour and the WebSocket
  subprotocols.
- **GraphQL surface.** The generated schema for a given database and config:
  naming (per inflector and `simple_names` setting), connection and mutation
  shapes, filter and orderBy operators, error `extensions` and codes, and the
  SQLSTATE-to-error mapping.
- **Smart tags.** The tag vocabulary of `smart_comments` and what each tag
  does.
- **Schema cache.** The cache format version rules below.
- **Metrics.** Metric names and labels.

## What is not covered

- **The Go embedding and plugin API**: the `github.com/suprbdev/pdbq` package
  (`pdbq.New`, `Config`, the hook interfaces, the type aliases of internal
  types) and the packages of the `pdbcore` module. It is **experimental**; see
  below.
- Log message text and structured log attributes.
- The exact SQL text pdbq generates (the goldens pin it for development, not for
  users), `EXPLAIN` plans and performance characteristics.
- The text of error messages (error codes and `extensions` are covered), and
  GraphiQL.
- Anything marked experimental below.

## What counts as breaking in a generated API

Because the API is derived from your database, a new table or column naturally
adds to it. The following **are not** breaking and can appear in any minor
release:

- New tables, columns, functions and relations in your database appear as new
  fields. Clients must ignore fields they do not know.
- New configuration keys with backward-compatible defaults, new flags, new
  subcommands, new endpoints.
- New filter operators, orderBy values, scalar types, error codes and
  `extensions` members. Clients must tolerate codes they do not know.
- New smart tags and new `@behavior` spellings.
- Bug fixes that make pdbq stricter where it was accepting something wrong, and
  security fixes. These are called out in the changelog.

The following **are** breaking and need a major release:

- Removing or renaming a configuration key, environment variable, flag,
  subcommand or exit code, or changing the meaning of a default.
- Removing or renaming an HTTP endpoint, changing a status code, or changing how
  a request is authenticated.
- Changing naming rules of the generated schema: the default inflector, the
  `simple_names` mapping, `@name` handling, payload and input type names.
- Renaming or removing a filter or orderBy operator, or changing its
  semantics.
- Changing an error `code` or the condition that produces it.
- Changing the format of a cursor or `nodeId` so that old values stop decoding.
- Renaming or removing a metric or label.

## Experimental features

These features ship and are tested, but have caveats or are recent. They are
outside the 1.0 promise: their behavior, shapes, configuration keys and names
may change in a minor release, with a changelog entry. Each is marked
"experimental" in its own documentation and in [features.md](features.md).
Features not on this list are stable.

| Feature | Documentation | Why |
|---|---|---|
| Live queries (`live.enabled`) | [live.md](live.md) | Tables are found by `EXPLAIN`, so PL/pgSQL, matview refreshes, logical replication and single-partition `TRUNCATE` are blind spots; triggers need table ownership. |
| Incremental delivery (`@defer`, `@stream`) | [incremental.md](incremental.md) | The underlying RFC is still moving; two wire shapes exist; `@stream` works on forward primary-key connections only. |
| Nested mutations, update/delete/disconnect especially (`nested_mutations`) | [mutations.md](mutations.md), [plugins.md](plugins.md) | The payload does not reflect referential actions, parent `EXISTS` SELECT policies can fail, and the shapes diverge from the upstream plugin. |
| Partial success (`transactions.partial.*`) | [mutations.md](mutations.md) | A savepoint per element; the report shape is new. |
| Schema contracts (`contracts`) | [contracts.md](contracts.md) | Need smart comments; integrity-message rewriting is heuristic. |
| Typed geometry (`plugins.settings.postgis.typed_geometry`) | [postgis.md](postgis.md) | Opt-in and recent. |
| Rasters, cube and earthdistance, bit-vector distances, `ts_headline` | [postgis.md](postgis.md), [contrib.md](contrib.md) | Raster filters are neither reprojected nor costed; cube and earth nearest-neighbour tie-breaking needs PostgreSQL 17. |
| Index-aware contrib ordering | [contrib.md](contrib.md) | Operator classes are not introspected: any GIN index is assumed to be trigram and an HNSW index is assumed to serve every metric, so an ordering can end up as a sequential scan. |
| `@anyelement` and `@anycompatible` polymorphic function resolution | [smart-comments.md](smart-comments.md) | Recent. |
| Watch mode (`watch.enabled`) | [quickstart.md](quickstart.md) | A development convenience, not a production feature. |
| The Go embedding and plugin API | [plugins.md](plugins.md) | See below. |

A feature leaves this list in a minor release once its caveats are resolved.

## Go embedding and plugin API

The Go API is experimental: the `github.com/suprbdev/pdbq` package, its type
aliases of internal types, the plugin hook interfaces (`CatalogHook`,
`InflectionHook`, `SchemaHook`, `CompileHook`, `RequestHook`) and everything
from the pdbcore module they expose. It may change or be removed in any minor
release without a deprecation period. The module is tagged `v1.x.y` for the
binary's sake, but `go get` users get no semantic-versioning guarantee for the
Go API, and a breaking change to it does not move the module to a `/v2` import
path. Several helpers a plugin needs are internal today (see
[plugins.md](plugins.md)); plan on running a fork if you embed pdbq in
production. The command line, the configuration file and the HTTP API are the
supported ways to use pdbq.

## Deprecation policy

- **Configuration keys, flags and environment variables.** A deprecated key
  keeps working for at least one minor release and logs a warning that names
  its replacement. It is removed in the next major release at the earliest.
  Before 1.0 there is no deprecation period: a renamed key fails to load with
  an error naming its new place (see `upgrading.md`).
- **Generated API.** A deprecated field or enum value in the generated schema carries GraphQL's
`@deprecated(reason: ...)`, which appears in introspection and GraphiQL and
names the replacement. `@deprecated` smart tags on your own columns and
functions use the same mechanism. A deprecated element stays for at
  least one minor release.
- **Command line.** Deprecated subcommands and flags keep working for at least
  one minor release and print a warning.
- **Go API.** Deprecated identifiers carry a `// Deprecated:` doc comment. The
  Go API is experimental, so this is a courtesy.
- **Experimental features** may be changed or removed without deprecation.

Deprecations, removals and security fixes are listed in the changelog under the
release that made them.

## Schema cache format

`pdbq schema dump` and `pdbr schema dump` write the same cache format. It
carries a format version owned by pdbcore (`introspect.CatalogFormatVersion`).
Within one format version a cache written by either product loads in the other.
Bumping the version requires a minor release of each product and a changelog
note asking you to re-run `schema dump`; a golden test in pdbcore fails the
build if the JSON shape changes without a bump. `serve` refuses a cache with
another format version, and the error names the program that wrote it.

## Supported versions

| Component | Supported |
|---|---|
| pdbq | The latest minor release of the latest major receives fixes. See [SECURITY.md](../SECURITY.md). |
| PostgreSQL | 14 to 19. Older servers are refused at startup. |
| PostGIS | 3.7 is what CI runs against. Earlier 3.x releases are not tested. |
| pgvector | Best effort. CI installs it when the image's package source has it and skips its tests otherwise. Operators added in pgvector 0.7 (`l1` distance, bit distances) need 0.7 or later. |
| Operating systems | Release binaries for Linux (amd64, arm64), macOS (amd64, arm64) and Windows (amd64); the container image is Linux amd64 and arm64. CI runs on Linux amd64 only. |
| Go | The version in `go.mod`, for building from source. |

Every PostgreSQL major from 14 to 19 runs the end-to-end suite on pushes to `main`, on a `postgis/postgis:<major>-3.7` image; pull requests run 14 and 19.

Dropping a PostgreSQL major, including one upstream no longer supports, is a
breaking change and happens in a major release. A new PostgreSQL major is added
in a minor release once CI is green on it.

