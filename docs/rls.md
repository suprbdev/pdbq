# RLS & authentication

RLS support is **on by default**. Each operation runs inside a transaction
that first applies the identity, in one statement sent in the same pipelined
batch as the operation's root field statements:

1. `SET LOCAL ROLE <role>` (as `set_config('role', $1, true)`, the same
   operation with the role as a bind parameter) — the role from the
   request's claims (`rls.role_claim`, default claim `role`), falling back
   to `rls.default_role`, then `rls.anonymous_role` for unauthenticated
   requests.
2. `set_config('pdbq.claims.<key>', <value>, true)` for every claim —
   readable in policies via `current_setting('pdbq.claims.user_id', true)`.

Only `SET LOCAL` / transaction-scoped `set_config` are ever used, so pooled
connections cannot leak one request's identity into another.

## Claim sources (`rls.auth.mode`)

- **`jwt`** (default): `Authorization: Bearer <token>`, HMAC-verified with
  `rls.auth.jwt_secret`; optional `jwt_issuer`/`jwt_audience` checks. Tokens
  must carry an `exp` claim — tokens without one are rejected, so a stolen
  token cannot stay valid forever. A missing token is anonymous; an invalid
  token is a 401.

  Set `rls.auth.jwks_url` for asymmetric verification instead (RS256/384/512,
  ES256/384/512): keys are fetched from the JWKS endpoint, matched by `kid`,
  and cached for `rls.auth.jwks_cache_ttl` (default 1h; an unknown `kid`
  refreshes early, so key rotation just works). In JWKS mode the HMAC
  algorithms are off the allowlist entirely — no downgrade to
  HMAC-with-public-key.
- **`headers`**: for deployments behind a trusted gateway that has already
  authenticated the caller. Headers matching `rls.auth.header_prefix`
  (default `X-Pdbq-Claim-`) become claims: `X-Pdbq-Claim-User-Id: 42` →
  `pdbq.claims.user_id = '42'`.

  Set `rls.auth.trusted_proxies` to the gateway's CIDR blocks (or single
  IPs), e.g. `["10.0.0.0/8", "192.168.1.5"]`: claim headers arriving from
  any other peer are rejected. Left empty, **every** peer that can reach the
  port can forge claims and roles — pdbq logs a startup warning; only do
  this when the network layer guarantees that nothing but the gateway can
  connect.
- **`none`**: every request is anonymous.

## Example policy

From the fixture schema (`db/init/01-schema.sql`):

```sql
ALTER TABLE posts ENABLE ROW LEVEL SECURITY;
CREATE POLICY posts_public ON posts FOR SELECT
    USING (published
           OR author_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer);
```

The connection role needs `GRANT anonymous, app_user TO <connection role>`
(or be superuser) so `SET ROLE` succeeds.

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
  table (`createPost { post { author { postsByAuthorId { … } } } }`) reads a
  visibility CTE merging the table with the mutation's own rows; it projects
  the columns those relations read, plus, for bulk updates and deletes, the
  primary-key (or other `NOT NULL` unique key) columns that identify the
  affected rows.

- **`SELECT` policies apply to rows returned by the mutation itself.** This
  matters for nested creates (the `nested-mutations` plugin): all inserts run
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
  (`nested-mutations: … matched 0 rows`); a filtered `disconnectBy…` leaves
  the row as it was. An update mutation whose patch only carries nested
  fields still locks its row with `SELECT … FOR UPDATE`, so it needs the
  `UPDATE` privilege and passes the `UPDATE` policies like any update. The
  statement selects and returns only the columns the payload and the
  nested operations read, so roles with column-level `SELECT` grants work.

## Restricting assumable roles

By default any role named in the role claim may be assumed (the database
still rejects roles not granted to the connection role). Set
`rls.allowed_roles` to pin the set explicitly:

```yaml
rls:
  allowed_roles: [app_user, app_admin]
```

A request whose role claim resolves to anything else is rejected with a 401.
`rls.default_role` and `rls.anonymous_role` are always allowed — the operator
configured those deliberately.

Leaving `allowed_roles` empty in `jwt` or `headers` mode means a client that
controls its claims can name any role granted to the connection role —
including privileged ones. pdbq logs a startup warning in that configuration;
set the list explicitly in production.

Some names are never assumable, whatever the list says: `none` (in any
letter case; assigning it to the role setting is `RESET ROLE`, which would
run the request as the connection role and bypass RLS), `public`, and
anything starting with `pg_` (the server's predefined roles). A role claim
naming one is rejected with a 401 before the database is reached, and
configuring one as `rls.allowed_roles`, `rls.default_role` or
`rls.anonymous_role` fails config validation at startup. `rls.claims_prefix`
is validated too: dot-separated identifiers whose first component is not a
server or extension settings namespace (`plpgsql`, `pg_*`, `postgis`, ...).

## Turning it off

`rls.enabled: false` runs everything as the privileged connection role and
logs a loud warning at startup. Fine for local dev, never for production.
