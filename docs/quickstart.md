# Quickstart

## With the bundled dev stack

```console
$ docker compose up
```

This starts PostgreSQL with a demo schema (`db/init/01-schema.sql`) and
pdbq in watch mode, without authentication (`PDBQ_AUTH_MODE=none`) and
running every request as the connection role (`PDBQ_ROLES_SWITCH=false`).

The stack publishes no ports by itself — networking setups differ, so port
mappings live in a gitignored `compose.override.yaml` that compose picks up
automatically. Create one that fits your machine:

```yaml
# compose.override.yaml
services:
  db:
    ports:
      - "5432:5432"
  pdbq:
    ports:
      - "8080:8080"
```

Then open <http://localhost:8080> for GraphiQL.

Try:

```graphql
{
  allUsers(first: 2, orderBy: [EMAIL_ASC]) {
    totalCount
    nodes {
      email
      mood
      postsByAuthorId { nodes { title published } }
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

(The names are PostGraphile v4's. With
`plugins.settings.simple_names.enabled: true` the same query is `users` /
`posts`; see [plugins.md](plugins.md#worked-example-1-simple_names).)

Because watch mode is on, `ALTER TABLE`/`CREATE TABLE` in the database is
picked up automatically: pdbq re-introspects and swaps the schema without a
restart.

## Against your own database

```console
$ pdbq config init                        # starter pdbq.yaml
$ $EDITOR pdbq.yaml                       # set database.url
$ pdbq serve
```

The starter runs with nothing but `database.url` filled in: it sets
`auth.mode: none` and `roles.switch: false`, so every request runs as the
connection role without credentials (pdbq logs a warning). Configure
`auth` and `roles` before production; see [Row-level security](rls.md).
Without them the defaults expect a JWT secret (`auth.jwt.secret`) and refuse
requests without a token.

Every option is also an env var (`PDBQ_DATABASE_URL=...`) or flag
(`--database.url=...`); precedence is flags > env > file.

Without a file, the same first run is one command:

```console
$ pdbq serve --database.url postgres://localhost/mydb \
    --auth.mode none --roles.switch=false --server.graphiql
```

Plain `pdbq serve --database.url ...` fails validation on purpose: the
defaults verify JWTs and need a secret, see the next section.

## Secure setup

The first run above uses the connection role for every request and ignores
row-level security. This is the same app with real roles, a JWT and a policy.
The SQL is identical for pdbr.

1. Create the roles and an example table. The login role inherits the
   request roles: pdbq introspects with the connection role's own privileges,
   so a table that role cannot reach would not be generated (do not create it
   `NOINHERIT` as for PostgREST). Each request then runs `SET LOCAL ROLE` to
   the role in its token.

   ```sql
   CREATE ROLE authenticator LOGIN PASSWORD 'change-me';
   CREATE ROLE anonymous NOLOGIN;
   CREATE ROLE authenticated NOLOGIN;
   GRANT anonymous, authenticated TO authenticator;

   CREATE SCHEMA app;
   CREATE TABLE app.todos (
       id    serial PRIMARY KEY,
       owner text NOT NULL DEFAULT current_setting('jwt.claims.sub', true),
       title text NOT NULL,
       done  boolean NOT NULL DEFAULT false
   );
   CREATE INDEX todos_owner_idx ON app.todos (owner);

   GRANT USAGE ON SCHEMA app TO anonymous, authenticated;
   GRANT SELECT, INSERT, UPDATE, DELETE ON app.todos TO authenticated;
   GRANT USAGE ON SEQUENCE app.todos_id_seq TO authenticated;

   ALTER TABLE app.todos ENABLE ROW LEVEL SECURITY;
   ALTER TABLE app.todos FORCE ROW LEVEL SECURITY;
   CREATE POLICY own_todos ON app.todos TO authenticated
       USING (owner = current_setting('jwt.claims.sub', true))
       WITH CHECK (owner = current_setting('jwt.claims.sub', true));
   ```

2. Start pdbq with a secret from the environment. `auth.mode: jwt` and
   `roles.switch: true` are the defaults; `roles.allowed` pins which roles a
   token may name. Requests without a token get `401` until you set
   `roles.anonymous: anonymous`.

   ```console
   $ export PDBQ_DATABASE_URL=postgres://authenticator:change-me@localhost/mydb
   $ export PDBQ_AUTH_JWT_SECRET=$(openssl rand -hex 32)
   $ export PDBQ_ROLES_ALLOWED=authenticated
   $ pdbq serve --schema.schemas app
   ```

3. Mint a token for `alice`. Tokens need an `exp` claim, and the `role` claim
   picks the database role. (`auth.mint_type` can have the database mint
   tokens from a login function instead, see [RLS](rls.md).)

   ```console
   $ b64() { openssl base64 -A | tr '+/' '-_' | tr -d '='; }
   $ h=$(printf '{"alg":"HS256","typ":"JWT"}' | b64)
   $ p=$(printf '{"role":"authenticated","sub":"alice","exp":%d}' $(( $(date +%s) + 3600 )) | b64)
   $ s=$(printf '%s.%s' "$h" "$p" | openssl dgst -sha256 -hmac "$PDBQ_AUTH_JWT_SECRET" -binary | b64)
   $ TOKEN=$h.$p.$s
   ```

4. Call the API. Each user sees only their own rows:

   ```console
   $ curl -s localhost:8080/graphql -H "Authorization: Bearer $TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"query":"mutation { createTodo(input: {title: \"buy milk\"}) { todo { id owner title } } }"}'
   {"data":{"createTodo":{"todo":{"id":1,"owner":"alice","title":"buy milk"}}}}
   $ curl -s localhost:8080/graphql -H "Authorization: Bearer $TOKEN" \
       -H 'Content-Type: application/json' \
       -d '{"query":"{ todos { nodes { id title owner } } }"}'
   $ curl -si localhost:8080/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ todos { nodes { id } } }"}' | head -1
   HTTP/1.1 401 Unauthorized
   ```

For production also terminate TLS in front of pdbq, boot from a schema cache
and set `server.introspection: false`; the checklist is in
[deployment.md](deployment.md#production-checklist).

## What gets generated

For each table/view the connection role can `SELECT`:

| Postgres | GraphQL |
|---|---|
| table `users` | type `User implements Node` (global `nodeId`) |
| rows | `allUsers(first, last, offset, before, after, orderBy, filter): UserConnection!` |
| primary key | `userById(id)`, `node(nodeId)` |
| unique constraints | `userByEmail(email)` |
| foreign keys | `post.userByAuthorId`, `user.postsByAuthorId` (child connection) |
| `INSERT`/`UPDATE`/`DELETE` privileges | `createUser`, `updateUserById`, `deleteUserById` |
| enums | GraphQL enums (`HAPPY`, `SAD`, ...) |
| composite types | object types with per-field selection (`address { street city }`), plus `AddressInput` on mutations; arrays too |
| rows inside other values (`posts[]`, composite attributes, `RETURNS TABLE` columns) | `PostRow` object types holding the columns `Post` exposes |
| functions | queries (stable/immutable) or mutations (volatile) |

One-off queries from the shell:

```console
$ echo '{allUsers {nodes {email}}}' | pdbq query -
$ pdbq query '{userById(id: 1) {email}}'
```

Next: [configuration.md](configuration.md), [rls.md](rls.md), [migrating-from-postgraphile.md](migrating-from-postgraphile.md).
