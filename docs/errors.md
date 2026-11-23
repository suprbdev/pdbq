# Errors

Every error pdbq produces carries a machine-readable `extensions.code`. This
holds for GraphQL errors in a `200` response, for the bodies of non-`200`
HTTP responses, and for WebSocket `error` and `next` messages. Clients branch
on `code` and show `message` to people. The vocabulary is closed: every code
pdbq emits is listed below. A database error is `pg:<SQLSTATE>`. Codes that
name a concept pdbr also has use pdbr's spelling (see pdbr's
`docs/errors.md`): `auth_required`, `invalid_token`, `role_not_allowed`,
`read_only`, `cost_exceeded`, `page_size_exceeded`, `not_found`, `timeout`,
`unavailable`, `body_too_large`, `method_not_allowed`,
`unsupported_media_type`, `not_acceptable`, `invalid_cursor`, `internal`
and `pg:<SQLSTATE>`.

## Format

A GraphQL result keeps the spec shape. Each error has `message`, optional
`locations` and `path`, and `extensions.code`:

```json
{
  "data": { "createUser": null },
  "errors": [{
    "message": "duplicate key value violates unique constraint \"users_email_key\"",
    "path": ["createUser"],
    "extensions": { "code": "pg:23505" }
  }]
}
```

pdbq answers a request it cannot execute with a non-`200` status. The body
has the same shape, has `Content-Type: application/json` and holds exactly
one error:

```http
HTTP/1.1 415 Unsupported Media Type
Content-Type: application/json

{"errors":[{"message":"unsupported content type: POST requests must send Content-Type: application/json","extensions":{"code":"unsupported_media_type"}}]}
```

`errors.detail` changes only the message text and the extra database
members in `extensions` (see [below](#database-errors-pgsqlstate)). The
status and `code` stay the same at every level. The status of a GraphQL
result is `200` even when every field failed. Only the errors in the table
below that show another status change the HTTP status.

On WebSocket (`graphql-transport-ws`), an operation that cannot start ends
with an `error` message whose payload is the list of errors, each with its
`code`. On the legacy `graphql-ws` protocol it ends with a `data` message
carrying `errors`. Errors raised while an operation runs arrive in the
`next` (or `data`) payload, as over HTTP. Connection-level failures are
WebSocket close codes, not GraphQL errors (`4400`, `4401`, `4403`, `4406`,
`4408`, `4409`, `4429`: see [subscriptions.md](subscriptions.md)).

## Code vocabulary

| `code` | HTTP status | When |
|---|---|---|
| `graphql_parse_failed` | 200 | the document is not valid GraphQL syntax |
| `graphql_validation_failed` | 200 | the document fails validation against the schema (unknown field, wrong argument type, a misused `@defer` / `@stream`, ...), or a variable value does not match its declared type |
| `invalid_request` | 400; 200 for an unknown `operationName` | malformed GraphQL-over-HTTP request: the body is not a JSON object, `variables` is not valid JSON, `query` is missing, the `persistedQuery` extension has an unsupported version, no `sha256Hash`, or a hash that does not match the query; `operationName` names no operation of the document |
| `document_limit_exceeded` | 200 | a structural limit of the document: `limits.max_query_tokens`, `limits.max_input_depth` (literals and variables), `limits.max_selections`, `limits.max_field_comparisons`, `incremental.max_deferred`, `transactions.partial.max_items` |
| `depth_exceeded` | 200 | selection depth above `limits.max_depth`; nested mutations nested deeper than `plugins.settings.nested_mutations.max_depth` |
| `cost_exceeded` | 200 | estimated cost above `limits.max_cost` |
| `page_size_exceeded` | 200 | `first` / `last` above `limits.max_page_size` |
| `introspection_disabled` | 200 | `__schema` or `__type` while `server.introspection` is off |
| `introspection_limit_exceeded` | 200 | the introspection response would exceed `limits.max_introspection_bytes` |
| `read_only` | 200 | a mutation while `server.read_only` is on |
| `partial_mutations_disabled` | 200 | `extensions.partialMutations` while `transactions.partial.enabled` is off |
| `operation_not_supported` | 200 | a subscription sent over HTTP; a subscription or live query while that feature is off; a subscription selecting more than one root field or one that is not `listen` |
| `operation_rejected` | 200 | a `RequestHook`'s `BeforeOperation` refused the operation with an error that carries no code of its own (the hook's message is the message) |
| `invalid_argument` | 200 | an argument value the compiler rejects: a negative `first` / `offset`, `first` with `last`, an empty patch or bulk-create list, an upsert input without its key field, a malformed `nodeId`, a filter of the wrong shape, an unknown operator, an invalid geometry, interval or vector value, a distance or similarity orderBy value or score field without its `near:` reference (`orderBy LOCATION_DISTANCE_ASC requires near: {location: …}`), a nested-mutation input that is not allowed, more than `plugins.settings.nested_mutations.max_operations` nested operations, a subscription topic that is invalid or not allowed, ... |
| `invalid_cursor` | 200 | an `after` / `before` cursor that cannot be decoded, has the wrong number of key values or belongs to another type |
| `not_found` | 200; 404 for a path | a keyed update or delete matched no row (`no row matched "<alias>"`), or a nested `connectBy…` / `updateBy…` / `deleteBy…` (or the key of an update with nested fields) matched no row (`nested_mutations: <path> matched 0 rows`; the whole mutation is rolled back); an HTTP path pdbq does not serve |
| `auth_required` | 401 | no credentials and `roles.anonymous` is empty; `WWW-Authenticate: Bearer` |
| `invalid_token` | 401 | the credentials are present but invalid (signature, expiry, issuer, audience, JWKS, malformed or untrusted claim headers); `WWW-Authenticate: Bearer error="invalid_token"` |
| `role_not_allowed` | 403 | the credentials are valid but the role is outside `roles.allowed`, is never assumable (`none`, `public`, `pg_*`, not `[A-Za-z0-9_]+`) or the credential resolves to no role; `WWW-Authenticate: Bearer error="insufficient_scope"` |
| `method_not_allowed` | 405 + `Allow` | a mutation or subscription over `GET` / `HEAD`; a method no route of that path serves |
| `unsupported_media_type` | 415 | `POST` without `Content-Type: application/json` |
| `not_acceptable` | 406 | `Accept: multipart/mixed` naming only a `deferSpec` / `incrementalSpec` pdbq does not serve |
| `body_too_large` | 413 | request body larger than `limits.max_body_bytes` |
| `persisted_query_not_found` | 200 | a hash-only request whose hash is not registered; message `PersistedQueryNotFound`, so Apollo clients resend with the query |
| `persisted_query_not_supported` | 200 for a hash-only request; 400 for a registration | a hash-only request while persisted queries are off (message `PersistedQueryNotSupported`, so Apollo clients stop sending hashes); a query sent with its hash to register it while `persisted_queries.apq` is off |
| `persisted_query_required` | 400 | `persisted_queries.only` and the request has no `persistedQuery` extension |
| `subscription_limit_exceeded` | WebSocket | `subscriptions.max_per_connection` or `live.max_per_connection` reached; a subscriber that fell `subscriptions.buffer` events behind |
| `unavailable` | 503 for `/readyz` and WebSocket upgrades; 200 in a result | the database refused or dropped the connection without a SQLSTATE; `/readyz` ping failed; `subscriptions.max_connections` reached (upgrade answered 503); `subscriptions.max_operations` or `live.max_operations` reached; the LISTEN connection is down, busy or shutting down. The message is `database unavailable` outside `dev`; retry later |
| `timeout` | 504; 200 in a result | `server.request_timeout` elapsed. Over HTTP the response is `504` with `{"errors":[{"message":"request timeout","extensions":{"code":"timeout"}}]}`; an incremental response, which is already streaming, ends with the error on the pending parts; a WebSocket operation gets it in its result |
| `internal` | 500; 200 in a result | anything else: a hook, plugin or compile failure that is not a client error, a response that cannot be encoded. The message is `internal error` (`compile: invalid query` for a compile error) outside `dev`, and the full error is logged |
| `pg:<SQLSTATE>` | 200 | a database error; see below |

Codes are stable from 1.0: new codes may be added in a minor release,
existing ones are never renamed or reused. A client should treat an
unknown code like `internal`.

Each row is asserted by a unit test (`internal/errcode`, `internal/exec`,
`internal/server`) and, where a database or a full server is needed, by
`test/errors_e2e_test.go`. `subscription_limit_exceeded`, `operation_rejected`
and `internal` are unit-tested only: the e2e fixture cannot reach them.

## Database errors (`pg:<SQLSTATE>`)

A database error's code is `pg:` followed by the five-character SQLSTATE
verbatim: `pg:23505` (unique violation), `pg:23503` (foreign key),
`pg:42501` (insufficient privilege), `pg:40001` (serialization failure
after `transactions.max_retries`), `pg:57014` (`database.statement_timeout`),
`pg:P0001` (`RAISE EXCEPTION`). A function can pick the code with
`RAISE … USING ERRCODE = '<state>'`.

There is one exception: the `22P02` that the nested-mutation guard raises
when a keyed nested operation matches no row is reported as `not_found`
with the message `nested_mutations: <path> matched 0 rows`.

The message follows `errors.detail`:

| Level | Message | Extra `extensions` |
|---|---|---|
| `dev` | PostgreSQL's message | `detail`, `hint`, `table`, `constraint` |
| `prod` (default) | `23*`, `22*` and `21*` messages, and malformed full-text / ltree operands, pass through (an integrity error naming an object the schema hides gets a generic message: `unique violation`, `foreign key violation`, ...); PostGIS input errors are `invalid geometry`; `42501` is `permission denied`; other `42*` are `invalid operation`; everything else is `database error` | none |
| `strict` | `23*` are `constraint violation`; `21*`, `22*`, contrib and PostGIS input errors are `invalid input value`; the rest as `prod` | none |

A `dev` error looks like this:

```json
{"message":"duplicate key value violates unique constraint \"users_email_key\"","path":["createUser"],
 "extensions":{"code":"pg:23505","constraint":"users_email_key","detail":"Key (email)=(a@b.c) already exists.","hint":"","table":"users"}}
```

## Plugins

A `RequestHook` that refuses an operation in `BeforeOperation` controls the
code: return a `*gqlerror.Error` whose `Extensions["code"]` is set, and
pdbq passes it through unchanged. Any other error becomes
`operation_rejected` with the error's message. A `CompileHook` returns a
client error with `compile.UserErrorf` (code `invalid_argument`) or
`compile.UserErrorCodef(code, …)`. Any other compile error is `internal`.

## Related

- [configuration.md](configuration.md): `errors.detail`, `limits.*`,
  `server.request_timeout`, `database.statement_timeout`.
- [mutations.md](mutations.md): not-found and partial-success errors.
- [rls.md](rls.md): `401` / `403` and the challenge headers.
- [deployment.md](deployment.md): persisted queries, `/readyz`.
- [upgrading.md](upgrading.md#error-codes): what changed from 0.x.
