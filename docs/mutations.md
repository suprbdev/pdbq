# Mutations and payloads

Every mutation returns a payload object in PostGraphile's shape. Like any
other root field, a table mutation and everything its payload selects
compile into **one** SQL statement. A custom mutation whose payload reads
tables compiles into two, run in one transaction (see
[What the payload sees](#what-the-payload-sees)).

## Table mutations

Per table (gated by the role's privileges): `create<Type>(input:)`,
`update<Type>By<Key>(…, patch:)`, `delete<Type>By<Key>(…)`,
`upsert<Type>By<Key>(input:)`, and the bulk `create<Types>`,
`update<Types>(filter:, patch:)`, `delete<Types>(filter:)`.

The single-row payloads (`CreatePostPayload`, `UpdatePostPayload`,
`DeletePostPayload`, `UpsertPostPayload`) carry:

| Field | Type | |
|---|---|---|
| `post` | `Post` | The mutated row (as it was, for a delete). |
| `clientMutationId` | `String` | Echoes `input.clientMutationId` (`patch.clientMutationId` for updates, the `clientMutationId` argument for deletes). |
| `query` | `Query` | The query root: read anything alongside the mutation's result. |
| `author`, … | | The row's forward relations, named as on the row type. |
| `postEdge(orderBy:)` | `PostEdge` | An edge for adding the row to a Relay connection (tables with a primary key). |

```graphql
mutation {
  createPost(input: {authorId: 1, title: "Hello"}) {
    post { id title }
    author { email }
    postEdge { cursor node { id } }
    query { allPosts { totalCount } }
  }
}
```

The bulk payloads carry the mutated rows (`posts: [Post!]!`),
`affectedCount`, `clientMutationId` and `query`.

A forward relation whose name is already a payload field (a `query_id`
column's `query` relation, say) takes the `<referencedTable>By<Columns>` form
on the payload (`savedQueryByQueryId`); it keeps its name on the row type.

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
  author: User
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
| the `rls.auth.jwt_type` composite | the composite's name (`jwt: String`, the signed token) |

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
  the deleted one. The `nested-mutations` plugin does the same for every
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
      post { id author { name } }      # the author the function inserted
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
  - The pair always runs in a transaction, also with
    `transactions.mutations: false` (then that root field alone runs in
    one). An error in the second statement, and a serialization failure
    retried under `transactions.max_retries`, roll back the function's
    writes; a retry re-runs both statements.
  - `pdbq query --print-sql` prints the second statement after the first,
    under a `-- then:` line. It reads the first statement's result as
    `$1`, so its own parameters start at `$2`.

## Differences from PostGraphile

- Payload relations use pdbq's relation names (`author`, not
  `userByAuthorId`), the same as on the row type.
- Delete payloads have no `deleted<Type>NodeId` field; select the deleted
  row's `nodeId` from the row field instead.
- `query` fields compile in the core compiler: `CompileHook` plugins do not
  see them, and JWT minting applies to a custom mutation's result field, not
  to a token-returning function selected under `query`.
