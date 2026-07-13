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

## The important knobs

| Key | Default | Notes |
|---|---|---|
| `database.url` | — | required |
| `server.addr` | `:8080` | |
| `server.max_depth` / `max_cost` | 15 / 10000 | per-operation limits |
| `server.max_page_size` | 100 | caps `first`/`last`; also the default page size when neither is given |
| `server.graphiql` | `false` | GraphiQL playground at `/` (enable for dev) |
| `server.expose_schema` | `false` | SDL at `/schema.graphql` (reveals full schema) |
| `server.compression` | `false` | gzip responses when the client sends `Accept-Encoding: gzip` |
| `schema.schemas` | `[public]` | schema allowlist |
| `schema.cache_path` | — | boot from `pdbq schema dump` output |
| `filters.indexed_only` | `true` | see [filtering.md](filtering.md) |
| `rls.enabled` | `true` | see [rls.md](rls.md) |
| `transactions.mutations` | `true` | wrap each mutation in a tx |
| `watch.enabled` | `false` | dev only; refuses to combine with cache |
| `errors.detail` | `prod` | `dev` exposes full PG error detail |
| `plugins.disabled` | `[]` | e.g. `[simple-names]` |
| `plugins.settings.<name>` | `{}` | per-plugin config |
