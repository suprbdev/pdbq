# Configuration

pdbq layers configuration from three sources, listed here from highest
precedence to lowest:

1. **Command-line flags** — named after config paths: `--database.url`,
   `--rls.enabled=false`.
2. **Environment variables** — `PDBQ_` prefix, `_` separates levels, `__`
   preserves an underscore inside a key: `PDBQ_DATABASE_URL`,
   `PDBQ_SERVER_MAX__DEPTH=20`, `PDBQ_RLS_AUTH_JWT__SECRET=...`.
3. **YAML file** — `--config path.yaml`, defaulting to `./pdbq.yaml` when
   present.

The **complete annotated reference** lives at
[`examples/pdbq.example.yaml`](../examples/pdbq.example.yaml). It is
generated from the config structs (`pdbq config example`), so it cannot
drift from the implementation — regenerate with `make example-config`.

## Commands

```console
$ pdbq config init      # write a minimal starter pdbq.yaml
$ pdbq config validate  # check the effective config, exit non-zero on error
$ pdbq config example   # print the full annotated reference
```

## The knobs

| Key | Default | Notes |
|---|---|---|
| `database.url` | — | required |
| `database.max_conns` | 10 | maximum pooled connections |
| `database.connect_timeout` | `10s` | timeout for establishing a connection |
| `database.statement_timeout` | `30s` | per-statement timeout on every request (`0` disables) |
| `server.addr` | `:8080` | |
| `server.graphiql` | `false` | GraphiQL playground at `/` (enable for dev) |
| `server.expose_schema` | `false` | SDL at `/schema.graphql` (reveals full schema) |
| `server.request_timeout` | `30s` | overall HTTP request timeout |
| `server.max_body_bytes` | 1048576 | maximum request body size (1 MiB) |
| `server.max_depth` / `max_cost` | 15 / 10000 | per-operation limits; recursive relations can tune the cost estimate with `@costMultiplier` (see [smart comments](smart-comments.md#cost-estimation-for-recursive-relations)) |
| `server.max_page_size` | 100 | largest accepted `first`/`last` (a larger value errors); also the default page size when neither is given |
| `server.document_cache_size` | 1000 | parsed and validated documents cached by query text (LRU, at most 32 MiB of query text; validation errors are cached too); `0` disables; dropped with the schema on a watch-mode swap |
| `server.cors_origins` | `[]` | allowed CORS origins (exact match, or `*`); empty disables CORS headers |
| `server.compression` | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `server.apq` | `false` | Apollo automatic persisted queries (in-memory, register-on-miss) |
| `server.persisted_queries_path` | — | JSON file of sha256 → document, preloaded as persisted queries |
| `server.persisted_only` | `false` | reject requests that don't reference a persisted query (requires `apq` or `persisted_queries_path`) |
| `server.read_only` | `false` | reject every mutation (maintenance, demos, defense in depth) |
| `server.disable_introspection` | `false` | reject `__schema`/`__type` queries (`__typename` keeps working) |
| `schema.schemas` | `[public]` | schema allowlist |
| `schema.cache_path` | — | boot from `pdbq schema dump` output |
| `schema.functions` | `true` | expose PostgreSQL functions as custom queries/mutations |
| `schema.expose_partitions` | `false` | expose partitions of partitioned tables as tables of their own (their rows are always reachable through the parent) |
| `filters.indexed_only` | `true` | see [filtering.md](filtering.md) |
| `filters.allow_columns` | `{}` | per-table extra filterable columns, overriding `indexed_only` |
| `rls.enabled` | `true` | see [rls.md](rls.md) for the `rls.*` and `rls.auth.*` keys |
| `transactions.mutations` | `true` | wrap each mutation in a tx |
| `transactions.per_request` | `false` | one tx for the whole request instead of one per operation |
| `transactions.isolation` | `read_committed` | or `repeatable_read`, `serializable`; `read_committed` uses the server's `default_transaction_isolation` (read committed unless changed): queries run in the pipeline's implicit transaction, mutations in a plain `BEGIN` |
| `transactions.max_retries` | 0 | automatic retries on serialization failure/deadlock (40001/40P01) |
| `watch.enabled` | `false` | dev only; refuses to combine with cache |
| `watch.poll_interval` | `5s` | fallback polling when event triggers can't be installed |
| `watch.channel` | `pdbq_ddl` | NOTIFY channel used by the DDL event trigger |
| `errors.detail` | `prod` | `dev` exposes full PG error detail; `strict` hides even constraint-violation messages |
| `plugins.disabled` | `[]` | e.g. `[simple-names]` |
| `plugins.settings.<name>` | `{}` | per-plugin config |
| `plugins.settings.aggregates.enabled` | `false` | aggregates on every table's connections; when false only `@aggregates`-tagged tables get them; see [Aggregates](aggregates.md) |
| `plugins.settings.aggregates.grouped` | `true` | `groupedAggregates(groupBy:, having:)` with its `<Type>GroupBy` enum and `<Type>HavingInput` |
| `plugins.settings.aggregates.order` | `true` | `<REL>_COUNT_*` and `<REL>_<AGGREGATE>_<COLUMN>_*` orderBy values over reverse relations |
| `plugins.settings.aggregates.filters` | `true` | `aggregates` field in `advanced-filters` to-many relation filters |
| `plugins.settings.aggregates.spatial` | `true` | `stExtent`/`stCentroid` over geometry and geography columns (false also drops the next row) |
| `plugins.settings.aggregates.spatial_union_collect` | `false` | `stUnion`/`stCollect`, whose results grow with the row set (10 × `scan_cost` each) |
| `plugins.settings.aggregates.scan_cost` | `10` | cost-estimate surcharge per aggregate field, aggregate orderBy value and relation-aggregate comparison |
| `plugins.settings.contrib.filters` | `true` | full-text, trigram, hstore, ltree, range and inet filter operators plus `<field>Rank` fields; see [Contrib types](contrib.md) |
| `plugins.settings.contrib.ordering` | `true` | `_RANK_`/`_SIMILARITY_` orderBy values and the `similarTo:` argument; see [Contrib types](contrib.md) |
| `plugins.settings.postgis.typed_geometry` | `false` | spatial columns read as typed geometry objects (`GeometryPoint { x y }`, …) instead of the `GeoJSON` scalar; see [PostGIS](postgis.md#typed-geometry) |
| `log.level` | `info` | `debug`, `info`, `warn`, `error` |
| `log.format` | `text` | `text` or `json` |
