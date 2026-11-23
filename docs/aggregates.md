# Aggregates

The built-in `aggregates` plugin adds aggregate fields to connections, modelled
on [`@graphile/pg-aggregates`](https://github.com/graphile/pg-aggregates):
connection-wide `aggregates`, `groupedAggregates` with `groupBy` and `having`,
ordering by aggregates over related rows, and filtering by them. pdbq extends
the set with PostGIS aggregates on spatial columns.

Every aggregate compiles into the root field's **single SQL statement**, as a
scalar subquery over the same row set the connection reads. Row-level
security therefore applies to aggregates exactly as it does to `nodes`.

## Turning it on

Aggregates are **off by default**, as in PostGraphile, because they grow the
schema considerably and every aggregate reads the connection's whole filtered
row set. With the plugin off, the schema is byte-identical to a pdbq build
without it.

Turn it on for every table:

```yaml
plugins:
  settings:
    aggregates:
      enabled: true
```

(or `PDBQ_PLUGINS_SETTINGS_AGGREGATES_ENABLED=true`), or leave it off and opt
individual tables in with a smart comment:

```sql
COMMENT ON TABLE posts IS '@aggregates';
```

`plugins.disabled: [aggregates]` removes the surface completely, tags
included.

## Connection aggregates

Every `<Type>Connection` gains an `aggregates: <Type>Aggregates` field. The
aggregates respect `filter`, `condition` and the parent relation of a nested
connection, and ignore `first`/`last`/`offset`/`before`/`after`/`orderBy`,
like `totalCount`:

```graphql
{
  allPosts(filter: {authorId: {equalTo: 1}}, first: 5) {
    nodes { title }
    aggregates {
      sum { id }
      distinctCount { title }
      average { id }
    }
  }
  allUsers {
    nodes {
      email
      postsByAuthorId { aggregates { max { id } } }
    }
  }
}
```

`<Type>Aggregates` has one field per aggregate. Each holds one field per
suitable column, named like the column's own field:

| Field | SQL | Applies to | Result type |
|---|---|---|---|
| `sum` | `coalesce(sum(x), 0)` | numbers, `interval` | `int2`/`int4` → `BigInt!`, `int8`/`numeric`/`money` → `BigFloat!`, `float4`/`float8` → `Float!`, `interval` → `Interval!` |
| `distinctCount` | `count(DISTINCT x)` | types with a default btree operator class: built-in scalars and ranges, enums, `citext`/`ltree`/`hstore`, PostGIS types, arrays of these, domains over them (not `json`, `xml`, geometric types, composites or unknown extension types) | `BigInt` |
| `min`, `max` | `min(x)`, `max(x)` | numbers, `interval`, `date`, `timestamp`, `timestamptz` | the column's type |
| `average` | `avg(x)` | numbers except `money`, `interval` | integers/`numeric` → `BigFloat`, floats → `Float` |
| `stddevSample`, `stddevPopulation` | `stddev_samp(x)`, `stddev_pop(x)` | numbers except `money` | floats → `Float`, the rest → `BigFloat` |
| `varianceSample`, `variancePopulation` | `var_samp(x)`, `var_pop(x)` | numbers except `money` | floats → `Float`, the rest → `BigFloat` |

The result types follow PostgreSQL's, as in PostGraphile: the sum of an
`integer` column is a `bigint` (`BigInt`, serialized as a string). Only `sum`
is non-null: it reads `0` over no rows. The other aggregates read `null`.
`money` results (sum, min, max) are converted to `numeric`, because money's
text form depends on `lc_monetary`.

Compared with pg-aggregates, `min`/`max` also cover dates and timestamps. A
group type whose table has no suitable column is left out.

## Spatial aggregates

When a table has a `geometry` or `geography` column, its aggregates object
also gets the PostGIS aggregates. They are a pdbq extension, so they carry an
`st` prefix to keep clear of upstream names:

| Field | SQL | Result |
|---|---|---|
| `stExtent` | `ST_SetSRID(ST_Extent(x)::geometry, srid)` | the bounding box as a GeoJSON `Polygon` (a `Point` or `LineString` when degenerate) |
| `stCentroid` | `ST_Centroid(ST_Collect(x))` | the centroid of all values |
| `stUnion` | `ST_Union(x)` | the dissolved union (opt-in) |
| `stCollect` | `ST_Collect(x)` | a GeoJSON multi-geometry or `GeometryCollection` (opt-in) |

`ST_Extent` returns a `box2d` without an SRID. pdbq restores it from the
column's typmod (`geometry(Point, 4326)`), or with `min(ST_SRID(x))` on an
unconstrained column, so the GeoJSON names the right CRS.

`stUnion` and `stCollect` return a value that grows with the row set, so they
are off unless `plugins.settings.aggregates.spatial_union_collect` is true,
and each of their fields costs 10 × `scan_cost`. On an unconstrained column
holding several SRIDs, `ST_Union`, `ST_Collect` and `ST_Centroid` fail with
PostGIS's mixed-SRID error. Filter to one SRID first.

```graphql
{
  allPlaces(filter: {ownerId: {equalTo: 1}}) {
    aggregates {
      stExtent { location }
      stCentroid { location }
    }
  }
}
```

The results use the `GeoJSON` scalar even with `typed_geometry` on.
`geography` values are cast to `geometry` first, because PostGIS defines these
aggregates for `geometry` only, so `stCentroid` is planar. Spatial aggregates
take no part in `having`, ordering or filtering. Turn them all off with
`plugins.settings.aggregates.spatial: false`.

## Grouped aggregates

`groupedAggregates(groupBy: [...], having: ...)` returns one
`<Type>Aggregates` object per group. Its `keys` field holds the group's key
values as strings (`null` outside grouped aggregates), in `groupBy` order:

```graphql
{
  allPosts {
    byAuthor: groupedAggregates(groupBy: [AUTHOR_ID]) {
      keys            # ["1"], ["2"], ...
      distinctCount { id }
    }
  }
  allUsers {
    byDay: groupedAggregates(groupBy: [CREATED_AT_TRUNCATED_TO_DAY]) {
      keys            # ["2026-11-23T00:00:00+00:00"]
      sum { balance }
    }
  }
}
```

- The `<Type>GroupBy` enum lists the table's orderable columns that have a
  default btree operator class (see `distinctCount`). It is the same set as
  the `orderBy` enum, so `filters.indexed_only` and `@omit order` apply. A
  column that alone is a primary or unique key is left out, because each of
  its groups would hold one row.
- `timestamp`/`timestamptz` columns also get `<COLUMN>_TRUNCATED_TO_HOUR` and
  `<COLUMN>_TRUNCATED_TO_DAY` (`date_trunc` in the session time zone).
- Keys render like the column's own output, then as a string: ISO timestamps,
  enum values by their GraphQL name, numbers as text. `json`/`jsonb` values
  and arrays render as JSON text (`{"theme": "dark"}`), so a JSON `null` or
  string `"1"` stays distinct from SQL `NULL` (a `null` key) and the number
  `1`. Values that compare equal without being identical (`citext` case
  variants, `1.0` and `1.00`, nondeterministic collations) fall into one
  group. Its key is the smallest member's text under the `C` collation, so it
  is the same on every run.
- Groups come back ordered by their keys. At most `server.max_page_size` groups
  are returned.

### Having

`having: <Type>HavingInput` filters the groups. It takes one entry per
aggregate, plus `AND`/`OR` lists of nested having inputs. Entries at the same
level are ANDed. An empty object matches every group, so `OR: [{}, ...]` is
always true. The entries cover the table's filterable columns, the columns
of its `<Type>Filter` input, so `filters.indexed_only` and `@omit filter`
apply:

```graphql
{
  allPosts {
    groupedAggregates(
      groupBy: [AUTHOR_ID]
      having: {sum: {id: {greaterThan: "3"}}, OR: [{max: {id: {equalTo: 1}}}, {max: {id: {equalTo: 4}}}]}
    ) { keys }
  }
}
```

The comparison inputs are `HavingIntFilter`, `HavingBigintFilter`,
`HavingFloatFilter`, `HavingBigfloatFilter` and `HavingDatetimeFilter`, each
with `equalTo notEqualTo greaterThan greaterThanOrEqualTo lessThan
lessThanOrEqualTo`. pdbq picks the input from the aggregate's **result** type,
so `distinctCount` always compares a `BigInt`. Upstream picks it from the
column's type.

## Ordering by related aggregates

A table's `orderBy` enum gains values for every reverse relation from a table
with aggregates enabled. Values are named after the relation field, as in
pg-aggregates:

| Value | Sorts by |
|---|---|
| `<REL>_COUNT_ASC` / `_DESC` | the number of related rows |
| `<REL>_<AGGREGATE>_<COLUMN>_ASC` / `_DESC` | an aggregate of a related column, e.g. `POSTS_BY_AUTHOR_ID_SUM_ID_DESC`, `POSTS_BY_AUTHOR_ID_DISTINCT_COUNT_TITLE_ASC` |

With `simple-names` the relation is `posts`, so the values read
`POSTS_COUNT_DESC` and `POSTS_SUM_ID_DESC`. Each value compiles to a
correlated scalar subquery, like the advanced-filters relational ordering. It
works with keyset cursors, and descending values sort `NULL` (no related rows)
last. The related table's aggregated columns are its orderable columns,
those behind its own `orderBy` values, so `filters.indexed_only` and column
`@omit order` apply as in advanced-filters. On the foreign key, `@omit order`
and `@omit many` skip the relation. In advanced-filters' `relations_opt_in`
mode, only keys tagged `@filterable`/`@sortable` get values.

Keyset cursors over a floating-point aggregate (`sum`, `average`,
`stddev*`/`variance*` of `float4`/`float8` columns) can skip or repeat a row
at a page boundary. The cursor predicate compares the row's aggregate against
the anchor row's, and each comes from its own subquery. Floating-point
summation is order-dependent, so the two can differ in the last bit when the
planner reads the related rows in a different order. Integer and `numeric`
aggregates, `count`, `min` and `max` are exact. Use offset pagination, or add
an exact tiebreaker such as `ID_ASC` (which only helps for exactly equal
values), when this matters.

## Filtering by related aggregates

With `advanced-filters` relation filters on (the default), the
`<Type>ToManyFilter` wrapper (`some`/`none`/`every`) gains an `aggregates`
field, the pg-aggregates shape:

```graphql
{
  allUsers(filter: {
    postsByAuthorId: {
      aggregates: {
        filter: {title: {startsWith: "N"}}   # narrows the aggregated rows
        sum: {id: {greaterThan: "3"}}
        distinctCount: {title: {equalTo: "1"}}
      }
    }
  }) { nodes { email } }
}
```

`<Type>AggregatesFilter` takes the related table's `filter` plus one
`<Type><Aggregate>AggregateFilter` per aggregate. The operators come from the
aggregate's result type (`BigIntFilterOps` for the sum of an integer). The
whole object compiles to one scalar subquery over the related rows. With no
related rows, `sum` reads 0 and every other aggregate reads `NULL`, so a
comparison on them is false. `not: {...}` around it is the exact complement.
The aggregated columns are the related table's filterable columns, those of
its `<Type>Filter`, so `filters.indexed_only` and column `@omit filter` apply.
The field is reachable only through relation filters advanced-filters
generated, so `@omit filter` on the foreign key and `relations_opt_in` govern
it too. Spatial results have no filter.

## Smart comments

| Tag | On | Effect |
|---|---|---|
| `@aggregates` / `@aggregates on` | table | opt the table in while `enabled` is false |
| `@aggregates off`, `@omit aggregates` | table | opt the table out |
| `@behavior -aggregates` / `+aggregates` | table | same, in PostGraphile V5 behavior form |
| `@behavior -groupedAggregates` | table | no `groupedAggregates` (or `having`) on the table's connections |
| `@behavior -aggregates:orderBy` | table | the table drives no `<REL>_..._ASC` values on its parents |
| `@behavior -aggregates:filterBy` | table | no `aggregates` field in the table's `<Type>ToManyFilter` |
| `@omit aggregates`, `@aggregates off`, `@behavior -aggregate` | column | the column is aggregated nowhere |
| `@behavior -groupBy` | column | the column is not a `groupBy` value |
| `@omit order` | foreign key | no relational aggregate ordering over it (as advanced-filters) |

Columns hidden with `@omit read` are never aggregated. Column renames
(`@name`) carry over to the aggregate fields, relation renames
(`@foreignFieldName`) to the ordering values.

## Settings

All under `plugins.settings.aggregates`:

| Key | Default | Effect |
|---|---|---|
| `enabled` | `false` | aggregates for every table; otherwise only for `@aggregates` tables |
| `grouped` | `true` | `groupedAggregates`, `<Type>GroupBy`, `<Type>HavingInput` |
| `order` | `true` | `<REL>_COUNT_*` / `<REL>_<AGGREGATE>_<COLUMN>_*` orderBy values |
| `filters` | `true` | the `aggregates` field of to-many relation filters |
| `spatial` | `true` | `stExtent`, `stCentroid` (and the next row) |
| `spatial_union_collect` | `false` | `stUnion`, `stCollect`, whose results grow with the row set |
| `scan_cost` | `10` | cost-estimate surcharge per aggregate field, aggregate orderBy value and relation-aggregate comparison |

## Cost

The executor's cost estimate adds `scan_cost` for each of:

- an `aggregates` or `groupedAggregates` field, on top of its selection,
  because both read the whole filtered row set rather than a page;
- each aggregate `orderBy` value, tripled when an `after`/`before` cursor
  repeats it in the keyset predicate;
- each aggregate comparison in a relation-aggregate filter.

The last two run once per candidate row. `groupedAggregates` returns up to
`server.max_page_size` groups, so its selection is multiplied by that cap.
Each `stUnion`/`stCollect` field costs 10 × `scan_cost`. All of these are
multiplied by the enclosing connection's page size, as for any other field:
a nested `postsByAuthorId { aggregates { ... } }` runs once per parent row.

## Limits

- Aggregates cannot be combined with `distinctOn` on the same connection (an
  error).
- Aggregates are added to connections only, never to plain list fields.
- Computed columns are not aggregated.
