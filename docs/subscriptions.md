# Subscriptions

pdbq serves PostGraphile's **simple subscriptions** (`--simple-subscriptions`,
`@graphile/pg-pubsub`): a client subscribes to a topic, the database sends
`pg_notify` on that topic's channel, and each notification becomes a
subscription event. The event can name a row (`__node__`), which the client
selects through `relatedNode`, and it can run any query through `query`.
Both are resolved under the subscriber's own RLS identity.

Subscriptions are **off by default**. While they are off, the generated
schema has no `Subscription` type and is byte-for-byte what it was before
subscriptions existed.

```yaml
server:
  subscriptions: true
```

## Schema

```graphql
type ListenPayload {
  query: Query
  relatedNode: Node
  relatedNodeId: ID
}

type Subscription {
  listen(topic: String!, initialEvent: Boolean! = false): ListenPayload!
}
```

The names and types match PostGraphile v4. `relatedNode` and `relatedNodeId`
exist only when the schema has the `Node` interface, meaning at least one
table has a primary key. With `initialEvent: true` the subscription sends one
event as soon as it is listening. That event has no node, so `relatedNodeId`
and `relatedNode` are `null`, but `query` resolves as usual.

## Sending events

The channel is `server.subscriptions_topic_prefix` (`postgraphile:` by
default) followed by the topic. The payload is text. When it is a JSON object
with a `__node__` array of `["TypeName", primary key...]`, that array names
the related row:

```sql
-- from anywhere: a trigger, a function, a job
SELECT pg_notify('postgraphile:posts', json_build_object('__node__', json_build_array('Post', 42))::text);
```

```sql
CREATE FUNCTION notify_post_change() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  PERFORM pg_notify('postgraphile:posts',
    json_build_object('__node__', json_build_array('Post', NEW.id))::text);
  RETURN NEW;
END $$;
CREATE TRIGGER posts_notify AFTER INSERT OR UPDATE ON posts
  FOR EACH ROW EXECUTE FUNCTION notify_post_change();
```

The first element is the GraphQL type name, the same name a pdbq `nodeId`
starts with (base64-decode a `nodeId` to check). It is not always the table
name, and it can differ from the identifier PostGraphile puts in its node IDs.
`relatedNodeId` is the base64 of the array rendered as PostgreSQL renders
`jsonb` (`["Post", 42]`), so it equals the `nodeId` pdbq returns for that
row. A payload that is not JSON, or has no `__node__`, gives an event with
`relatedNodeId: null` and `relatedNode: null`. `NOTIFY` is transactional:
events are sent when the notifying transaction commits.

```graphql
subscription {
  listen(topic: "posts") {
    relatedNodeId
    relatedNode { ... on Post { id title author { fullName } } }
    query { allPosts(first: 5) { totalCount } }
  }
}
```

## Transport

WebSocket upgrades on the GraphQL endpoint (`/graphql`) are accepted with
either of two subprotocols:

- **`graphql-transport-ws`**: the protocol of the
  [graphql-ws](https://github.com/enisdenjo/graphql-ws) library, used by
  current Apollo Client, urql and Relay setups. Supported messages are
  `connection_init`, `connection_ack`, `ping`, `pong`, `subscribe`, `next`,
  `error` and `complete`. Queries and mutations sent with `subscribe` get one
  `next` message followed by `complete`. The close codes follow the
  protocol: `4400` for an invalid message, `4401` for `subscribe` before
  the connection was acknowledged, `4403` when `connection_init` is
  rejected or the token expired, `4406` for an unknown subprotocol, `4408`
  when `connection_init` does not arrive within 10 seconds, `4409` for a
  duplicate operation id, and `4429` for a second `connection_init`.
- **`graphql-ws`**: the legacy `subscriptions-transport-ws` protocol
  (`connection_init`, `connection_ack`, `connection_error`, `ka`, `start`,
  `data`, `error`, `stop`, `complete`, `connection_terminate`). Request
  errors come back as a `data` message carrying `errors`, followed by
  `complete`.

Any other subprotocol is closed with `4406`. Plain HTTP `POST /graphql` with
a subscription operation answers with an error.

WebSocket upgrades skip the HTTP middleware chain: gzip, CORS headers and
HTTP metrics do not apply to them. `server.request_timeout` bounds each query
or mutation sent over the socket and the resolution of each subscription
event, but not the life of the connection. There is no cap on the number of
connections, so put one in the load balancer if you need it. A browser
connection's `Origin` must be the server's own host or match
`server.cors_origins` (`*` accepts any origin). `server.max_body_bytes`
limits the size of each incoming message. With `server.subscriptions_keepalive`
set, the server sends a WebSocket ping every interval and drops a peer that
does not answer within the interval. Legacy clients also receive a `ka`
message every interval. On shutdown every socket is closed with `1001`.

## Authentication

The identity is fixed for the life of the connection by `connection_init`,
following [`rls.auth.mode`](rls.md):

- **`jwt`**: browsers cannot set headers on a WebSocket, so the token comes
  from the `connection_init` payload, as PostGraphile reads it:
  `{"authorization": "Bearer <jwt>"}`. The key is matched without regard
  to case, and `{"headers": {"Authorization": "Bearer <jwt>"}}` also
  works. Without either, the upgrade request's `Authorization` header is
  used, and without that the connection is anonymous. An invalid or expired
  token, or a role outside `rls.allowed_roles`, closes the socket with
  `4403`. When the token's `exp` passes, the socket is closed with `4403`,
  so the client reconnects with a fresh token.
- **`headers`**: claims come only from the upgrade request's claim headers,
  as set by the trusted gateway. The `connection_init` payload cannot add
  claim headers.
- **`none`**: every connection is anonymous.

## Topics

A topic must consist of letters, digits and `_ . : / -`, and the full channel
(prefix plus topic) must fit PostgreSQL's 63-byte identifier limit. A longer
channel name would be silently truncated by `LISTEN` and rejected by
`pg_notify`. `server.subscriptions_topics` restricts topics to an allowlist:

```yaml
server:
  subscriptions: true
  subscriptions_topic_prefix: "app:"
  subscriptions_topics: ["announcements", "chat:*"]   # chat:<anything>
```

Channel names are always sent as quoted identifiers (`LISTEN "app:chat:1"`)
and never as interpolated text. A subscription to a disallowed topic gets an
`error` message. Topics are not secrets: any client allowed to connect can
listen to any allowed topic, and RLS protects what an event reveals through
`relatedNode` and `query`. `relatedNodeId` is the notifier's `__node__`
encoded as is, as in PostGraphile: it reaches every subscriber even when
RLS hides the row from them, so do not put secrets in a node key. To keep
a topic name from leaking information, put only identifiers in it and leave
the data in tables that RLS guards.
PostGraphile's `pgSubscriptionAuthorizationFunction` is not supported.

## How events are resolved

- **One LISTEN connection.** It is opened outside the pool, so it never
  holds a pool slot, on the first subscription. It `LISTEN`s on a channel when
  the channel gets its first subscriber and `UNLISTEN`s when the last one
  leaves. A subscription starts receiving events only after its channel is
  LISTENed: an `initialEvent` therefore means "listening". If the connection
  fails, it reconnects with backoff (100 ms doubling to 5 s) and LISTENs on
  every active channel again. Notifications sent while it was down are lost
  without notice to the subscribers, as they are with PostGraphile. Clients
  that cannot miss an event should resubscribe with `initialEvent` and
  resynchronize through `query` after reconnecting. An idle connection is
  pinged every 30 seconds, and a LISTEN, UNLISTEN or ping that takes longer
  than 10 seconds replaces the connection, so a silently dropped connection
  is noticed.
- **Fan-out.** Each notification is copied into a bounded queue per
  subscription (`server.subscriptions_buffer`). A subscriber whose queue is
  full gets an `error` message and its subscription ends. Other subscribers
  are not slowed down by it. Writes to a client time out after 10 seconds,
  and a timed-out write drops the connection.
- **Per-event cost.** Each event is resolved separately for each
  subscriber, under that subscriber's role and claims: N subscribers on a
  topic mean N round trips per notification. A round trip has one statement
  per selected `relatedNode` and per root field under `query`, compiled with
  the single-statement compiler and sent as one batch, in a transaction when
  RLS applies. `relatedNodeId` and `__typename` are computed without the
  database, so a subscription that selects only those costs nothing per
  event. pdbq does not coalesce identical subscriptions: subscribers with
  the same document and identity still resolve separately. Keep high-fan-out
  topics cheap by selecting `relatedNodeId` and fetching on the client, or
  by narrowing topics.
- **Limits.** The subscription document goes through the usual validation,
  `server.max_depth` / `server.max_cost`, `server.disable_introspection`
  (it also covers `__schema` inside `query`), persisted-query rules
  (`server.persisted_only` applies to WebSocket operations too) and the
  `BeforeOperation` request hooks, once, when it is subscribed. Plugins'
  `AfterOperation` hooks do not run for subscription events.
  `server.subscriptions_max_per_connection` caps the operations running at
  once on one socket.
- **Watch mode.** A subscription keeps resolving against the schema it was
  validated with. Clients pick up a new schema when they resubscribe.

## Embedding

`App.Serve` and `App.Handler` set everything up when `server.subscriptions`
is on. `App.Close` closes the WebSockets and the LISTEN connection, because
`net/http`'s graceful shutdown does not track hijacked WebSocket
connections. `App.Serve` also closes them when its context ends.
