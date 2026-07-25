# Filtering & ordering

Every table gets a `<Type>Filter` input with `and` / `or` / `not`
combinators plus one field per filterable column, typed by an operator set
matching the Postgres type:

| Postgres type | Operators |
|---|---|
| all scalars | `equalTo notEqualTo in notIn isNull lessThan lessThanOrEqualTo greaterThan greaterThanOrEqualTo` |
| text/citext | + `like likeInsensitive startsWith endsWith` |
| arrays | `equalTo notEqualTo contains containedBy overlaps isNull` |
| json/jsonb | `equalTo contains containedBy containsKey pathExists pathMatch isNull` |
| enums | scalar set, with GraphQL enum values |

`startsWith`/`endsWith` escape `%`/`_`/`\` in the operand; every operand is
a bind parameter — the SQL text never varies with input values (fuzz-tested
invariant).

`pathExists` (`@?`) and `pathMatch` (`@@`) take a
[SQL/JSON path](https://www.postgresql.org/docs/current/functions-json.html#FUNCTIONS-SQLJSON-PATH)
string, e.g. `{settings: {pathMatch: "$.theme == \"dark\""}}`; `json` columns
are cast to `jsonb` since the jsonpath operators exist only for `jsonb`.

Composite-typed columns are never filterable or orderable (regardless of
indexes or `allow_columns`): they map to object types, which have no scalar
operator set.

```graphql
{
  allUsers(filter: {
    and: [
      {mood: {in: [HAPPY, OK]}}
      {or: [{email: {endsWith: "@example.com"}}, {tags: {contains: ["admin"]}}]}
      {not: {settings: {containsKey: "banned"}}}
    ]
  }) { nodes { email } }
}
```

## The condition shorthand

Every table also gets a `<Type>Condition` input — the zero-ceremony
equality variant of the filter. One field per filterable column, typed as
the column's plain input type; every provided field must equal its value
and all fields are ANDed. An explicit `null` matches SQL `NULL`
(`IS NULL`).

```graphql
{
  allPosts(condition: {authorId: 1, title: "Hello"}) { nodes { id } }
}
```

`condition` and `filter` can be passed together; their conditions are
ANDed. The condition surface follows the same column policy as the filter
(indexed-only, `allow_columns`, `@omit filter` smart comments).

## The indexed-only policy

By default **only columns covered by an index** (leading column, plus PK and
unique-constraint columns) are filterable and orderable. An unindexed column
simply does not appear in `<Type>Filter` / `<Types>OrderBy`, so accidental
sequential-scan APIs cannot be built.

Loosen it globally or per column:

```yaml
filters:
  indexed_only: true
  allow_columns:
    public.posts: [published]   # extra columns despite the policy
```

or `filters.indexed_only: false` to expose everything.

## Ordering & pagination

`orderBy` takes a list of `<COLUMN>_ASC|_DESC` enum values; the primary key
is always appended as a tiebreaker so pagination is stable.

Every collection is a Relay connection (`nodes`, `edges { cursor node }`,
`totalCount`, `pageInfo`) paginated with `first`/`last`/`offset`/`before`/
`after`. Cursors are keyset-backed: a cursor is the row's `nodeId`
(base64 of `["Type", pk...]`), and `after`/`before` compile to index-friendly
lexicographic predicates anchored on that row under the current `orderBy` —
no `OFFSET` scans. Tables without a primary key fall back to offset-backed
cursors (and have no `nodeId`). Notes:

- `first` + `last` combined is rejected; `offset` cannot combine with
  `last`/`before`.
- A cursor stays decodable if `orderBy` changes, but the page is then
  relative to the anchor row under the *new* order.
- If the anchor row was deleted: with an ordering entirely on primary-key
  columns (including the default order) pagination continues past where the
  row used to be; an ordering involving other columns needs the anchor row's
  values, so the page comes back empty.
- `hasNextPage`/`hasPreviousPage` are exact in the direction being paginated
  (one extra row is fetched); the opposite side reflects the supplied
  cursors.

## Relational ordering

With the `advanced-filters` plugin (on by default), each `<Types>OrderBy`
enum also carries one-level relational values named
`<RELATION>__<COLUMN>_ASC|_DESC` — a double underscore separates the relation
from the column:

```graphql
{ allPosts(orderBy: [AUTHOR__EMAIL_ASC]) { nodes { title } } }        # forward
{ allUsers(orderBy: [POSTS_BY_AUTHOR_ID__TITLE_DESC]) { nodes { email } } }  # reverse
```

- **Forward** (FK on the ordered table): rows sort by the referenced parent
  row's column, compiled as a correlated scalar subquery.
- **Reverse** (FK pointing back at the ordered table): rows sort by an
  aggregate over the referencing rows — `MIN(column)` for `_ASC` values,
  `MAX(column)` for `_DESC`. Rows with no related row yield `NULL`
  (PostgreSQL default: last under `ASC`, first under `DESC`).

The related table's columns pass through the same
[indexed-only policy](#the-indexed-only-policy) as its own `orderBy` enum,
`@omit order` on the FK comment removes the values, and keyset cursor
pagination works under relational orderings (the anchor row's value is
re-evaluated by subquery). Disable with
`plugins.settings.advanced-filters.relation_order: false`.

## distinctOn

`distinctOn` takes a list of `<Types>DistinctOn` column enum values and
compiles to `SELECT DISTINCT ON (...)`: one row per distinct combination,
picking the first row per group under the effective `orderBy` (the distinct
columns are moved to the front of the `ORDER BY`, keeping your direction).
`totalCount` counts distinct groups. Because de-duplication changes row
identity, `distinctOn` only supports `first`/`offset` pagination — `last`,
`before`, and keyset `after` cursors are rejected.

```graphql
{ allUsers(distinctOn: [MOOD], orderBy: [MOOD_ASC]) { nodes { mood } } }
```
