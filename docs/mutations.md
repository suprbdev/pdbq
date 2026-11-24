# Mutations and payloads

Every mutation returns a payload object in PostGraphile's shape. Like any
other root field, a table mutation and everything its payload selects
compile into **one** SQL statement. A custom mutation whose payload reads
tables compiles into two, run in one transaction (see
[What the payload sees](#what-the-payload-sees)).

## Table mutations

Per table, gated by the role's privileges, pdbq generates PostGraphile v4's
CRUD mutations plus bulk ones. Every one takes exactly one argument,
`input`, of its own input type named after the mutation field
(`UpperCamel(<field>) + "Input"`, so it follows `@name`, `simple_names` and
naming plugins). That input holds `clientMutationId`, the key or filter
selecting the rows, and the row values:

| Mutation | Field | Input |
|---|---|---|
| create | `createUser(input: CreateUserInput!): CreateUserPayload!` | `{clientMutationId: String, user: UserInput!}` |
| update by key | `updateUserById(input: UpdateUserByIdInput!): UpdateUserPayload!` | `{clientMutationId: String, id: Int!, userPatch: UserPatch!}` |
| update by unique key | `updateUserByEmail(input: UpdateUserByEmailInput!): UpdateUserPayload!` | `{clientMutationId: String, email: String!, userPatch: UserPatch!}` |
| delete by key | `deleteUserById(input: DeleteUserByIdInput!): DeleteUserPayload!` | `{clientMutationId: String, id: Int!}` |
| delete by unique key | `deleteUserByEmail(input: DeleteUserByEmailInput!): DeleteUserPayload!` | `{clientMutationId: String, email: String!}` |
| upsert | `upsertUserByEmail(input: UpsertUserByEmailInput!): UpsertUserPayload!` | `{clientMutationId: String, user: UserInput!}` |
| bulk create | `createManyUsers(input: CreateManyUsersInput!): CreateManyUsersPayload!` | `{clientMutationId: String, users: [UserInput!]!}` |
| bulk update | `updateManyUsers(input: UpdateManyUsersInput!): UpdateManyUsersPayload!` | `{clientMutationId: String, filter: UserFilter!, userPatch: UserPatch!}` |
| bulk delete | `deleteManyUsers(input: DeleteManyUsersInput!): DeleteManyUsersPayload!` | `{clientMutationId: String, filter: UserFilter!}` |

- The row values are `<Type>Input` (`UserInput`: the columns a create can
  set, required where the column is `NOT NULL` without a default) and
  `<Type>Patch` (`UserPatch`: the columns an update can set, all optional;
  setting none is an error). Neither carries `clientMutationId`. The field
  holding them is the lowerCamel type name (`user`), `<type>Patch`
  (`userPatch`) for a patch and the plural (`users`) for a bulk create.
- `<Key>` is the primary key (`ById`, shortened away by `simple_names`:
  `updateUser`, `deleteUser`, `upsertUser`) and each unique constraint
  (`ByEmail`). A keyed update or delete that matches no row is the
  `no row matched` error.
- The bulk mutations `createMany<Types>`, `updateMany<Types>` and
  `deleteMany<Types>` are named apart from the single-row ones, so a table
  named by an uncountable noun (`news`: `createNews`, `createManyNews`)
  gets both. The filter of a bulk update or delete is required; `{}`
  matches every row. They are generated only for tables with a filter
  type.
- A table whose every column PostgreSQL computes gets a create without row
  values (`createTick(input: {clientMutationId})` inserts the defaults) and
  no bulk create, upsert or update.

```graphql
mutation {
  createUser(input: {clientMutationId: "1", user: {email: "ada@example.com"}}) {
    clientMutationId
    user { id }
  }
  updateUserById(input: {id: 1, userPatch: {fullName: "Ada"}}) { user { fullName } }
  deleteUserByEmail(input: {email: "old@example.com"}) { deletedUserNodeId }
  updateManyUsers(input: {filter: {mood: {equalTo: SAD}}, userPatch: {mood: OK}}) { affectedCount }
}
```

The whole input can be one variable:
`mutation ($in: CreateUserInput!) { createUser(input: $in) { user { id } } }`.

Two table mutations never share a name: when two would get the same root
field name, or the same input or payload type name (two tables of the same
name in different schemas, a naming plugin mapping two kinds to one name,
an enum named `user_patch`), the schema build fails naming both, e.g.
`schema: mutation name collision: "createNews" is both the create mutation
of table public.news and the create mutation of table app.news; rename the
table or column with the @name smart comment, or the generated name in an
InflectionHook plugin`. A key column named like the input's own fields
(`clientMutationId`, `userPatch`) is an `input field collision` error. A
custom mutation colliding with a table mutation is skipped with a warning,
the table mutation kept.

An upsert takes the row values and, on a key conflict, overwrites only
the provided columns that the patch (`<Type>Patch`) also has:
a column tagged `@omit update` is set when the row is inserted and kept
when it already exists. Upserts are generated only for keys every column of
which the row values can set.

Identity `GENERATED ALWAYS` columns and generated (`STORED` or `VIRTUAL`)
columns are left out of `<Type>Input` and `<Type>Patch`: PostgreSQL computes
them. Identity `GENERATED BY DEFAULT` columns are in both, optional on
create like a serial column: an omitted value takes the sequence's next
one. A key on such a column therefore gets an upsert.

The single-row payloads (`CreatePostPayload`, `UpdatePostPayload`,
`DeletePostPayload`, `UpsertPostPayload`) carry:

| Field | Type | |
|---|---|---|
| `post` | `Post` | The mutated row (as it was, for a delete). |
| `clientMutationId` | `String` | Echoes `input.clientMutationId`. |
| `query` | `Query` | The query root: read anything alongside the mutation's result. |
| `userByAuthorId`, … | | The row's forward relations, named as on the row type. |
| `postEdge(orderBy:)` | `PostEdge` | An edge for adding the row to a Relay connection (tables with a primary key). |
| `deletedPostNodeId` | `ID` | Delete payloads only: the deleted row's `nodeId`, computed in the same statement from the row's keys (tables with a `nodeId`). |

`deleted<Type>NodeId` (`deletedPostNodeId`, the PostGraphile v4 name) is
named through `inflect.KindPayloadDeletedNodeID`, follows `@name` on the
table and is not affected by `simple_names`. It is present on every
`delete<Type>By<Key>` payload, including the by-unique mutations, and absent
when the table has no `nodeId` (no primary key, or `@omit read` on a key
column). A delete that matches no row is still the not-found error
(`no row matched "<alias>"`, code `not_found`; see [errors.md](errors.md)).

```graphql
mutation {
  createPost(input: {post: {authorId: 1, title: "Hello"}}) {
    post { id title }
    userByAuthorId { email }
    postEdge { cursor node { id } }
    query { allPosts { totalCount } }
  }
}
```

The bulk payloads (`CreateManyPostsPayload`, `UpdateManyPostsPayload`,
`DeleteManyPostsPayload`, named `UpperCamel(<field>) + "Payload"`) carry the
mutated rows (`posts: [Post!]!`), `affectedCount`, `clientMutationId` and
`query`.

A forward relation whose name is already one of the payload's own fields
(`clientMutationId`, `query`, the row field, `<row>Edge`,
`deleted<Type>NodeId`) is left out of that payload with a build warning; it
keeps its name on the row type, so the row field still reaches it. With
`simple_names`, a `query_id` column's `query` relation is such a case. The
relation is never renamed.

## Custom mutations

A `VOLATILE` function is a mutation taking one `input` argument:

```graphql
publishPost(input: PublishPostInput!): PublishPostPayload!

input PublishPostInput {
  postId: Int
  clientMutationId: String
}

type PublishPostPayload {
  post: Post
  clientMutationId: String
  query: Query
  userByAuthorId: User
  postEdge(orderBy: [PostsOrderBy!]): PostEdge
}
```

The result field is named the way PostGraphile names it:

| Function returns | Result field |
|---|---|
| a table row (`RETURNS posts`) | `post: Post`, plus the row's forward relations and `postEdge` |
| `SETOF posts` | `posts: [Post!]` (no relations or edge) |
| one `OUT` parameter (`OUT new_total int`) | `newTotal: Int` |
| several `OUT`/`INOUT`/`TABLE` parameters | `result: <Function>Record` (`results` for `SETOF`) |
| `int`, `float`, `boolean`, `text` | `integer`, `float`, `boolean`, `string` |
| another type | the lowerCamel type name: `mood: Mood`, `bigInt: BigInt`, `geoJson: GeoJSON`, `uuid: UUID` |
| an array of any of these | the plural: `integers: [Int!]`, `strings: [String!]` |
| `void` | no result field |
| the `auth.mint_type` composite | the composite's name (`jwt: String`, the signed token) |

A `SETOF` result list (rows, composites or scalars) holds at most
`limits.max_page_size` items, like a set-returning query function, and the
cost estimate charges it that many rows. The function still runs to
completion and writes everything it writes; only the returned list is cut
off, so a function that must return more rows pages inside the function.

Functions returning an anonymous `record` without `OUT` parameters (or
`SETOF record`) are not exposed, as in PostGraphile: the shape of the row is
only known per call (PostgreSQL requires a column definition list to read it),
so there is no sound GraphQL type, and exposing it as `JSON` would not work
for `SETOF record`. pdbq logs one `skipping function` line at startup for each
such function; declare `OUT` parameters or `RETURNS TABLE` to expose it.

### Function arguments

Custom queries (stable and immutable functions) and custom mutations map
their parameters the same way: scalars, enums, domains, PostGIS values and
arrays to the matching GraphQL types, a composite type to its `<Type>Input`.

**Table rows.** A parameter typed as a table's row type, in any position, or
an array of rows (`posts[]`), takes `<Type>RowInput` (`UserRowInput`,
`[PostRowInput]`; `<Type>Input` is a create's row values). It has one
nullable field per column of the table,
including generated, identity and primary-key columns. The argument is a
value handed to the function, not a write to the table, so `@omit create`
and `@omit update` do not apply. Columns tagged `@omit read` are left out:
the input never accepts a value the API hides. An absent field is `NULL` in
the row. The value is bound as a single `jsonb` parameter and populated with
`jsonb_populate_record(NULL::<table>, $n)`, so the SQL text never depends on
the values.

```graphql
# CREATE FUNCTION user_greeting(greeting text, u users) RETURNS text STABLE
userGreeting(greeting: String, u: UserRowInput): String
# CREATE FUNCTION rename_user(u users, new_name text) RETURNS users VOLATILE
renameUser(input: RenameUserInput!): RenameUserPayload!   # input RenameUserInput { u: UserRowInput  newName: String  clientMutationId: String }
```

A *stable* function whose first parameter is a row is a computed column on
that type (see [smart comments](smart-comments.md)), not a root field. A
*volatile* one is a custom mutation taking the row. PostGraphile takes
`UserInput` (its create input) by default and selects other variants with
`@arg0variant`. pdbq has one row input type, `<Type>RowInput`, with every
readable column, and does not read `@argNvariant`.

**Polymorphic parameters** (`anyelement`, `anyarray`, `anynonarray`,
`anyenum`, `anyrange`, `anymultirange` and the `anycompatible*` family)
only take a type when PostgreSQL sees the actual argument types of a call.
A non-polymorphic sibling parameter does not take part, so the signature
alone never fixes them. The `@anyelement <type>` / `@anycompatible <type>`
[smart comments](smart-comments.md#functions) name the type. The function
is then exposed as if declared with it: `anyarray` becomes `<type>[]` and
`anyrange` the range over `<type>`. Arguments are cast to it, which makes
PostgreSQL resolve the call the same way. Without the tag the function is
skipped and one `skipping function: polymorphic signature` line is logged.
PostGraphile skips such functions.

The `@resultFieldName <name>` [smart comment](smart-comments.md) on the
function sets the name explicitly; an `InflectionHook` plugin can override
the default through `inflect.KindFunctionResultField`.

The function is called once, in the `FROM` clause of the mutation's first
statement, whatever the payload selects (the call runs when only
`clientMutationId` is selected).

## Edges

`<row>Edge` is an edge of the table's connection type: `cursor` is the row's
`nodeId`, which is the keyset cursor of every ordering of that table's
connections (`allPosts(orderBy: [TITLE_DESC], after: <cursor>)` continues
after the row). `orderBy` is accepted for compatibility with PostGraphile
clients and does not change the cursor; pdbq's orderBy enums have no
`PRIMARY_KEY_ASC` value, so the argument has no default.

## What the payload sees

A statement reads the database through the snapshot taken when it started,
so a plain read of a table the statement modifies would see it as it was
before the modification.

- **Table mutations** read the mutated table through a visibility CTE
  wherever the payload reads it — relations leading back to it, and every
  `query` field: `createPost { query { allPosts { totalCount } } }` counts
  the new post, `deletePostById { query { postById(…) } }` no longer finds
  the deleted one. The `nested_mutations` plugin does the same for every
  table its CTEs write. Rows changed by triggers or referential actions
  (`ON DELETE CASCADE`, …) are not reflected.
- **Custom mutations** are opaque: pdbq does not know what the function
  writes, so the payload is read by a second statement, after the call, in
  the same transaction. The first statement calls the function and returns
  its result; the second builds the payload from it, and its own snapshot
  includes the function's writes:

  ```graphql
  mutation {
    writePost(input: {authorName: "grace", title: "Hello"}) {
      post { id authorByAuthorId { name } } # the author the function inserted
      query { allPosts { totalCount } } # counts the new post
    }
  }
  ```

  - The result field is the value the function returned. A returned table
    row reaches the second statement as the row's text form, which
    round-trips every column exactly, and is not read back from the table,
    so the role's RLS policies do not hide a row the function returned.
    The row's relations, computed columns and edge node, and every `query`
    field, read the database as the function left it, under the role's
    policies. A `SETOF` result keeps the order the function returned it
    in.
  - The second statement only runs when the payload reads tables: a
    `query` field other than `__typename`, a forward relation of the
    returned row, or a result or edge node selecting more than the
    returned row's own columns (`id`, `nodeId`, …). Otherwise the payload
    compiles into the call's statement: `publishPost { post { id title }
    clientMutationId }` is one statement.
  - The pair runs in the operation's transaction, like every mutation.
    An error in the second statement, and a serialization failure
    retried under `transactions.max_retries`, roll back the function's
    writes; a retry re-runs both statements.
  - `pdbq query --print-sql` prints the second statement after the first,
    under a `-- then:` line. It reads the first statement's result as
    `$1`, so its own parameters start at `$2`.

## Partial success

A mutation operation is all-or-nothing by default: its root fields run in
one transaction, and the first failing field rolls the whole operation back
and leaves the fields after it out of the response.

A request can opt in to **partial success**: every root field runs in its
own `SAVEPOINT`, a failing field is rolled back on its own and reported, and
the other fields commit. An operator enables it first:

```yaml
transactions:
  partial:
    enabled: true   # default false
    max_items: 1000 # most mutation fields per partial request
```

and a request asks for it with a request extension:

```json
{
  "query": "mutation { a: createUser(input: {user: {email: \"a@x.io\"}}) { user { id } } b: createUser(input: {user: {email: \"a@x.io\"}}) { user { id } } c: createUser(input: {user: {email: \"c@x.io\"}}) { user { id } } }",
  "extensions": { "partialMutations": true }
}
```

```json
{
  "data": { "a": { "user": { "id": 7 } }, "b": null, "c": { "user": { "id": 8 } } },
  "errors": [{ "message": "...", "path": ["b"], "extensions": { "code": "pg:23505" } }]
}
```

The extension is a request property rather than a schema directive, so the
schema, the SDL and the document cache are the same with the feature on or
off, and it works with persisted queries (it is not part of the document).
It is read from the HTTP body (or the `extensions` query parameter of a
GET, which cannot run a mutation anyway) and from WebSocket `subscribe`
messages; only the JSON value `true` turns it on, and it has no effect on a
query. While `transactions.partial.enabled` is off, a mutation asking for
it is rejected with `partial mutations are disabled` (code
`partial_mutations_disabled`) before anything runs,
so a client never believes it got partial semantics that it did not.

What a failure covers:

- A field's error is its database error, a `no row matched` error (code
  `not_found`), a
  compile error or a JWT mint failure. The field is `null`, its error
  carries `path: [<alias>]`, and everything the field wrote (a nested
  mutation's whole CTE chain, a custom mutation's two statements) is rolled
  back to the savepoint. Fields before and after it are unaffected and
  commit with the transaction.
- Fields still run in selection order, one statement at a time, so a later
  field sees the writes of the earlier ones that succeeded. Partial mode
  therefore does not use the pipelined single round trip of a plain mutation
  (it is an opt-in exception to that rule): it costs `SAVEPOINT`, the
  statement and `RELEASE` per field, plus `ROLLBACK TO` for a failed one.
- Response `data` lists every root field in the request, `null` for a failed
  one, unlike the all-or-nothing response which stops at the first error.
  Declared non-null payload types are not enforced in the response, as
  before.

Interactions:

| | |
|---|---|
| Retries (`transactions.max_retries`) | A serialization failure or deadlock is never a field error: it aborts the transaction and the **whole operation** re-runs, savepoints included. When the retries run out, nothing is committed and the response has one error without a `path` and no `data`. The same holds for any failure at `COMMIT`. |
| RLS identity | `SET LOCAL ROLE` and the claims are applied once, when the transaction begins, and every field runs under them; a savepoint rollback does not undo them. A row-level security violation (`42501`) in one field is that field's error and the others commit as the same role. |
| Nested mutations | A root field is the unit: the nested creates, updates, connects and deletes of one field commit or fail together. |
| `transactions.partial.max_items` | An operation with more mutation root fields is rejected before it runs. |
| `transactions.isolation` | Applies to the one transaction, so every field shares its snapshot rules. |
| Defer and stream | Ignored in mutations, as always; a mutation is delivered in one response. |
| `Prefer: tx=rollback` | pdbq has no such preference; see pdbr for the equivalent. |

## Differences from PostGraphile

- The bulk mutations (`createManyUsers`, `updateManyUsers`,
  `deleteManyUsers`) and upserts have no PostGraphile v4 counterpart; no
  `nodeId`-keyed root mutations (`updateUser(input: {nodeId, userPatch})`)
  are generated.
- A payload's forward relation named like one of the payload's own fields is
  left out of the payload (with a build warning) rather than renamed.
- `query` fields compile in the core compiler: `CompileHook` plugins do not
  see them, and JWT minting applies to a custom mutation's result field, not
  to a token-returning function selected under `query`.
