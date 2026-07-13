# RLS & authentication

RLS support is **on by default**. Each operation runs inside a transaction
that:

1. `SET LOCAL ROLE <role>` — the role from the request's claims
   (`rls.role_claim`, default claim `role`), falling back to
   `rls.default_role`, then `rls.anonymous_role` for unauthenticated
   requests.
2. `SELECT set_config('pdbq.claims.<key>', <value>, true)` for every claim —
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

## Turning it off

`rls.enabled: false` runs everything as the privileged connection role and
logs a loud warning at startup. Fine for local dev, never for production.
