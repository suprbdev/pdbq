# Migrating from PostGraphile

pdbq is a PostGraphile-style server: it introspects PostgreSQL, generates a
Relay-style GraphQL schema and keeps PostGraphile's smart-comment vocabulary
and payload shapes where it can. It is not a port, so names and a few
behaviors differ. This guide is for people moving an existing PostGraphile v4
or v5 deployment. It lists what maps directly, what differs and what is
missing, then gives a migration order.

> **The 1.0 names are being finalized.** Naming, mutation shapes, error codes
> and operator names are still being settled before 1.0. This guide describes
> pdbq as it is on `main` today; check [upgrading.md](upgrading.md) for what
> changed between releases and re-run `pdbq schema print` against the version
> you deploy.

Two facts shape everything else: pdbq compiles each GraphQL root field to
**one SQL statement** (there are no resolvers to wrap or extend), and it is a
single Go binary configured by YAML, environment variables and flags rather
than a Node application with plugins.

## Start by diffing the schema

The fastest way to see what a client must change is to dump both schemas and
compare them:

```console
$ pdbq --config pdbq.yaml schema print > pdbq.graphql
$ # PostGraphile v4: postgraphile --export-schema-graphql postgraphile.graphql ...
$ diff -u postgraphile.graphql pdbq.graphql
```

`pdbq schema print` needs no running server. Add `plugins.disabled:
[simple_names]` (below) first, so the diff shows real differences instead of
naming noise.

## Naming

| PostGraphile v4 | pdbq (default) | pdbq with `plugins.disabled: [simple_names]` |
|---|---|---|
| `allUsers` | `users` | `allUsers` |
| `userById(id:)` | `user(id:)` | `userById(id:)` |
| `userByEmail(email:)` (unique key) | `userByEmail(email:)` | same |
| `node(nodeId:)`, `nodeId: ID!` | same | same |
| forward relation `userByAuthorId` | `author` | `author` |
| backward relation `postsByAuthorId` | `posts` | `postsByAuthorId` |
| mutation `createUser`, `updateUserById`, `deleteUserById` | `createUser`, `updateUser`, `deleteUser` | `createUser`, `updateUserById`, `deleteUserById` |

- **`simple_names` is on by default** (PostGraphile's simplify-inflector
  look). Disable it to get v4's default `all*` / `*ById` names.
- **Forward relations are always named for the relation**, `author`, not
  `userByAuthorId`. Where a table has several foreign keys to the same table
  pdbq falls back to the verbose form so names do not collide.
- **Filter types** are `<Type>Filter` with `<Scalar>FilterOps` operator types
  (`StringFilterOps`), and order-by enums are `<Types>OrderBy` with
  `<COLUMN>_ASC|_DESC` values. There is no `PRIMARY_KEY_ASC` and no
  `NATURAL`: the primary key is appended to every `orderBy` as the tiebreaker.
  Related-column ordering uses `AUTHOR__EMAIL_ASC` (forward) and
  `POSTS_BY_AUTHOR_ID__TITLE_DESC` (backward).
- Enum types, composite types, domains, ranges, intervals and PostGIS
  `GeoJSON` follow PostGraphile's shapes (`IntRange`, `Interval`, a scalar named
  after the domain). See [domains.md](domains.md), [interval.md](interval.md),
  [contrib.md](contrib.md) and [postgis.md](postgis.md).
- Rename anything you need with the `@name` smart tag, as in PostGraphile.
- V5 changed several default names relative to v4. Compare against your v5
  schema with the diff above rather than assuming the v4 table.

## Queries and pagination

Maps directly: Relay connections (`nodes`, `edges { cursor node }`,
`pageInfo`, `totalCount`), `first`/`last`/`before`/`after`/`offset`, the
`condition` argument (equality only), `orderBy` lists, `distinctOn`, and
`Node` / `node(nodeId:)`.

Differences:

- **Pages are bounded.** A connection with neither `first` nor `last` returns
  at most `limits.max_page_size` rows (100 by default), and `first` or `last`
  above it is an error. PostGraphile returns every row unless you set a limit.
  Raise `limits.max_page_size` or paginate clients.
- **Cursors and `nodeId` are pdbq's own** (`base64(["Type", pk...])`, keyset
  backed). A `nodeId` or cursor a client stored from PostGraphile does not
  decode; clients must refetch.
- **Only indexed columns are filterable and orderable** by default
  (`filters.indexed_only: true`, with `filters.allow_columns` and the
  `@filterable` tag as exceptions). An unindexed column does not appear in
  `<Type>Filter`, `<Type>Condition` or `<Types>OrderBy`, so schemas never
  invite a sequential scan. Set `filters.indexed_only: false` for PostGraphile's
  behavior. See [filtering.md](filtering.md).
- **The filter surface is built in.** It stands in for
  `postgraphile-plugin-connection-filter` (operator names such as `equalTo`,
  `in`, `likeInsensitive`, `startsWith`, plus `and` / `or` / `not`) and
  `postgraphile-plugin-connection-filter-postgis`; the `advanced_filters` plugin,
  on by default, adds relation and computed-column filters. Check the operator
  names your clients use against the generated `*FilterOps` types.
- Depth and cost are limited per operation (`limits.max_depth`,
  `limits.max_cost`), so you do not need a complexity-limit plugin.
- Aggregates are the `@graphile/pg-aggregates` shapes, **opt-in** with
  `aggregates.enabled: true` or the `@aggregates` tag
  ([aggregates.md](aggregates.md)).

## Mutations

Maps directly: `create`, `update` and `delete` per table (gated by the role's
privileges), upsert by unique key, `clientMutationId`, the payload fields
`query`, `<row>Edge(orderBy:)` and the row's forward relations, and
`deleted<Type>NodeId` on delete payloads. Volatile functions are mutations
with PostGraphile's payload naming; stable and immutable functions are queries
and computed columns.

Differences, on `main` today:

| Area | PostGraphile v4 | pdbq |
|---|---|---|
| create | `createUser(input: {user: {...}})` | `createUser(input: {...})` with a flat `UserCreateInput` |
| update | `updateUserById(input: {id, userPatch})` | `updateUser(id:, patch: UserUpdateInput!)` |
| delete | `deleteUserById(input: {id})` | `deleteUser(id:)` |
| bulk | not generated | `createUsers`, `updateUsers(filter:, patch:)`, `deleteUsers(filter:)` |
| nested | `postgraphile-plugin-nested-mutations` | the `nested_mutations` plugin, similar shapes, **experimental** ([compatibility.md](compatibility.md#experimental-features)) |

- Payload relations use pdbq's relation names (`author`, not `userByAuthorId`).
- Every mutation runs inside a transaction; there is no `transactions.mutations`
  switch.
- `@arg0variant` is not supported: pdbq has one row input type, with every
  readable column (see [mutations.md](mutations.md#function-arguments)).

## Smart comments

The `smart_comments` plugin reads the same `COMMENT` syntax. Supported tags:
`@omit` (with actions `read`, `create`, `update`, `delete`, `all`, `many`,
`filter`, `order`), `@name`, `@fieldName`, `@resultFieldName`, `@deprecated`,
`@primaryKey`, `@unique`, `@foreignKey`, `@enum`, `@notNull`, `@nullable`,
`@filterable` (alias `@sortable`), plus pdbq's own `@aggregates`, `@contract`,
`@anyelement` and `@anycompatible`. The full list is in
[smart-comments.md](smart-comments.md).

- **`@omit read` is stricter.** It also removes the column from filters,
  `condition`, `orderBy`, `distinctOn` and aggregates, and a key containing it
  no longer generates lookups, by-key mutations, cursors or `nodeId`. In
  PostGraphile the column stays filterable, which lets a client bisect hidden
  values. This is deliberate.
- **`@filterable` and `@sortable` are one tag** here: both admit a column past
  the indexed-only policy for filtering and ordering together.
- **`@behavior` is a small subset.** pdbq reads the negative tokens `-select`,
  `-insert`, `-update`, `-delete`, `-filter` and `-order` (mapped to the `@omit`
  actions), `±contract:<name>` and `±aggregates`. Other V5 behaviors, and any tag
  not listed above (`@simpleCollections`, `@hasDefault`, `@arg0variant`, ...),
  have no effect.

## Authentication and authorization

Maps directly: a bearer JWT with a role claim, `SET LOCAL ROLE` per request,
claims in transaction-local settings readable with `current_setting(...)`,
`jwt_type` minting (set `auth.mint_type` to a composite type, as with
`--jwt-token-identifier`), and an anonymous role.

| PostGraphile v4 | pdbq |
|---|---|
| `--jwt-secret` | `auth.jwt.secret` (or `auth.jwt.secret_file`, `auth.jwt.jwks_url`) |
| `--jwt-verify-audience` | `auth.jwt.audience` |
| `--jwt-verify-issuer` | `auth.jwt.issuer` |
| `--jwt-token-identifier` | `auth.mint_type` |
| `--default-role` | `roles.anonymous` for requests without a token, `roles.default` for tokens without a role claim |
| `pgSettings` function (claims as settings) | `claims.prefix` (default `jwt.claims`) |
| `pgSettings` function (anything else) | no equivalent; use `auth.mode: headers` behind a gateway, or a `RequestHook` Go plugin |

Differences to plan for:

- **Claim settings are `jwt.claims.*`**, PostGraphile's namespace, so policies
  that read `current_setting('jwt.claims.user_id', true)` work as they are.
  Set `claims.json_setting: request.jwt.claims` to also get one JSON object
  (PostgREST and Supabase policies).
- **Tokens must carry `exp`.** A token without it is rejected. Algorithms are
  HS256/384/512 with a secret, or RS/ES with a JWKS URL; there is no downgrade
  from one to the other.
- **Anonymous access is opt-in.** With `roles.anonymous` empty (the default), a
  request without credentials is a `401`. Set `roles.anonymous: <role>` to
  match PostGraphile's `--default-role` behavior for anonymous callers.
- **Introspection runs as the connection role**, and only tables it can reach
  are generated (like `ignoreRBAC: false`). Make the login role a member of the
  request roles with `INHERIT`, as in
  [Secure setup](quickstart.md#secure-setup). Alternatively dump a schema cache
  as a privileged role and boot from it ([caching.md](caching.md)).
- `roles.allowed` pins which roles a token may name; set it in production.
- Details are in [rls.md](rls.md).

## Subscriptions and live queries

| PostGraphile v4 | pdbq |
|---|---|
| `--simple-subscriptions` (`listen(topic:)`) | `subscriptions.enabled: true`; the default topic prefix is `postgraphile:`, so `pg_notify('postgraphile:' \|\| topic, ...)` keeps working ([subscriptions.md](subscriptions.md)) |
| `--live` | `live.enabled: true` ([live.md](live.md)), **experimental** |
| `@graphile/subscriptions-lds` | not applicable: pdbq finds the tables a live query read with `EXPLAIN` and installs per-table notification triggers |
| `pgSubscriptionAuthorizationFunction` | not supported; every event is resolved under the subscriber's own identity |

Transport is the `graphql-transport-ws` protocol. `@defer` and `@stream` are
also available, opt-in and experimental ([incremental.md](incremental.md)).

## Extensibility

PostGraphile plugins (`makeExtendSchemaPlugin`, `makeWrapResolversPlugin`,
`makeAddInflectorsPlugin`, Graphile Build hooks) have no direct equivalent: pdbq
has no resolvers. Prefer these, in order:

1. **SQL.** A `STABLE` function is a query field or a computed column; a
   `VOLATILE` function is a mutation. This covers most `makeExtendSchemaPlugin`
   uses.
2. **Smart comments** for naming, hiding and relations.
3. **Views** with `@primaryKey` and `@foreignKey` for custom shapes.
4. **Go plugins** (`CatalogHook`, `InflectionHook`, `SchemaHook`, `CompileHook`,
   `RequestHook`) compiled into your own binary. The Go API is
   **experimental**, and some helpers a third-party plugin needs are internal
   today ([plugins.md](plugins.md)).

## Operations

| PostGraphile | pdbq |
|---|---|
| `--connection` / `DATABASE_URL` | `database.url` (`PDBQ_DATABASE_URL`) |
| `--schema` | `schema.schemas` |
| `--port` | `server.addr` |
| `--graphiql` / `--enhance-graphiql` | `server.graphiql` |
| `--disable-introspection` / `--no-introspection` | `server.introspection: false` |
| `--cors` | `server.cors_origins` |
| `--max-pool-size` | `database.max_conns` |
| `--body-size-limit` | `limits.max_body_bytes` |
| `--extended-errors` | `errors.detail: dev` (`strict` hides all database messages) |
| `--watch` | `watch.enabled` (development only) |
| `--export-schema-graphql` | `pdbq schema print` |
| `--write-cache` / `--read-cache` | `pdbq schema dump` and `schema.cache_path`; `pdbq schema check` is a CI drift gate |
| `--persisted-queries` | `persisted_queries.*` ([configuration.md](configuration.md)) |
| `/metrics` via a plugin | `metrics.enabled` (Prometheus, [metrics.md](metrics.md)) |

pdbq is one static Go binary or a distroless container image; `GET /healthz` and
`GET /readyz` are the probes. There is no Grafast plan introspection; use
`pdbq query --print-sql` to see the one statement each root field compiles to
(`pdbq query` runs a document without a server, as the connection role or,
with `--role` and `--claims`, under a request's identity). Configuration is strict:
unknown YAML keys and misspelt `PDBQ_*` variables are errors that suggest the
closest key.

## Migration order

1. Run pdbq next to PostGraphile against the same database, with
   `plugins.disabled: [simple_names]` and `filters.indexed_only: false`, and
   diff the two schemas.
2. Set `auth`, `roles` and `claims` to match your tokens, and check one policy
   per role end to end ([quickstart](quickstart.md#secure-setup)).
3. Decide on naming: keep `simple_names` and update clients, or disable it and
   keep v4's names. Then adapt the mutation call sites (flat inputs,
   `id`/`patch` arguments).
4. Tighten: restore `filters.indexed_only: true` and add the indexes your
   clients' filters need, or opt columns in with `@filterable`.
5. Set page sizes (`limits.max_page_size`), cost and depth limits, and
   `server.introspection: false` in production.
6. Switch traffic. Stored `nodeId` and cursor values will not decode, so refetch.
