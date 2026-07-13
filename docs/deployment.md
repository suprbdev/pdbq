# Deployment

## Docker image

The root `Dockerfile` is a multi-stage build producing a static binary on
distroless (`nonroot`), entrypoint `pdbq`, default command `serve`:

```console
$ make docker-build
$ docker run -e PDBQ_DATABASE_URL=postgres://... -p 8080:8080 pdbq:dev
```

Distroless has no shell, so run health checks from the orchestrator against
`GET /healthz` (`/readyz` is an alias).

## Endpoints

| Path | Purpose |
|---|---|
| `POST /graphql` | the API (GET with `?query=` also supported) |
| `GET /` | GraphiQL (opt-in via `server.graphiql: true`; off by default) |
| `GET /healthz`, `/readyz` | liveness/readiness |
| `GET /schema.graphql` | generated SDL (opt-in via `server.expose_schema: true`; off by default) |

## Production checklist

- `rls.enabled: true` with a **non-superuser** connection role that has been
  granted the request roles (`GRANT anonymous, app_user TO pdbq_conn`).
- `rls.auth.mode: jwt` with a strong `jwt_secret` (or `headers` strictly
  behind a gateway that strips inbound `X-Pdbq-Claim-*`).
- `errors.detail: prod` (default) — constraint violations pass through,
  internals do not.
- Leave `server.graphiql` and `server.expose_schema` off (their defaults)
  unless you want the playground and full SDL public — both hand an attacker
  a map of every table, column and relation.
- Boot from a schema cache (see [caching.md](caching.md)) so the runtime
  role needs no catalog privileges; leave `watch.enabled` off.
- Set `server.max_depth` / `server.max_cost` to fit your workload;
  `database.statement_timeout` (default 30s) backstops runaway queries.
- Resource limits: `database.max_conns` bounds the pool; each request uses
  at most one connection.

## Persisted queries / APQ

- `server.apq: true` enables Apollo automatic persisted queries: clients send
  `extensions.persistedQuery = {version: 1, sha256Hash}` instead of the query
  text; a miss returns the standard `PersistedQueryNotFound` response and the
  client retries once with the full document, registering it in an in-memory
  cache (bounded, FIFO eviction; empty after every restart until clients
  re-register).
- `server.persisted_queries_path` preloads a JSON file of
  `{"<sha256 hex>": "<GraphQL document>"}` entries (validated at startup,
  never evicted).
- `server.persisted_only: true` locks the endpoint down to persisted queries:
  requests without a `persistedQuery` extension are rejected. Combine with a
  file (and `apq: false`) for a strict build-time allowlist — client
  registration is refused without `apq`.

## Rate limiting

pdbq has **no built-in rate limiter**. The per-request protections
(`server.max_body_bytes`, `server.request_timeout`, `server.max_depth`,
`server.max_cost`, `server.max_page_size`, `database.statement_timeout`)
bound the cost of a single request, but nothing stops a client from sending
many maximally-expensive requests in parallel.

Run a rate limiter in front of pdbq in production:

- **nginx**: `limit_req_zone $binary_remote_addr zone=graphql:10m rate=10r/s;`
  plus `limit_req zone=graphql burst=20;` on the `/graphql` location.
- **Envoy**: `envoy.filters.http.local_ratelimit` (per-instance token bucket)
  or the global rate limit service for cluster-wide budgets.
- **Cloud load balancers / API gateways**: most support per-client request
  budgets out of the box.

Key by client IP at minimum; if you terminate JWTs at a gateway, keying by
the authenticated subject gives fairer budgets than IP alone.
