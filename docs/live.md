# Live queries

> **Experimental.** Live queries are outside the 1.0 compatibility promise and may change in a minor release. See [Compatibility and versioning](compatibility.md#experimental-features).

A live query is a query whose result the server sends again whenever the data
it read changes. pdbq serves them as PostGraphile v4 does (`--live`): change
`query` to `subscription` and the operation becomes live. Every `Query` root
field is repeated on the `Subscription` root under the same name:

```graphql
subscription {
  allPosts(filter: {published: {equalTo: true}}, first: 20) {
    nodes { id title author { fullName } }
  }
}
```

A query operation marked with the `@live` directive is live too, which is
how `graphql-live-query` style clients ask for it, and it may select several
root fields:

```graphql
query @live {
  allPosts(first: 20) { totalCount nodes { title } }
  userById(id: 1) { fullName }
}
```

Each result is a complete execution result (a `next` message on
`graphql-transport-ws`, a `data` message on the legacy `graphql-ws`
protocol), not a patch. Over plain HTTP, `query @live` runs once like any
query, and a subscription is refused as it always is.

## Enabling

Live queries are **off by default** and need subscriptions, whose WebSocket
transport, authentication and limits they share (see
[Subscriptions](subscriptions.md)):

```yaml
subscriptions:
  enabled: true
live:
  enabled: true
```

With `live.enabled` off the generated schema is byte-for-byte what it is
without it: no repeated fields and no `@live` directive. With it on, the
`Subscription` type gets one field per `Query` field (except the
placeholder `_empty`), sharing the `Query` field's arguments, type and
description, plus `listen`. A `Query` field named `listen` is not repeated.
The directive is declared as `directive @live on QUERY`.

| Key | Default | Meaning |
|---|---|---|
| `live.enabled` | `false` | serve live queries |
| `live.channel` | `pdbq_live` | LISTEN channel of change notifications; also names the triggers and their function |
| `live.install_triggers` | `true` | install the change triggers at startup and after watch-mode rebuilds |
| `live.throttle` | `500ms` | shortest time between two runs of one live query |
| `live.concurrency` | `4` | live query runs in flight at once across the server |
| `live.max_per_connection` | `10` | live queries per WebSocket connection |
| `live.max_operations` | `1000` | live queries over all WebSocket connections |

## How it works

1. **Subscribe.** The operation is parsed, validated and checked against
   every request limit once, as any subscription: `limits.max_depth`,
   `limits.max_cost` (the repeated fields share the `Query` fields' cost
   metadata, `@costMultiplier` included), document limits,
   `persisted_queries.only`, `server.introspection: false` and the
   `BeforeOperation` hooks. Then the live caps are checked.
2. **Find the tables.** Each root field is compiled to its single SQL
   statement and `EXPLAIN (VERBOSE, FORMAT JSON)`ed, not run, under the
   subscriber's role and claims. Every relation in the plan, including
   subplans and init plans, is mapped to its partitioned root (a table outside
   a partition tree is its own root). The planner sees through views to the
   tables they read and includes the tables that row-level security policies
   read, so a policy over a membership table makes the live query follow that
   table too.
3. **Watch.** The operation registers its tables with the tracker, which
   LISTENs on `live.channel` over the subscriptions' shared LISTEN
   connection (opened outside the pool), and waits until that LISTEN is in
   place.
4. **First result.** The operation runs as a query under the subscriber's
   role and claims (one round trip, in a transaction when RLS applies,
   bounded by `server.request_timeout`) and the result is sent.
5. **Changes.** A notification names a changed table. Every live operation
   reading it is marked changed. A marked operation runs again, no earlier
   than `live.throttle` after its previous run started; changes
   arriving meanwhile coalesce into that one run (leading and trailing edge,
   as PostGraphile's `LIVE_THROTTLE`). At most `live.concurrency` runs
   are in flight across the server; the others wait for a slot, so a write to
   a table many live queries read cannot take every pool connection.
6. **Dedupe.** The SHA-256 of the encoded result is compared with the last
   one sent; an identical result is not sent. A write that does not change
   what the subscriber can see (another user's row hidden by RLS, a column
   the query does not select) costs a run but sends nothing.

Each run re-executes the whole operation under the subscriber's own
identity, so RLS decides what every subscriber sees, results never mix
between subscribers, and computed columns are recomputed. When the token's
`exp` passes, the socket is closed with `4403` as for any subscription.

## Change detection

pdbq installs, when `live.install_triggers` is on (the default):

- a trigger function `<channel>_notify()` (in the connecting role's current
  schema) that runs `pg_notify('<channel>', TG_ARGV[0])`;
- on every ordinary table of the exposed schemas, and on every table their
  views read (recursively, in any schema), an
  `AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ... FOR EACH STATEMENT`
  trigger named `<channel>`, passing the table's OID;
- on every partitioned table, an `AFTER INSERT OR UPDATE OR DELETE ... FOR
  EACH ROW` trigger named `<channel>`, which PostgreSQL clones onto every
  partition, including partitions created later, plus an
  `AFTER TRUNCATE ... FOR EACH STATEMENT` trigger named `<channel>_truncate`.
  Both pass the partitioned table's OID, so a write to a partition refreshes
  live queries reading the parent.

The payload is just the OID: a notification reveals that a table changed,
never what changed. `NOTIFY` is transactional and PostgreSQL drops identical
notifications within a transaction, so one statement, or one transaction
touching a table many times, sends one notification per table. A statement
trigger fires even when the statement changed no row.

Triggers are created only where missing (existing ones are kept), each under
`lock_timeout = 5s`, and again after every watch-mode rebuild so new tables
are covered. Creating a trigger needs ownership of the table. A table the
connecting role cannot add a trigger to is logged, and so is a live query
reading a table without the trigger: changes to it do not refresh live
queries. To install the triggers yourself (for example in a migration run by
the table owner), set `live.install_triggers: false` and run:

```sql
CREATE FUNCTION pdbq_live_notify() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
	PERFORM pg_notify('pdbq_live', TG_ARGV[0]);
	RETURN NULL;
END $$;

-- an ordinary table: the argument is its OID, as a string literal
DO $$ BEGIN
	EXECUTE format('CREATE TRIGGER pdbq_live AFTER INSERT OR UPDATE OR DELETE OR TRUNCATE ON %s '
		'FOR EACH STATEMENT EXECUTE FUNCTION pdbq_live_notify(%L)',
		'public.posts'::regclass, 'public.posts'::regclass::oid);
END $$;
```

Trigger arguments are string constants, hence the `format`. A partitioned
table takes the row trigger and the `pdbq_live_truncate` statement trigger
described above, with its own OID. With the setting off,
pdbq still checks which tables have the triggers and logs the others. Anything
can announce a change by hand, which is how to refresh live queries over data
the triggers do not see:

```sql
SELECT pg_notify('pdbq_live', 'public.daily_totals'::regclass::oid::text);
```

### Why triggers, not logical decoding

PostGraphile's `@graphile/subscriptions-lds` reads a logical replication slot
with `wal2json`. pdbq uses triggers instead:

- **No replication slot.** A slot retains WAL until it is consumed: a stopped
  or lagging pdbq would make the database keep WAL without bound and
  eventually fill its disk. Triggers hold nothing when pdbq is down.
- **No server configuration or privileges.** Logical decoding needs
  `wal_level = logical` (a restart), the `REPLICATION` attribute or a
  superuser, and an output plugin; triggers need table ownership, once.
- **Table granularity is what re-running needs.** Every live query is re-run
  in full under its subscriber's identity anyway (RLS and computed values
  make anything finer unsafe), so knowing which tables changed is enough. Row
  images from the WAL would cost decoding work for no benefit.
- **Delivery fits the existing LISTEN connection** and its reconnect logic,
  with no polling interval (`subscriptions-lds` polls the slot every
  125 ms).

The costs: a statement trigger per write statement (one function call and a
`pg_notify`), a row trigger per row on partitioned tables, and notifications
lost while the LISTEN connection is down (see below).

## Reliability

- **Lost notifications.** When the LISTEN connection fails, the hub
  reconnects with backoff (see [Subscriptions](subscriptions.md)); as soon
  as it LISTENs again, every live query re-runs, because notifications sent
  in between were lost. If the tracker's own queue (4096 notifications)
  overflows, it re-subscribes and every live query re-runs too. A live query
  therefore never stays stale after a gap, at the price of one run each.
- **What is not seen.** Changes to tables read only inside functions that
  the planner does not inline (for example PL/pgSQL functions behind computed
  columns or custom queries; simple SQL functions are inlined), refreshes of
  materialized views, changes to tables outside the exposed schemas that no
  exposed view reads (unless you add the trigger), changes applied by
  logical replication (triggers do not fire on a subscriber unless enabled
  with `ENABLE ALWAYS TRIGGER`), and `TRUNCATE` of a single partition. The
  next run triggered by a tracked table, or a manual `pg_notify`, picks them
  up. PostGraphile's live queries have the same blind spots for functions
  and views it cannot track.
- **Watch mode.** A live query keeps running against the schema it was
  validated with, like any subscription; triggers for new tables are
  installed after each rebuild.

## Limits and cost

A live query costs one statement per root field each time it runs, under
the subscriber's identity: `N` live queries on a table mean up to `N` runs
per change, spread over `live.throttle` and queued behind
`live.concurrency`. Keep live queries narrow (filters, small pages),
and prefer [listen subscriptions](subscriptions.md) when thousands of
clients watch the same data: PostGraphile likewise does not recommend live
queries for very large deployments.

Live queries count against the subscription caps
(`subscriptions.max_per_connection`,
`subscriptions.max_operations`, the subscribe rate) and, in
addition, against `live.max_per_connection` and
`live.max_operations`. A live query over a cap gets an `error`
message (a `data` message with `errors` then `complete` on the legacy
protocol). The live query's statements are bounded by `limits.max_cost`,
`limits.max_depth`, `limits.max_page_size` and `server.request_timeout` per
run. Any client that may call `pg_notify` can make live queries re-run; the
throttle and the concurrency bound cap what that costs.

A listen subscription cannot listen on `live.channel` (it gets an
`error` saying the topic is not allowed).

## Metrics

With metrics on (see [Metrics](metrics.md)):

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `pdbq_live_changes_total` | counter | | table change notifications received |
| `pdbq_live_refreshes_total` | counter | `result` (`sent`, `unchanged`, `error`) | live query runs: a new result was sent, the result was identical and not sent, or the result carried errors (sent) |
| `pdbq_live_operations_total` | counter | `event` (`started`, `stopped`, `rejected`) | live queries started, stopped, and refused by a live cap; started minus stopped is the number running |

With contracts, the live counters are recorded on the first contract's
series.
