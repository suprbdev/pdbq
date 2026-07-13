# Quickstart

## With the bundled dev stack

```console
$ docker compose up
```

This starts PostgreSQL with a demo schema (`db/init/01-schema.sql`) and
pdbq in watch mode.

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
  users(first: 2, orderBy: [EMAIL_ASC]) {
    totalCount
    nodes {
      email
      mood
      posts { nodes { title published } }
    }
    pageInfo { hasNextPage endCursor }
  }
}
```

(The dev stack has the `simple-names` plugin enabled — with it disabled the
same query is `allUsers` / `postsByAuthorId`.)

Because watch mode is on, `ALTER TABLE`/`CREATE TABLE` in the database is
picked up automatically: pdbq re-introspects and swaps the schema without a
restart.

## Against your own database

```console
$ pdbq config init                        # starter pdbq.yaml
$ $EDITOR pdbq.yaml                       # set database.url
$ pdbq serve
```

Every option is also an env var (`PDBQ_DATABASE_URL=...`) or flag
(`--database.url=...`); precedence is flags > env > file.

## What gets generated

For each table/view the connection role can `SELECT`:

| Postgres | GraphQL |
|---|---|
| table `users` | type `User implements Node` (global `nodeId`) |
| rows | `allUsers(first, last, offset, before, after, orderBy, filter): UserConnection!` |
| primary key | `userById(id)`, `node(nodeId)` |
| unique constraints | `userByEmail(email)` |
| foreign keys | `post.author`, `user.postsByAuthorId` (child connection) |
| `INSERT`/`UPDATE`/`DELETE` privileges | `createUser`, `updateUserById`, `deleteUserById` |
| enums | GraphQL enums (`HAPPY`, `SAD`, ...) |
| composite types | object types with per-field selection (`address { street city }`), plus `AddressInput` on mutations; arrays too |
| functions | queries (stable/immutable) or mutations (volatile) |

One-off queries from the shell:

```console
$ echo '{allUsers {nodes {email}}}' | pdbq query -
$ pdbq query '{userById(id: 1) {email}}' --var id=1
```

Next: [configuration.md](configuration.md), [rls.md](rls.md).
