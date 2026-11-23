# Incremental delivery (`@defer` and `@stream`)

> **Experimental.** Incremental delivery (`@defer` and `@stream`) is outside the 1.0 compatibility promise and may change in a minor release. See [Compatibility and versioning](compatibility.md#experimental-features).

pdbq implements GraphQL incremental delivery: `@defer` on fragments and
`@stream` on list fields. The response shape follows the incremental delivery
RFC as graphql-js 17 and GraphQL Yoga implement it (`pending`, `incremental`,
`completed`, `hasNext`, payloads matched by `id`). The earlier
`deferSpec=20220824` shape is also served for clients that ask for it.

Incremental delivery is **off by default**:

```yaml
incremental:
  enabled: true
```

While it is off, the schema declares neither directive. Its SDL is
byte-for-byte the same as before the feature existed. `@stream` fails
validation, and `@defer` is accepted (gqlparser's built-in declaration) but
ignored: the fragment's data is part of the one result.

## Schema

With the option on, the SDL starts with the directive declarations of
graphql-js 17:

```graphql
directive @defer(if: Boolean! = true, label: String) on FRAGMENT_SPREAD | INLINE_FRAGMENT
directive @stream(initialCount: Int! = 0, if: Boolean! = true, label: String) on FIELD
```

## Example

```graphql
{
  allPosts(first: 20, orderBy: [ID_ASC]) {
    totalCount
    nodes @stream(initialCount: 5) {
      id
      title
      ... @defer(label: "author") { author { fullName } }
    }
  }
  userById(id: 1) {
    email
    ... @defer(label: "posts") { postsByAuthorId { totalCount } }
  }
}
```

The first payload holds `totalCount`, the first five posts and the user's
email. Later payloads bring the other posts in batches and the user's post
count. The `author` fragment sits inside a streamed item, so it is delivered
with its item (see [What is delivered incrementally](#what-is-delivered-incrementally)).

## Transports

### HTTP

A query is delivered incrementally when its `Accept` header lists
`multipart/mixed`:

| `Accept` entry | Payload shape | Response `Content-Type` |
|---|---|---|
| `multipart/mixed;incrementalSpec=v0.2` (Apollo Client 4 `GraphQL17Alpha9Handler`) | `pending`/`incremental`/`completed` | `multipart/mixed; boundary="-"; incrementalSpec=v0.2` |
| `multipart/mixed` | same | same |
| `multipart/mixed;deferSpec=20220824` (Apollo Client 3, `Defer20220824Handler`) | `incremental` entries with `path` and `label` | `multipart/mixed; boundary="-"; deferSpec=20220824` |
| `deferSpec` or `incrementalSpec` with any other value | — | `406 Not Acceptable` |

The response uses the framing of GraphQL Yoga and Apollo Router. Each payload
is a part with `Content-Type: application/json; charset=utf-8`, and pdbq
flushes each part as soon as it is ready:

```text

---
Content-Type: application/json; charset=utf-8
Content-Length: 123

{"data":{...},"pending":[...],"hasNext":true}
---
Content-Type: application/json; charset=utf-8
Content-Length: 87

{"hasNext":false,"incremental":[...],"completed":[...]}
-----
```

An operation with nothing to deliver incrementally is answered with a plain
`application/json` result, even when the client accepts `multipart/mixed`.
This covers mutations, operations whose directives are all `if: false`, and a
stream whose list fits in `initialCount`. Request errors (parse, validation,
limits) are answered the same way.

A request that accepts `multipart/mixed` bypasses `server.compression`,
because gzip would buffer the parts. `server.request_timeout` still bounds the
whole response.

### WebSocket

With `subscriptions.enabled` on, queries sent over `graphql-transport-ws` (or
the legacy `graphql-ws`) are delivered incrementally as several `next`
(`data`) messages, one per payload, followed by `complete`. WebSocket has no
content negotiation, so these payloads always use the `incrementalSpec=v0.2`
shape. A `complete` from the client cancels the delivery.

## Payloads

`incrementalSpec=v0.2`:

```json
{"data":{"userById":{"email":"ada@example.com"}},"pending":[{"id":"0","path":["userById"],"label":"posts"}],"hasNext":true}
{"hasNext":false,"incremental":[{"id":"0","data":{"postsByAuthorId":{"totalCount":2}}}],"completed":[{"id":"0"}]}
```

Streams announce the list's path. Each later payload has `items` to append,
and the stream's `completed` entry follows its last items:

```json
{"data":{"allPosts":{"nodes":[{"id":1}]}},"pending":[{"id":"0","path":["allPosts","nodes"]}],"hasNext":true}
{"hasNext":true,"incremental":[{"id":"0","items":[{"id":2},{"id":3}]}]}
{"hasNext":false,"incremental":[{"id":"0","items":[{"id":4}]}],"completed":[{"id":"0"}]}
```

`deferSpec=20220824` drops `pending` and `completed`. Each `incremental` entry
carries its `path` and `label` instead. A stream's path ends with the index of
its first item:

```json
{"data":{"allPosts":{"nodes":[{"id":1}]}},"hasNext":true}
{"hasNext":true,"incremental":[{"items":[{"id":2},{"id":3}],"path":["allPosts","nodes",1]}]}
{"hasNext":false,"incremental":[{"items":[{"id":4}],"path":["allPosts","nodes",3]}]}
```

A deferred fragment under a list is announced once per item, with the item's
index in its path (`["allUsers","nodes",3]`). The data of a deferred fragment
holds all of the fragment's fields, including those the parent payload already
delivered. Clients merge it into the object at the path.

Records are delivered in document order, breadth first. A nested deferred
fragment is announced in the payload that delivers its parent fragment.

## How it executes

pdbq keeps compiling each root field to one SQL statement; incremental
delivery runs a few more:

- **One snapshot.** Every statement of an incremental response runs in one
  transaction at `REPEATABLE READ` (`SERIALIZABLE` when
  `transactions.isolation` is `serializable`), so all payloads read the
  database as it was when the first statement ran. Writes committed while the
  response is being delivered never show up in its later payloads. The
  transaction is not retried: a payload already sent cannot be taken back.
  It holds one pooled connection until the last payload is written, so a
  slow client keeps that connection for up to `server.request_timeout`, and
  a client that stops reading for up to `incremental.write_timeout`
  (see [Limits and errors](#limits-and-errors)).
- **RLS identity.** The role and claims are applied once, at the start of
  that transaction, so every deferred fragment and stream batch runs under the
  request's identity.
- **Deferred fragments.** The initial statements leave deferred fragments
  out. Each fragment then compiles to its own statement per root field, which
  re-runs the path from the root down to the fragment (with the same
  arguments, so it reaches the same rows in the snapshot) and selects only the
  fragment's fields. A fragment under a list runs once for all its items, not
  once per item, and its data is matched to the items by position. Table
  lists always end their order with the primary key, so positions match.
  A list with no stable order (a table without a primary key, a
  set-returning function) depends on the snapshot returning its rows in the
  same order twice.
- **Streams.** The initial statement selects the first `initialCount` items
  of the list. When the connection selects other fields too (`pageInfo`,
  `totalCount`, aggregates), the streamed list becomes a subquery of its own,
  so those fields still describe the whole page. Each later batch reads the
  next `incremental.stream_batch_size` items of the same page with
  `OFFSET`/`LIMIT` in the same snapshot. The stream ends at the first short
  batch.

## What is delivered incrementally

pdbq may deliver a directive's data in the parent payload instead, which the
RFC allows. That happens:

- in **mutations and subscriptions** (both directives are ignored; only
  queries are delivered incrementally);
- for `@stream` on any list other than the `nodes` or `edges` of a
  **connection over a table with a primary key, paged forward** (`first`, or
  neither `first` nor `last`). Backward pages (`last`), tables without a
  primary key, scalar arrays and set-returning functions return the whole
  list;
- for `@stream` **below a list** (each item would need its own stream), and
  for `@defer` or `@stream` **inside a streamed item**;
- under a field of an **interface type** (`node`, `relatedNode`), where the
  fragment's type condition is not known to hold;
- inside **introspection** fields.

A stream whose list has fewer than `initialCount` items is complete in the
first payload and is not announced.

## Validation

With the option on, these rules apply to every operation:

- `@stream` only on list fields;
- `initialCount` must not be negative;
- a `label` must be a literal string (not a variable), and unique among the
  document's `@defer` and `@stream` directives.

## Limits and errors

- `limits.max_depth`, `limits.max_cost` and `limits.max_selections` measure the
  whole operation, deferred fragments included.
- `incremental.max_deferred` (16) caps the `@defer` fragments and
  `@stream` fields one operation may apply. Each of them runs its own
  statements.
- `server.request_timeout` covers the whole response. When it expires, a last
  payload completes every pending fragment and stream with a
  `request timeout` error and `hasNext: false`. A client that disconnects
  cancels the transaction.
- `incremental.write_timeout` (10s) bounds each payload of an HTTP
  response: a write the client has not taken in that time (it stopped
  reading, and the socket buffers are full) fails, the transaction rolls
  back and the connection is closed without a last payload. No write
  outlasts `server.request_timeout` either, so the pooled connection is
  released by then whatever the client does. Over WebSocket each message
  has a fixed 10 second write timeout; a write that misses it closes the
  WebSocket connection, which ends its transactions.
- `incremental.max_responses` caps the incremental responses holding
  a transaction at once, over HTTP and WebSocket and over every contract
  (default 5; it must be below `database.max_conns`), so incremental
  clients cannot take the whole pool. A query beyond it is
  answered by one result, as if the directives were absent: an
  `application/json` response over HTTP, one `next` message over
  WebSocket.
- A deferred fragment or stream batch that fails is completed with its errors
  (`completed: [{id, errors}]`; in `deferSpec=20220824`, an entry with
  `data: null` or `items: null`). A database error aborts the transaction, so
  the fragments and streams after it fail too.
