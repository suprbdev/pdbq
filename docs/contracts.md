# Schema contracts

A schema contract is a named GraphQL schema for one audience — `public`,
`partner`, `internal` — built from the same database as every other
contract. Each contract is a subset of the full schema: smart comments tag
tables, columns, functions and relations into or out of contracts, and pdbq
builds and serves one schema per contract, each at its own endpoint.

```yaml
contracts:
  - name: internal
    path: /            # served at /graphql
  - name: public       # served at /public/graphql
  - name: partner
    default: exclude   # only what is tagged into partner
    path: /partners    # served at /partners/graphql
```

```sql
COMMENT ON TABLE app.audit_log IS '@contract internal';
COMMENT ON COLUMN app.users.email IS '@behavior -contract:public';
COMMENT ON TABLE app.orders IS '@contract internal, partner';
COMMENT ON FUNCTION app.partner_feed() IS '@behavior +contract:partner';
```

Without `contracts` nothing changes: pdbq serves the full schema at
`/graphql` and ignores contract tags.

## Configuration

`contracts` is a list; each entry has:

| Key | Default | Meaning |
|---|---|---|
| `name` | required | Lower-case letters, digits, `_` and `-`, starting with a letter. Matched case-insensitively against the tags. |
| `default` | `include` | Policy for tables and functions without a tag for this contract: `include` (everything not tagged out) or `exclude` (only what is tagged in). |
| `path` | `/<name>` | URL prefix the contract is served under. `/` serves it at the usual root paths. |

Contracts can also be listed by name, with the default policy and path:
`contracts: [public, partner]` in YAML, `PDBQ_CONTRACTS=public,partner` in
the environment.

`pdbq config validate` rejects duplicate names, unknown policies, malformed
paths (`/` or `/segment[/segment...]` of letters, digits, `_ . ~ -`, no
trailing slash), two contracts on one path, nested paths (`/a` and `/a/b`)
a `metrics.path` under a contract's path on the main listener, and
contracts with the `smart-comments` plugin disabled: hidden columns
are removed by the plugin's `@omit` handling (see [Security](#security)).

## Endpoints

With contracts configured **only the contracts are served**. The full,
unfiltered schema is not reachable over HTTP unless a contract with
`default: include` and no tags against it stands in for it, typically at
`path: /`. Each contract at path `P` serves:

| Path | Served when |
|---|---|
| `P/graphql` | always: POST, GET and, with `server.subscriptions`, the WebSocket upgrade |
| `P/schema.graphql` | `server.expose_schema` |
| `P/` | `server.graphiql` (the page posts to `P/graphql`) |

`/healthz`, `/readyz` and the metrics endpoint stay at the root. Any other
path is a 404. Every `server.*` setting (limits, CORS, timeouts, APQ,
persisted queries, read-only) applies to every contract;
`server.subscriptions_max_connections` and `subscriptions_max_operations`
count connections and operations over all contracts together.

## Tags

Contract membership is read from the same `COMMENT`s as the other
[smart comments](smart-comments.md):

| Tag | Effect for contract `c` |
|---|---|
| `@contract a, b` | The object belongs to contracts `a` and `b` only (repeatable; lists merge). A bare `@contract` puts it in no contract. |
| `@behavior +contract:c` (or `contract:c`) | The object belongs to `c`, in addition to any `@contract` list. |
| `@behavior -contract:c` | The object is hidden from `c`, whatever else says. |

For a contract `c` and an object, in order:

1. `-contract:c` hides the object.
2. `@contract` listing `c`, or `+contract:c`, shows it.
3. A `@contract` tag that does not list `c` hides it.
4. Otherwise a **table or function** follows the contract's `default`
   policy, and a **column or foreign key** follows its table: it is shown
   wherever its table is.

Tags apply to:

| Object | Hiding it removes |
|---|---|
| Table, view, materialized view | Everything a bare `@omit` on the table removes: the type, its queries, mutations, connections, relation fields both ways, filters and nested inputs referencing it. Also its partitions, every function taking or returning its row type, every column typed as its row type, and every composite attribute of its row type. |
| Column | Everything a bare `@omit` on the column removes: the output field, filter and condition fields, orderBy and distinctOn values, aggregates, create and update input fields, and every key containing it (by-key lookups and mutations, `nodeId`, keyset cursors; see [What `@omit read` hides](smart-comments.md#what-omit-read-hides)). |
| Function | Its root query or mutation field, or its computed column. |
| Foreign key (`COMMENT ON CONSTRAINT`) | The relation fields in both directions, relation filters and orderings, nested-mutation inputs. |

Functions follow the contract policy, not the table of their first
argument: in a `default: exclude` contract tag computed-column functions in
along with their table.

Logical `@foreignKey` relations go with either end but cannot be tagged on
their own. `@enum` tables become enums at introspection and are not
filtered.

## Security

A contract only ever removes surface. pdbq copies the introspected catalog
for each contract, drops the hidden tables, functions and foreign keys from
the copy, and marks every hidden column with a bare `@omit` before the
normal schema build runs on the copy, so every plugin's `@omit` handling
(`smart-comments`, `advanced-filters`, `aggregates`, `postgis`, `contrib`,
`nested-mutations`) removes the column from its surface too. Nothing a
contract does can add a field, a type, a privilege or a row:

- A contract's schema is always a subset of the full schema; `+contract:`
  tags only choose among objects the full schema exposes.
- Row-level security, roles and claims are unchanged. A contract narrows
  what a client can ask for; PostgreSQL still decides which rows the role
  sees. Point different audiences at different contracts *and* give them
  different roles when the data itself must differ.
- A hidden column is hidden from filters, ordering and keys too, so it
  cannot be recovered by bisecting with range filters or probed with by-key
  lookups.
- Hiding a foreign key column does not hide the relation it backs: tag the
  constraint as well when the related row must not be reachable.
- Integrity errors keep their SQLSTATE but not always PostgreSQL's message.
  With `errors.detail: prod`, a foreign key, not-null, unique, check or
  exclusion violation whose message names a table, column or foreign key
  the contract hides (or `@omit` hides, with or without contracts) is
  reported as `foreign key violation`, `not-null violation`, and so on. A
  check or exclusion constraint, which pdbq does not introspect, gets the
  generic message whenever its table has a hidden column. Deleting a
  visible row still fails while a hidden table references it: the error no
  longer names that table, but its SQLSTATE (`23503`) still tells that some
  row does. `errors.detail: dev` shows every message.

## Watch mode and metrics

Watch mode rebuilds every contract after each DDL change (tag changes are
DDL too) and swaps each one in independently; a contract whose rebuild
fails keeps serving its previous schema.

With `metrics.enabled`, every series of the [metric reference](metrics.md)
carries a `contract` label, except the pool and runtime series, which are
shared. Requests to `/healthz`, `/readyz` and the metrics endpoint are not
counted in contract mode. An embedder passing its own instance with
`pdbq.WithMetrics` records every contract into that one instance, without a
contract label.

## CLI

```
$ pdbq schema print --contract public          # SDL of one contract
$ pdbq schema print --contract partner --json  # its introspection JSON
```

`schema print` without `--contract` prints the full schema, and `pdbq
query` always runs against the full schema. The schema
cache (`schema dump` / `schema check`) holds the catalog, which every
contract is derived from, so one cache serves all contracts.
