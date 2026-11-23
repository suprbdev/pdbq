# Deployment

## Docker image

The root `Dockerfile` is a multi-stage build producing a static binary on
distroless (`nonroot`), entrypoint `pdbq`, default command `serve`:

```console
$ make docker-build
$ docker run -e PDBQ_DATABASE_URL=postgres://... -p 8080:8080 pdbq:dev
```

Distroless has no shell, so run health checks from the orchestrator against
`GET /healthz` (liveness) and `GET /readyz` (readiness). `/readyz` pings the
database pool, bounded by `server.readyz_timeout` (default `2s`), and answers
`503` with a GraphQL-style error body (`code: unavailable`) when the ping fails
or times out; `/healthz` only reports that the process is serving.

## Endpoints

| Path | Purpose |
|---|---|
| `POST /graphql` | the API; the body must be `Content-Type: application/json` (`415` otherwise). `GET` with `?query=` runs queries only (`405` for mutations and subscriptions); see [CSRF](#csrf) |
| `GET /` | GraphiQL (opt-in via `server.graphiql: true`; off by default) |
| `GET /healthz`, `/readyz` | liveness / readiness (`/readyz` pings the database; `503` when unreachable) |
| `GET /schema.graphql` | generated SDL (opt-in via `server.expose_schema: true`; off by default) |
| `GET /metrics` | Prometheus metrics (opt-in via `metrics.enabled: true`; path `metrics.path`, or on its own listener with `metrics.addr`); see [metrics.md](metrics.md) |

GET requests carry the query and variables in the URL, so proxy, load
balancer, and server access logs along the path will record them. If your
queries or variables are sensitive, use POST only and consider stripping or
truncating the query string in intermediary log formats.

## Production checklist

- **Terminate TLS in front of pdbq.** The server speaks plain HTTP only —
  JWTs, claims and all query data cross the wire in cleartext until a
  TLS-terminating reverse proxy, load balancer or ingress controller sits in
  front. Never expose the pdbq port directly to untrusted networks.
- `rls.enabled: true` with a **non-superuser** connection role that has been
  granted the request roles (`GRANT anonymous, app_user TO pdbq_conn`).
- `rls.auth.mode: jwt` with a strong `jwt_secret` (or `headers` strictly
  behind a gateway that strips inbound `X-Pdbq-Claim-*`).
- `errors.detail: prod` (default) — constraint violations pass through,
  internals do not. Use `strict` if constraint/column names in error
  messages are themselves sensitive (hides all database error messages).
- Leave `server.graphiql` and `server.expose_schema` off (their defaults)
  unless you want the playground and full SDL public — both hand an attacker
  a map of every table, column and relation. GraphQL introspection reveals
  the same map; set `server.disable_introspection: true` to close that
  channel too (breaks GraphiQL and schema-aware clients). It also leaves the
  "Did you mean …" suggestions out of validation errors, which would
  otherwise let a client rebuild the schema by guessing names. The base
  messages still name the parent type of an unknown field (`Cannot query
  field "x" on type "User"`) and the expected type of a mistyped value, as
  in graphql-js and Apollo Server.
- Boot from a schema cache (see [caching.md](caching.md)) so the runtime
  role needs no catalog privileges; leave `watch.enabled` off.
- Set `server.max_depth` / `server.max_cost` to fit your workload;
  `database.statement_timeout` (default 30s) backstops runaway queries.
- Resource limits: `database.max_conns` bounds the pool; each request uses
  at most one connection.
- If you enable `metrics.enabled`, set `metrics.addr` (e.g. `:9090`) to a
  port only your Prometheus can reach: the endpoint is unauthenticated, and
  its route and error counts describe your traffic.

## CSRF

`/graphql` only accepts requests that a cross-site page cannot send
without a CORS preflight, following the
[GraphQL-over-HTTP](https://graphql.github.io/graphql-over-http/draft/)
spec:

- `POST` requires `Content-Type: application/json` (parameters such as
  `charset=utf-8` are fine). Any other type, including a missing header and
  the CORS-safelisted `text/plain`, `application/x-www-form-urlencoded` and
  `multipart/form-data`, answers `415 Unsupported Media Type` without
  reading the body.
- `GET` runs query operations only. A mutation or subscription, including
  one sent as a persisted query hash, answers `405 Method Not Allowed` with
  `Allow: POST`.

A browser preflights a cross-origin `application/json` POST, and pdbq
answers preflights only for `server.cors_origins`, so a page on another
origin cannot make a victim's browser run a mutation with its cookies. This
matters when identity comes from ambient credentials: `rls.auth.mode:
headers` behind a gateway that authenticates with a session cookie, client
certificates, or IP allowlists. Bearer JWTs are not sent automatically by
the browser and were never exposed. GraphiQL and the usual GraphQL clients
(Apollo Client, urql, graphql-request, Relay) already send
`application/json`. WebSocket upgrades are covered separately: their
`Origin` must be the server's own host or match `server.cors_origins`.

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
