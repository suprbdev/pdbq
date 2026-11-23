# Smart comments

The built-in `smart-comments` plugin (enabled by default) reads
PostGraphile-style tags out of ordinary PostgreSQL `COMMENT`s and customizes
the generated GraphQL schema — rename, hide, deprecate, and extend objects
with plain DDL, no pdbq configuration, no redeploy (watch mode picks comment
changes up like any other DDL).

## Syntax

Lines at the **start** of a comment beginning with `@` are tags; everything
after the first non-tag line is the description (tag lines never leak into
GraphQL descriptions):

```sql
COMMENT ON TABLE app.users IS E'@name people\n@omit delete\nA person with an account.';
```

Tags may repeat where noted. Disable everything with
`plugins.disabled: [smart-comments]`.

## Tag reference

### Tables and views

| Tag | Effect |
|---|---|
| `@omit` | Hide the table entirely (type, queries, mutations, relations). Functions returning or taking its rows are not exposed either (a warning is logged), and columns of other tables typed as its row type are omitted. |
| `@omit read` | No root query fields (`allUsers`, `userById`, …, and root query functions returning its rows); the type stays reachable through relations, computed columns and mutations — useful for detail/join tables. |
| `@omit all` | No connection query (`allUsers`); by-key lookups stay. |
| `@omit many` | No one-to-many surface over this table's rows on other types: relation fields, nested-mutation inputs, reverse relation filters and orderings, relational aggregates. |
| `@omit create` / `update` / `delete` | Suppress the corresponding mutation (same effect as missing the SQL privilege). |
| `@omit filter` / `@omit order` | Drop the table's `<Type>Filter` and `<Type>Condition` inputs / `<Types>OrderBy` enum, the `<Types>DistinctOn` enum, and every argument referencing them. The bulk `update<Types>` / `delete<Types>`, which require a filter, go with `@omit filter`. |
| `@name <new_name>` | Rename the table for naming purposes. Give a name shaped like a table name (e.g. snake-case plural); every derived name follows: `@name customers` on `users` yields `Customer`, `allCustomers`, `createCustomer`, `CustomerFilter`, … Composes with `simple-names`. |
| `@primaryKey col[, col]` | Declare a logical primary key on a view (or PK-less table): enables `nodeId`, the by-key lookup, and keyset pagination. Mutations remain gated by real privileges, so views stay read-only. |
| `@unique col[, col]` | Declare a logical unique constraint: generates the single-row lookup field (repeatable). |
| `@foreignKey (col, …) references [schema.]table (col, …)` | Declare a logical foreign key (typically on views): generates relation fields in both directions and relation filters (repeatable). Append `\|@fieldName x\|@foreignFieldName y` to name the generated fields. |
| `@enum` | Treat the table as a GraphQL enum: its **rows** become the enum values. See [Enum tables](#enum-tables). |
| `@aggregates` / `@aggregates off` | Opt the table in to (out of) the `aggregates` plugin's surface; `@omit aggregates` and `@behavior ±aggregates` work too. See [Aggregates](aggregates.md#smart-comments). |

`@omit` actions combine: `@omit create, delete` or repeated `@omit` lines.

### Columns

| Tag | Effect |
|---|---|
| `@omit` | Hide the column everywhere: every action below. The column may still back foreign keys (relations join through it). |
| `@omit read` | Hide its value: the output field, PostGIS derived accessors, full-text rank fields, aggregates and group-by keys, distinctOn, distance / rank / similarity orderings, and every key including the column (see [What `@omit read` hides](#what-omit-read-hides)). It also implies `@omit filter`, `@omit order` and `@omit aggregates`: the column is left out of filters, conditions, orderBy and distinctOn too. |
| `@omit create` / `update` | Leave it out of the create input / update patch, nested-mutation inputs included; an upsert never overwrites a column left out of the patch. |
| `@omit filter` | No filter or condition field, distinctOn value, `having` or relation-aggregate filter. |
| `@omit order` | No orderBy values (relational and aggregate ones included), distinctOn or group-by value. |
| `@name <new_name>` | Rename the field (and its filter field, orderBy and distinctOn values, input fields, lookup arguments). |
| `@deprecated [reason]` | Emit `@deprecated(reason: …)` on the output field. |
| `@notNull` / `@nullable` | Override introspected nullability — views lose `NOT NULL`, `@notNull` restores the non-null GraphQL type. |
| `@filterable` (alias `@sortable`) | Admit the column to filtering **and** ordering despite `filters.indexed_only` — the per-column form of `filters.allow_columns`. Combine with `@omit filter` or `@omit order` to expose only one side. |
| `@omit aggregates` | Leave the column out of every aggregate (`@behavior -groupBy` only out of `groupBy`). See [Aggregates](aggregates.md#smart-comments). |

Composite type attributes (`COMMENT ON COLUMN address.street IS '@omit'`)
take `@omit read` (no field on the composite's object type) and `@omit
create` / `update` (no field on its input type, which serves both; writing
the composite leaves the attribute NULL).

### What `@omit read` hides

A primary key or unique constraint that includes a column hidden from read
is dropped from the API, with a warning: no by-key lookup, update, delete or
upsert, no nested-mutation operation addressing rows by it, and, for a
primary key, no `nodeId` (the type leaves the `Node` interface) and no
payload edge; connection cursors fall back to offsets rather than encode
the key. Looking a row up by the hidden value would tell a client whether
its guess exists. 
A foreign key referencing such a column keeps its relation fields, but
nested `connect…` / `create` / `disconnect…` operations setting it are not
generated: they would copy the hidden value into the referencing row's
readable column.

`@omit read` implies `@omit filter`, `@omit order` and `@omit aggregates` on
a column. **This differs from PostGraphile**, where `@omit read` only hides
the output field and leaves the column filterable and sortable. A column
that can still be filtered or ordered leaks its value to a client that
bisects with range operators (`salary > 50000`), so pdbq removes it from
filters, conditions, orderBy, distinctOn, aggregates (groupBy, having, sum,
min, max, distinctCount), keyset cursors and unique-key lookups as well. There
is no tag combination that keeps a column filterable but unreadable.

A type whose every field is hidden keeps a single `_empty: Boolean`
placeholder (always null), since GraphQL object types need a field.

### Constraints (`COMMENT ON CONSTRAINT … ON <table>`)

| Tag | Effect |
|---|---|
| `@omit` on a foreign key | Remove both relation fields and the relation filters. |
| `@omit many` on a foreign key | Remove only the one-to-many side (relation field, nested-mutation input, reverse relation filter and orderings, relational aggregates). |
| `@omit filter` on a foreign key | Keep the relation fields, drop the relation filter (both directions). |
| `@fieldName <name>` / `@foreignFieldName <name>` | Exact GraphQL names for the many-to-one / one-to-many relation fields. Applied through the shared naming pipeline, so `advanced-filters` filter fields and `nested-mutations` input fields align automatically. |
| `@filterable` on a foreign key | Opt the relation into filtering when `advanced-filters` runs in opt-in mode (below). |
| `@costMultiplier <n>` on a foreign key | Expected rows per parent on the one-to-many side, used by the cost estimator instead of the requested page size. See [Cost estimation for recursive relations](#cost-estimation-for-recursive-relations). |
| `@omit` on a unique constraint | Remove the generated by-unique lookup (`userByEmail`) and mutations; filtering is unaffected (the backing index remains). |

Tags appended to an `@foreignKey` smart comment (`|@omit many`) apply like
those on a real constraint.

### Functions

| Tag | Effect |
|---|---|
| `@omit` | Hide the function (root field or computed column). |
| `@name <new_name>` | Rename the generated field. |
| `@resultFieldName <name>` on a volatile function | Name the result field of its mutation payload (default: PostGraphile's, e.g. `post`, `integer`, `posts`). |
| `@deprecated [reason]` | Deprecate the generated field(s). |
| `@omit filter` / `@omit order` on a computed column | Keep the computed field, drop its `advanced-filters` filter field / orderBy values. |
| `@filterable` (alias `@sortable`) | Opt the computed column in when `advanced-filters` runs in opt-in mode. |
| `@anyelement <type>` | Resolve a polymorphic function's `anyelement` family to `<type>` (`integer`, `text[]`, `public.mood`, …): `anyelement`/`anynonarray`/`anyenum` become `<type>`, `anyarray` `<type>[]`, `anyrange`/`anymultirange` the range (multirange) over `<type>`. Untagged polymorphic functions are skipped. See [Function arguments](mutations.md#function-arguments). |
| `@anycompatible <type>` | The same for the `anycompatible*` family. |

`@omit read` on a column also removes it from the `<Type>Input` that
function arguments typed as the table's row take.

A function returning or taking rows of a table tagged `@omit`, or of a type
from a schema outside `schema.schemas` that pdbq cannot resolve (typically
a table's row type), is not exposed and a warning is logged: it would
otherwise surface as a `String` returning whole rows as JSON.

### Enums

| Tag | Effect |
|---|---|
| `@name <new_name>` | Rename the GraphQL enum type. |

### Domains

| Tag | Effect |
|---|---|
| `@name <new_name>` | Name the domain's custom scalar: `@name Percent` on `small_positive` yields `scalar Percent` (see [Domains](domains.md)). |

### Contracts

`@contract a, b` and `@behavior ±contract:<name>` on tables, columns,
functions and foreign keys select which [schema contracts](contracts.md)
an object belongs to. They have no effect unless `contracts` is configured.

## Enum tables

`@enum` on a table turns its rows into a GraphQL enum — the PostGraphile
pattern for lookup tables that are really a fixed vocabulary with referential
integrity:

```sql
CREATE TABLE event_type (
    code        text PRIMARY KEY,
    description text
);
COMMENT ON TABLE event_type IS E'@enum\nKind of event.';

INSERT INTO event_type (code, description) VALUES
    ('conference', 'A large formal gathering'),
    ('meetup',     'Casual get-together');

CREATE TABLE events (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       text NOT NULL,
    type_code  text NOT NULL REFERENCES event_type (code)
);
```

generates

```graphql
enum EventType {
  """A large formal gathering"""
  CONFERENCE
  """Casual get-together"""
  MEETUP
}

type Event implements Node {
  id: Int!
  name: String!
  typeCode: EventType!
}
```

- The **value column** is the table's single-column primary key, or the column
  of a single-column unique constraint commented `@enum`. It must be a
  text-family column — the row values become the enum value names
  (`meetup` → `MEETUP`), and clients speak names in both directions while the
  database stores the raw values.
- **Descriptions** come from a column commented `@enumDescription`, falling
  back to a column named `description`.
- The enum table itself **disappears from the API** (no type, queries, or
  mutations), and every single-column foreign key referencing the value column
  types its local column as the enum; those FKs generate no relation fields.
- `@name` on the table renames the enum (`@name event_kind` → `EventKind`).
- Tables that do not qualify (multi-column or non-text key, or no rows — an
  empty GraphQL enum is invalid) are left as ordinary tables.

Rows are read **at introspection time** and values are ordered by the value
column; adding a row is DDL-adjacent — watch mode (or a restart) picks it up
like any other schema change. Like `@costMultiplier`, the tag is applied
during introspection and therefore works even with the `smart-comments`
plugin disabled.

## Tuning advanced-filters per object

The relation/computed filter tags above are parsed by `advanced-filters`
directly from the catalog, so they work even if `smart-comments` is disabled.
Beyond the default opt-out (`@omit filter`/`@omit order`), the whole surface
can be flipped to explicit opt-in:

```yaml
plugins:
  settings:
    advanced-filters:
      relations_opt_in: true   # only FKs tagged @filterable get relation filters
      computed_opt_in: true    # only functions tagged @filterable get filter/order
```

## Cost estimation for recursive relations

The executor rejects an operation whose estimated cost exceeds
`server.max_cost` (default 10000). The estimate multiplies each subtree by the
page size requested at that level, so a nested query costs roughly
`page^depth`. That is right for an unrelated fan-out, but badly pessimistic
for self-referential trees — a Reddit-style comment thread — where the
requested page is an upper bound that almost no row reaches.

A four-level thread at `first: 20` per level estimates ~345,000, which no
reasonable `max_cost` can accommodate, even though the query returns a
handful of rows in practice.

`@costMultiplier <n>` on the recursive foreign key declares the *expected*
rows per parent, and the estimator uses it in place of the requested page
size:

```sql
CREATE TABLE comments (
    id         integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    post_id    integer NOT NULL REFERENCES posts (id),
    parent_id  integer REFERENCES comments (id),
    body       text NOT NULL
);

COMMENT ON CONSTRAINT comments_parent_id_fkey ON comments IS '@costMultiplier 3';
```

The same query now estimates 1,861 — comfortably inside the default limit.

Details worth knowing:

- **It only ever lowers an estimate.** A query requesting fewer rows than the
  declared multiplier is charged the smaller requested page, so the tag can
  never make a cheap query look expensive.
- **It applies to the one-to-many side**, the direction that multiplies.
- **Values below 1 and unparseable values are ignored**, since a zero or
  negative multiplier would let a subtree escape the cost limit entirely.
- **It is parsed straight from the catalog**, so it works with the
  `smart-comments` plugin disabled.
- **It changes the estimate, not the query.** Rows actually returned are still
  bounded by `first`/`last` and `server.max_page_size`. Setting the multiplier
  far below real fan-out weakens the cost limit as a guard — pick a value near
  the true average, not the smallest one that makes a query pass.

## Examples

```sql
-- A view made first-class: identified, relatable, read-only.
COMMENT ON VIEW app.order_totals IS E'@primaryKey order_id\n@foreignKey (order_id) references app.orders (id)';

-- Hide credentials, rename a legacy column, phase out another.
COMMENT ON COLUMN app.users.hashed_password IS '@omit';
COMMENT ON COLUMN app.users.fullname IS '@name display_name';
COMMENT ON COLUMN app.users.balance IS '@deprecated use credits';

-- Sharpen relation naming.
COMMENT ON CONSTRAINT posts_author_id_fkey ON app.posts IS E'@fieldName author\n@foreignFieldName posts';

-- A search function that should not become a computed filter.
COMMENT ON FUNCTION app.users_score(app.users) IS '@omit filter,order';
```
