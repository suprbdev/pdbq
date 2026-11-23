# RLS & authentication

Identity is configured in three sections:

- **`auth`** — how a request proves who it is: a bearer JWT, claim headers
  from a trusted gateway, or nothing.
- **`roles`** — which database role the request runs as.
- **`claims`** — how the request's claims are exposed to SQL.

Each operation that has a role or claims to apply runs inside a transaction
that first applies the identity, in one statement sent in the same pipelined
batch as the operation's root field statements:

1. `SET LOCAL ROLE <role>` (as `set_config('role', $1, true)`, the same
   operation with the role as a bind parameter) — the role from the
   request's role claim, falling back to `roles.default`, then
   `roles.anonymous` (see [Roles](#roles)).
2. `set_config('jwt.claims.<key>', <value>, true)` for every claim —
   readable in policies via `current_setting('jwt.claims.user_id', true)`
   (`claims.prefix`, PostGraphile's namespace by default).
3. With `claims.json_setting` set, `set_config(<setting>, <claims as JSON>,
   true)` — e.g. `current_setting('request.jwt.claims', true)::json->>'sub'`,
   PostgREST's and Supabase's form (`{}` for a request without claims; with
   the setting configured every operation runs in a transaction so it is
   always defined).

Only `SET LOCAL` / transaction-scoped `set_config` are ever used, so pooled
connections cannot leak one request's identity into another.

## Credential sources (`auth.mode`)

- **`jwt`** (default): `Authorization: Bearer <token>` (the scheme is
  case-insensitive), HMAC-verified with `auth.jwt.secret` or the contents of
  `auth.jwt.secret_file` (read at startup, trailing newlines stripped; handy
  for Docker and Kubernetes secrets). Jwt mode needs a secret, a secret file
  or a JWKS URL: pdbq refuses to start without one. Optional
  `auth.jwt.issuer`/`auth.jwt.audience` checks; `auth.jwt.leeway` tolerates
  clock skew on `exp`, `nbf` and `iat` (default `0s`). Tokens must carry an
  `exp` claim — tokens without one are rejected, so a stolen token cannot
  stay valid forever. An invalid token is a 401.

  The role is read from `auth.jwt.role_claim_path` (default `role`): a
  dot-separated path with optional array indexes and double-quoted keys, so
  Supabase's `app_metadata.role`, Keycloak's `realm_access.roles[0]` or a
  namespaced `"https://example.com/role"` work. A missing role claim falls
  back to `roles.default`; a role claim that is not a non-empty string is an
  invalid token.

  Set `auth.jwt.jwks_url` for asymmetric verification instead (RS256/384/512,
  ES256/384/512): keys are fetched from the JWKS endpoint, matched by `kid`,
  and cached for `auth.jwt.jwks_cache_ttl` (default 1h; an unknown `kid`
  refreshes early, at most once a minute, so key rotation just works). After
  the TTL keys refresh in the background and the previous set stays in use
  for up to one more TTL while the endpoint fails; failed fetches back off
  from 1s to 1m and are logged with the endpoint URL. In JWKS mode the HMAC
  algorithms are off the allowlist entirely — no downgrade to
  HMAC-with-public-key.
- **`headers`**: for deployments behind a trusted gateway that has already
  authenticated the caller. Headers matching `auth.headers.prefix`
  (default `X-Pdbq-Claim-`) become claims: `X-Pdbq-Claim-User-Id: 42` →
  `jwt.claims.user_id = '42'`; the role comes from the
  `auth.headers.role_claim` claim (default `role`, sent as
  `X-Pdbq-Claim-Role`).

  Each claim has exactly one header spelling: the part after the prefix is
  lower-cased and its hyphens become underscores, so `User-Id` is the only
  way to send `user_id`. A request is rejected with a 401 when a claim header
  contains an underscore or other punctuation (`X-Pdbq-Claim-User_Id`), is
  repeated, or folds onto the same claim as another claim header
  (`X-Pdbq-Claim-User-Id` plus `X-Pdbq-Claim-user-id` sent as separate
  headers).

  Set `auth.headers.claims` to an allowlist of claim names (e.g.
  `["user_id", "tenant_id"]`; the role claim is always accepted). A request
  carrying any other claim header is a 401. Empty accepts any claim.

  `auth.headers.trusted_proxies` lists the gateway's CIDR blocks (or single
  IPs), e.g. `["10.0.0.0/8", "192.168.1.5"]`, and is **required** in headers
  mode: pdbq refuses to start without it. Claim headers arriving from any
  other peer are rejected with a 401 (fail closed), never ignored. The
  gateway must strip every client-supplied header with the prefix, in any
  letter case, before adding its own.
- **`none`**: no credentials; every request runs as `roles.anonymous`, or as
  the connection role with `roles.switch: false`.

### Why the claim header prefix is `X-Pdbq-Claim-`

Names that SQL sees are product-neutral (`jwt.claims.*`,
`request.jwt.claims`), so policies port between pdbq, pdbr, PostGraphile and
PostgREST unchanged. Names on the wire between a gateway and the server are
product-namespaced like every other pdbq header, so a gateway in front of
both pdbq and pdbr can tell them apart (pdbr uses `X-Pdbr-Claim-`). Set
`auth.headers.prefix` to change it.

## Roles

- **`roles.anonymous`** (default empty) is the role of requests without
  credentials. Empty refuses them: a request without credentials is
  answered `401` with `WWW-Authenticate: Bearer`, an invalid one with
  `WWW-Authenticate: Bearer error="invalid_token"`, and a role outside
  `roles.allowed` with `Bearer error="insufficient_scope"` (pdbq answers
  that one 401 too). The body keeps pdbq's error shape:
  `{"errors":[{"message":"authentication required"}]}`. Set it (e.g.
  `anonymous`) to serve unauthenticated requests under that role. In auth
  mode `none` it is required while `roles.switch` is on.
- **`roles.default`** is the role of authenticated requests without a role
  claim; empty falls back to `roles.anonymous`. A credential that resolves to
  no role at all is refused.
- **`roles.allowed`** restricts role claims (see
  [below](#restricting-assumable-roles)).
- **`roles.switch`** (default `true`) runs each request as its resolved role.
  `roles.switch: false` runs every request as the connection role:
  credentials are still verified (an invalid token is still a 401) and
  claims are still set, so policies keyed on claims (rather than roles)
  keep working; pdbq logs a warning at startup. For no authentication at
  all, also set `auth.mode: none` (what `pdbq config init` writes for a
  first run; configure both before production).

## Exposing claims to SQL (`claims`)

`claims.prefix` (default `jwt.claims`, PostGraphile's) names the per-claim
settings; set it to `pdbq.claims` to keep policies written for pdbq 0.x, or
empty to set none. `claims.json_setting` (default empty) names one setting
receiving every claim as a JSON object, so PostgREST and Supabase policies
port unchanged:

```yaml
claims:
  json_setting: request.jwt.claims
```

```sql
CREATE POLICY own_todos ON todos FOR ALL
    USING (owner = (current_setting('request.jwt.claims', true)::json->>'sub')::uuid);
```

Both can be on at once. The JSON setting must not lie under the prefix
(where a claim of that name would overwrite it).

## Example policy

From the fixture schema (`db/init/01-schema.sql`):

```sql
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY posts_public ON posts FOR SELECT
    USING (published
           OR author_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer);
```

The connection role needs `GRANT anonymous, app_user TO <connection role>`
(or be superuser) so `SET ROLE` succeeds.

A configuration behind a gateway:

```yaml
auth:
  mode: headers
  headers:
    trusted_proxies: ["10.0.0.0/8"]
    claims: [user_id]
roles:
  anonymous: anonymous
  allowed: [app_user]
```

And with Supabase-style tokens:

```yaml
auth:
  jwt:
    secret_file: /run/secrets/jwt_secret
    role_claim_path: app_metadata.role
    leeway: 30s
roles:
  anonymous: anon
  default: authenticated
  allowed: [authenticated]
claims:
  json_setting: request.jwt.claims
```

## Mutations, `RETURNING`, and column privileges

PostgreSQL evaluates `RETURNING` with the same privilege rules as a `SELECT`:
each projected column needs a column-level `SELECT` grant, and (with RLS
enabled) each returned row must pass the table's `SELECT` policies.
Two consequences for generated mutations:

- **Column-level grants.** pdbq compiles a mutation's `RETURNING` list from
  the payload selection, so it only names the columns the query actually
  reads. A role granted `SELECT` on a subset of columns can therefore run
  mutations as long as its selections stay within that subset. The exception
  is selecting a **computed column**: the underlying function takes the whole
  row type, so the statement must return every column and the role needs
  `SELECT` on all of them. A payload relation that leads back to the mutated
  table (`createPost { post { userByAuthorId { postsByAuthorId { … } } } }`) reads a
  visibility CTE merging the table with the mutation's own rows; it projects
  the columns those relations read, plus, for bulk updates and deletes, the
  primary-key (or other `NOT NULL` unique key) columns that identify the
  affected rows.

- **`SELECT` policies apply to rows returned by the mutation itself.** This
  matters for nested creates (the `nested_mutations` plugin): all inserts run
  in one statement, and rows inserted by an earlier CTE are not visible to a
  policy's subquery in the same snapshot. A child-table `SELECT` policy that
  `EXISTS`-checks its parent row can never pass when the parent was created in
  the same statement — the insert succeeds but the `RETURNING` row is
  filtered, which PostgreSQL reports as an error. Write such policies against
  values that don't require same-statement visibility (e.g. compare the FK
  column to a claim setting directly), or split the creates into separate
  mutations.

- **Nested operations run under the same policies.** A nested
  `connectBy…`/`updateBy…`/`deleteBy…` whose row the role's `UPDATE` or
  `DELETE` policy (or, for a forward `connect`, `SELECT` policy) filters out
  matches nothing, which fails and rolls back the whole mutation
  (`nested_mutations: … matched 0 rows`); a filtered `disconnectBy…` leaves
  the row as it was. An update mutation whose patch only carries nested
  fields still locks its row with `SELECT … FOR UPDATE`, so it needs the
  `UPDATE` privilege and passes the `UPDATE` policies like any update. The
  statement selects and returns only the columns the payload and the
  nested operations read, so roles with column-level `SELECT` grants work.

## Restricting assumable roles

By default any role named in the role claim may be assumed (the database
still rejects roles not granted to the connection role). Set
`roles.allowed` to pin the set explicitly:

```yaml
roles:
  allowed: [app_user, app_admin]
```

A request whose role claim resolves to anything else is rejected with a 401
(`WWW-Authenticate: Bearer error="insufficient_scope"`). `roles.default` and
`roles.anonymous` are always allowed — the operator configured those
deliberately.

Leaving `roles.allowed` empty in `jwt` or `headers` mode means a client that
controls its claims can name any role granted to the connection role —
including privileged ones. pdbq logs a startup warning in that configuration;
set the list explicitly in production.

Some names are never assumable, whatever the list says: `none` (in any
letter case; assigning it to the role setting is `RESET ROLE`, which would
run the request as the connection role and bypass RLS), `public`, and
anything starting with `pg_` (the server's predefined roles). A role claim
naming one is rejected with a 401 before the database is reached, and
configuring one as `roles.allowed`, `roles.default` or `roles.anonymous`
fails config validation at startup. `claims.prefix` and
`claims.json_setting` are validated too: dot-separated identifiers whose first component is not a
server or extension settings namespace (`plpgsql`, `pg_*`, `postgis`, ...).

## Turning it off

`roles.switch: false` runs everything as the privileged connection role and
logs a warning at startup; add `auth.mode: none` to accept requests without
credentials. Fine for local dev (the dev compose stack and `pdbq config
init` do this), never for production. The CLI commands that run without an
HTTP request (`pdbq query`, `schema`, `plugins list`) always run this way;
`pdbq query --role <name> --claims '<json>'` runs one operation under a
request's role and claims instead. See [CLI](cli.md#--role-and---claims).
