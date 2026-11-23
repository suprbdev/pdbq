# Metrics

pdbq serves Prometheus metrics when `metrics.enabled` is set. The
instrumentation is shared with pdbr (`pdbcore/metrics`), so the two products
expose the same series under their own prefix (`pdbq_`, `pdbr_`).

```yaml
metrics:
  enabled: true
  path: /metrics   # default
  addr: ":9090"    # optional: a separate listener; empty serves on server.addr
```

Or with flags or the environment: `--metrics.enabled --metrics.addr=:9090`,
`PDBQ_METRICS_ENABLED=true PDBQ_METRICS_ADDR=:9090`.

The endpoint is unauthenticated. On the main listener it bypasses RLS and
auth like `/healthz`, so prefer `metrics.addr` on a port only Prometheus can
reach. With `metrics.addr` set, the main listener does not serve
`metrics.path` at all, and the two listeners start and stop together.

```yaml
# prometheus.yml
scrape_configs:
  - job_name: pdbq
    static_configs:
      - targets: ["pdbq:9090"]
```

With [schema contracts](contracts.md) configured, every series below except
`pdbq_db_pool_*` and the runtime series carries a `contract` label, and
requests to `/healthz`, `/readyz` and the metrics endpoint are not counted.

## Metric reference

All durations are in seconds, with the Prometheus default buckets
(5 ms to 10 s).

### HTTP

| Metric | Type | Labels |
|---|---|---|
| `pdbq_http_requests_total` | counter | `route`, `method`, `status_class` |
| `pdbq_http_request_duration_seconds` | histogram | `route`, `method`, `status_class` |
| `pdbq_http_requests_in_flight` | gauge | |

- `route` is one of `graphql`, `health` (`/healthz`, `/readyz`), `metrics`,
  `schema` (`/schema.graphql`), `graphiql` (`/`) or `other`.
- `method` is the HTTP method, or `other` for a non-standard one.
- `status_class` is `2xx`, `4xx`, `5xx`, and so on.

GraphQL errors, including parse, validation and limit errors, are answered
with 200, so `status_class` does not show them; execution errors show up in
`pdbq_field_errors_total`. Malformed request bodies, missing queries and
auth failures are 4xx, and a request that exceeds `server.request_timeout`
is a 503.

### GraphQL execution

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `pdbq_document_cache_requests_total` | counter | `result` (`hit`, `miss`) | parsed-document cache lookups (`server.document_cache_size`; nothing is counted when the cache is off) |
| `pdbq_cost_limit_rejections_total` | counter | | operations rejected for exceeding `server.max_cost` |
| `pdbq_field_errors_total` | counter | `class` | errors raised while executing root fields, by class: `user_input` (GraphQL and client-safe compile errors, bad-input SQLSTATEs), `permission`, `conflict`, `resource`, `timeout`, `internal` |
| `pdbq_compile_duration_seconds` | histogram | | time to compile one root field into SQL |
| `pdbq_live_changes_total` | counter | | table change notifications received for live queries (`server.live`) |
| `pdbq_live_refreshes_total` | counter | `result` (`sent`, `unchanged`, `error`) | live query runs: new result sent, identical result not sent, result with errors sent |
| `pdbq_live_operations_total` | counter | `event` (`started`, `stopped`, `rejected`) | live queries started, stopped and refused by a live cap; started minus stopped are running |

### Database

| Metric | Type | Labels |
|---|---|---|
| `pdbq_db_duration_seconds` | histogram | `op` |
| `pdbq_db_errors_total` | counter | `op`, `sqlstate_class` |
| `pdbq_db_retries_total` | counter | `op` |
| `pdbq_db_pool_{acquired,idle,constructing,total,max}_conns` | gauge | |
| `pdbq_db_pool_{acquires,canceled_acquires,empty_acquires,new_conns,max_lifetime_destroys,max_idle_destroys}_total` | counter | |
| `pdbq_db_pool_acquire_duration_seconds_total`, `pdbq_db_pool_empty_acquire_wait_seconds_total` | counter | |

- `op` is the database round trip:
  - `query` is a root field run as a single statement outside a transaction.
  - `query_tx` is one attempt of a pipelined transaction batch.
  - `run_tx` is one attempt of a statement-by-statement mutation transaction.
- `sqlstate_class` is the two-character SQLSTATE class (`23` integrity violation, `40` serialization failure or deadlock, `42` syntax or access, `57` statement timeout or cancel, ...). Errors without a SQLSTATE are `canceled`, `timeout` or `other`. A mutation that matched no row also counts as `other` in `query_tx`, because its rollback is decided after the statement ran.
- `pdbq_db_retries_total` counts the attempts re-run after a serialization failure or deadlock (`transactions.max_retries`).
- The pool series come from pgxpool's statistics at scrape time. A rising `empty_acquires_total` or `empty_acquire_wait_seconds_total` means requests are waiting for a connection: raise `database.max_conns` or find the slow queries.

### Schema

| Metric | Type | Labels |
|---|---|---|
| `pdbq_schema_rebuilds_total` | counter | `result` (`success`, `failure`) |
| `pdbq_schema_last_rebuild_timestamp_seconds` | gauge | |

These count watch-mode rebuilds after DDL (`watch.enabled`). The boot build
is not a rebuild, so the timestamp stays 0 until the first one.

### Runtime

The `go_*` and `process_*` series of the Prometheus Go client (goroutines, GC,
memory, CPU, open file descriptors).

## Embedding

An embedding program reaches the instance through `App.Metrics()` (nil when
metrics are off). `App.Handler` instruments the returned handler and serves
`metrics.path` on it, unless `metrics.addr` is set. In that case, mount
`App.Metrics().Handler()` yourself.

To record into your own registry, build the instance with
`metrics.New(metrics.Options{Namespace: "pdbq", Registerer: reg})` from
`github.com/suprbdev/pdbcore/metrics` and pass it with `pdbq.WithMetrics`.
Add `ConstLabels` when several pdbq instances share one registry. Each
`App` needs its own instance.
