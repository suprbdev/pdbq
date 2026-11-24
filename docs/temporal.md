# Temporal keys (PostgreSQL 18)

PostgreSQL 18 adds temporal primary/unique keys (`WITHOUT OVERLAPS`),
temporal foreign keys (`PERIOD`) and `NOT ENFORCED` foreign keys. pdbq reads
all three from the catalog (they only exist on PG18+; older servers never
report them) and adjusts the generated API:

```sql
CREATE EXTENSION btree_gist;  -- GiST equality for the scalar key columns

CREATE TABLE room_bookings (
  room_id  integer NOT NULL,
  valid_at daterange NOT NULL,
  guest    text NOT NULL,
  PRIMARY KEY (room_id, valid_at WITHOUT OVERLAPS)
);

CREATE TABLE booking_notes (
  id        integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  room_id   integer NOT NULL,
  valid_at  daterange NOT NULL,
  note      text NOT NULL,
  author_id integer NOT NULL,
  FOREIGN KEY (room_id, PERIOD valid_at) REFERENCES room_bookings (room_id, PERIOD valid_at),
  FOREIGN KEY (author_id) REFERENCES users (id) NOT ENFORCED
);
```

## Temporal keys (`WITHOUT OVERLAPS`)

A temporal key holds one row per non-overlapping period of the same entity
(`room_id = 1` can have many bookings). PostgreSQL enforces it as an
exclusion constraint, so:

- **No exact-tuple lookup.** `roomBookingByRoomIdAndValidAt` is not
  generated. Instead, when the period column is a built-in range or
  multirange, a point-in-time lookup named after the equality columns takes
  an `at:` argument of the range's element type and returns the row whose
  period contains it (`valid_at @> $n`):

  ```graphql
  { roomBookingByRoomId(roomId: 1, at: "2026-03-06") { guest validAt } }
  ```

  (`roomBooking(roomId:, at:)` with `simple_names`.) User-defined range types
  get no point-in-time lookup.
- **No upsert.** `ON CONFLICT (cols)` cannot infer an exclusion constraint,
  so temporal keys are not `upsert<Type>By…` targets. Plain primary keys and
  unique constraints on the same table still are.
- **Update/delete by the full key stay.** `updateRoomBookingByRoomIdAndValidAt`
  and `deleteRoomBookingByRoomIdAndValidAt` match on every key column
  (including the exact range), which addresses one version of the entity.
- **`nodeId` keeps the full key.** Empty periods are rejected, so the whole
  tuple is unique: `nodeId` (and keyset cursors) encode
  `["RoomBooking", room_id, valid_at]` and `node(nodeId:)` resolves it.
- An overlapping insert fails with SQLSTATE `23P01` (`conflicting key value
  violates exclusion constraint …`), passed through like a unique violation.

## Temporal foreign keys (`PERIOD`)

PostgreSQL requires a child's period to be covered by the union of the
periods of the parent rows matching the other columns, so a child can
reference several parent rows. Every join over such a key (selections,
filters, ordering, nested mutations) compares the equality columns with `=`
and the period pair by overlap (`&&`):

- **Forward (child → parent) is a connection**, like every to-many relation
  in pdbq (PostGraphile's convention), with the full connection arguments.
  It is named like a reverse relation, after the referenced table and the
  foreign key columns:

  ```graphql
  { allBookingNotes { nodes {
      note
      roomBookingsByRoomIdAndValidAt { totalCount nodes { guest validAt } }
  } } }
  ```

  A note whose period spans two bookings returns both. `@fieldName` on the
  constraint renames the field.
- **Backward (parent → children)** is the usual connection
  (`bookingNotesByRoomIdAndValidAt`, `bookingNotes` with `simple_names`),
  holding every child whose period overlaps the parent's.
- **advanced_filters**: the forward relation takes the `{some|none|every}`
  wrapper (`roomBookingsByRoomIdAndValidAt: {some: {guest: {equalTo:
  "Grace"}}}`) and a `<rel>Exists` Boolean, and its relational `orderBy`
  values aggregate (`MIN`/`MAX`) like reverse relations.
- **nested_mutations**: the forward relation gets an extra
  `connectBy<EqualityColumns>` taking the equality columns only
  (`{connectByRoomId: {roomId: 1}}`), which links the child to that entity;
  `connectByRoomIdAndValidAt` (the full key, exact period) and
  `connectByNodeId` link to one version, of which the child also copies only
  the equality columns. The child supplies its own period, which PostgreSQL
  checks for coverage at the end of the statement (`23503` when uncovered).
  Nested child `create` copies only the equality columns from the new
  parent, so each child keeps its own period; backward
  `updateBy…`/`deleteBy…`/`disconnectBy…` reach the children whose period
  overlaps the parent's.

## `NOT ENFORCED` foreign keys

PostgreSQL never checks them, so a child may reference a missing row even
when its columns are `NOT NULL`. The forward relation field is nullable
(`userByAuthorId: User`) and resolves to `null` for a dangling reference;
`userByAuthorIdExists: false` finds those rows.
