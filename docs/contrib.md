# Full-text search, trigrams, hstore, ltree, ranges and network types

pdbq maps PostgreSQL's non-scalar built-in types and the common contrib
extensions onto GraphQL types, reads and writes them in the same single
statement as everything else, and exposes their operator vocabularies as
filters and orderings. The vocabularies come from pdbcore's `contrib`
package, shared with pdbr.

Names follow PostGraphile (`KeyValueHash`, `InternetAddress`, `IntRange { start
{ value inclusive } end { ... } }`, `<column>Rank`, `<COLUMN>_RANK_DESC`,
connection-filter operator names) wherever PostGraphile has one.

## Type mapping

| Postgres type | GraphQL output | Input | Operator input |
|---|---|---|---|
| `tsvector` | `FullText` (text form) | `FullText` | `FullTextFilterOps` |
| `citext` | `String` | `String` | `StringFilterOps` (case-insensitive) |
| `hstore` | `KeyValueHash` (JSON object of string → string or null) | `KeyValueHash` | `KeyValueHashFilterOps` |
| `ltree` | `String` | `String` | `LtreeFilterOps` |
| `ltree[]` | `[String!]` | `[String!]` | `LtreeListFilterOps` |
| `int4range` / `int8range` / `numrange` / `daterange` / `tsrange`, `tstzrange` | `IntRange` / `BigIntRange` / `BigFloatRange` / `DateRange` / `DatetimeRange` | `<X>RangeInput` | `<X>RangeFilterOps` |
| the matching multiranges (PostgreSQL 14+) | `[<X>Range!]` | `[<X>RangeInput!]` | `<X>MultirangeFilterOps` |
| custom range types over a `pg_catalog` subtype (`CREATE TYPE ... AS RANGE`) | `<X>Range` of the subtype's scalar (`FloatRange` for `float8`) | `<X>RangeInput` | `<X>RangeFilterOps` |
| `inet` / `cidr` | `InternetAddress` / `CidrAddress` | same | `InternetAddressFilterOps` / `CidrAddressFilterOps` |
| `text`, `varchar`, `char(n)`, `citext` with `pg_trgm` installed | `String` | `String` | `StringTrgmFilterOps` (see [Trigram](#trigram-similarity-pg_trgm)) |

Ranges have PostGraphile's shape:

```graphql
type IntRange { start: IntRangeBound, end: IntRangeBound, isEmpty: Boolean! }
type IntRangeBound { value: Int!, inclusive: Boolean! }
input IntRangeInput { start: IntRangeBoundInput, end: IntRangeBoundInput, isEmpty: Boolean }
input IntRangeBoundInput { value: Int!, inclusive: Boolean! }
```

A bound is `null` when the range is unbounded on that side or the range is
empty; `isEmpty` tells the two apart, and `isEmpty: true` on input writes
the empty range. `isEmpty` is an addition beyond PostGraphile's range
types, which cannot express the empty range. On input, leave a bound out
for unbounded. Postgres normalizes discrete ranges, so `{start: {value: 3,
inclusive: false}, end: {value: 8, inclusive: true}}` reads back as `[4,9)`.
`BigInt` and `BigFloat` bounds are strings, like the scalars elsewhere.

The mapping applies to table columns, composite attributes, computed columns
and function arguments and results. Arrays of these types (other than
`ltree[]`) map to lists of their text form (`[String!]`, `["[1,3)", "[5,9)"]`
for `int4range[]`), as columns and as function arguments, VARIADIC ones
included. `citext`, `hstore` and `ltree` map only
when the catalog records the extension; a database that uses none of these
types gets exactly the schema it got before. A custom range type (`CREATE
TYPE floatrange AS RANGE (subtype = float8)`, in any schema) maps like the
built-in range over the same element scalar, so `floatrange` is a
`FloatRange`, a range over `int4` shares `IntRange`, and its multirange is a
list of them; the constructor in compiled SQL is schema-qualified
(`"public"."floatrange"($1::float8, $2::float8, $3::text)`). A text-like
subtype (`text`, `inet`, `interval`, ...) gives a `StringRange`. A range
whose subtype is not a `pg_catalog` type (an enum, a domain, an extension
type) keeps the `String` fallback, as do arrays of these types other than
`ltree[]`. An empty custom range is accepted only for the common numeric,
date/time, text and network subtypes. If a table's type name already
takes one of the names above (a table called `key_value_hash`), the column
keeps the `String` fallback and the builder logs a warning.

### Extension schemas

Extensions are often installed into their own schema (`CREATE EXTENSION
hstore SCHEMA extensions`) that is not on the search_path. pdbq
schema-qualifies every extension type, function and operator it emits
(`(c) OPERATOR("extensions".@>) "extensions".hstore($1::text[])`), so nothing
depends on the search_path. This matters most for `citext`: an unqualified
`email = $1` silently resolves to the case-sensitive `text` operator when
citext's schema is off the path, so pdbq compiles `citext` comparisons as
`email OPERATOR("ext".=) $1::"ext".citext` and `like`/`likeInsensitive`/
`startsWith`/`endsWith` as `OPERATOR("ext".~~)`/`~~*`. Conditions, unique
lookups (`accountByEmail`), `updateAccountByEmail` keys, `node(nodeId:)`
lookups, keyset cursors, foreign key joins and nested `connectBy<Key>`/`updateBy<Key>`/`deleteBy<Key>`
keys do the same;
keyset cursors over an `hstore` ordering compare with hstore's btree
operators (`#<#`, `#>#`).

## Filter operators

Each operator input keeps the generic comparisons its type supports, with
operands of the column's own input type: `equalTo notEqualTo isNull` for
every type, plus `in notIn` for `ltree` and network types and the ordering
comparisons for `ltree`, ranges and network types. The `contrib` plugin adds
the vocabulary:

| Input | Operators |
|---|---|
| `FullTextFilterOps` | `matches` (`to_tsquery`: `& \| ! <->`, `:*` prefixes), `matchesPlain` (`plainto_tsquery`), `matchesPhrase` (`phraseto_tsquery`), `matchesWebsearch` (`websearch_to_tsquery`: `"quoted phrase"`, `or`, `-negation`; never raises), and `config` |
| `StringTrgmFilterOps` | every `StringFilterOps` operator, `similarTo` (`%`), `wordSimilarTo` (`<%`), `strictWordSimilarTo` (`<<%`) |
| `KeyValueHashFilterOps` | `containsKey` (`?`), `containsAllKeys` (`?&`), `containsAnyKeys` (`?\|`), `contains` (`@>`), `containedBy` (`<@`) |
| `LtreeFilterOps` | `ancestorOf` (`@>`), `descendantOf` (`<@`), `ancestorOfAny`, `descendantOfAny`, `matchesLquery` (`~`), `matchesAnyLquery` (`?`), `matchesLtxtquery` (`@`) |
| `LtreeListFilterOps` | the same, holding when some element matches (no `*Any` forms) |
| `<X>RangeFilterOps`, `<X>MultirangeFilterOps` | `overlaps` (`&&`), `contains` (`@>`), `containsElement` (`@>` element), `containedBy` (`<@`), `strictlyLeftOf` (`<<`), `strictlyRightOf` (`>>`), `notExtendsRightOf` (`&<`), `notExtendsLeftOf` (`&>`), `adjacentTo` (`-\|-`); range operands for multiranges too |
| `InternetAddressFilterOps`, `CidrAddressFilterOps` | `contains` (`>>`), `containsOrEqualTo` (`>>=`), `containedBy` (`<<`), `containedByOrEqualTo` (`<<=`), `containsOrContainedBy` (`&&`) |

```graphql
{
  allArticles(filter: {
    search: {matchesWebsearch: "postgres -mysql", config: "english"}
    title: {similarTo: "postgress"}
    tags: {containsKey: "featured"}
    category: {descendantOf: "tech.databases"}
    during: {overlaps: {start: {value: "2025-01-01T00:00:00Z", inclusive: true}}}
    clientIp: {containedBy: "10.0.0.0/8"}
  }) { nodes { title } }
}
```

`config` names the text search configuration (`regconfig`) every `matches*`
operator in the same object parses its query with; without it Postgres uses
`default_text_search_config`.
An empty `config` is rejected. A malformed `matches` query, `lquery` or
`ltxtquery`, or an unknown configuration, is reported as a client error.

The trigram predicates compare against the `pg_trgm.similarity_threshold`,
`word_similarity_threshold` and `strict_word_similarity_threshold` settings
(0.3, 0.6, 0.5 by default; set them per role or database). The word
operators need pg_trgm 1.2 and the strict ones 1.4; older installs simply
do not get them.

Every operand is a bind parameter; the compiled SQL never varies with
operand values (the `FuzzFilter` invariant covers these operators).

### `filters.indexed_only`

Columns follow the usual [indexed-only policy](filtering.md#the-indexed-only-policy).
On top of that, with `indexed_only` on (the default):

- operators no index can serve are left out of the shared inputs:
  `KeyValueHashFilterOps.containedBy`, `LtreeFilterOps.ancestorOfAny` /
  `descendantOfAny`, `LtreeListFilterOps.ancestorOf`;
- a text column takes `StringTrgmFilterOps` only when an index led by it is a
  GIN or GiST index (build it with `gin_trgm_ops` / `gist_trgm_ops`), so the
  common btree-indexed text column keeps `StringFilterOps`;
- trigram ordering needs a GiST index (only GiST serves the KNN distance
  operators).

Columns listed in `filters.allow_columns` get everything. Operator classes are
not introspected: a GIN index on a text column is taken to be a trigram
index.

Full-text search over plain text columns (`to_tsvector(col) @@ ...`) is not
exposed: it cannot use an index unless an expression index matches the exact
configuration. Add a generated column instead:

```sql
ALTER TABLE articles ADD COLUMN search tsvector
  GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || body)) STORED;
CREATE INDEX ON articles USING gin (search);
```

## Full-text rank

Every filterable `tsvector` column gets a `<field>Rank: Float` sibling and
`<COLUMN>_RANK_ASC` / `_RANK_DESC` orderBy values (as in
`postgraphile-plugin-fulltext-filter`). Both compute `ts_rank(column, query)`
against the first `matches*` operator on that column in the filter of the
same list field, reusing its parameters. Only matches at the conjunctive
top level of the filter count (the root object and nested `and` lists):
matches under `or`, `not` or a relation filter do not hold for every
returned row and never rank. A column with `@omit read` gets neither the
field nor the ordering.

```graphql
{
  allArticles(filter: {search: {matches: "postgres & index"}}, orderBy: [SEARCH_RANK_DESC]) {
    nodes { title searchRank }
  }
}
```

`<field>Rank` is `null` when the list has no such filter (and for rows read
outside a filtered list: relations, mutation payloads). Ordering by rank
without one is an error. Keyset cursors work with rank ordering.

## Trigram similarity (pg_trgm)

Text columns with trigram operators also get `<COLUMN>_SIMILARITY_ASC/_DESC`,
`<COLUMN>_WORD_SIMILARITY_ASC/_DESC` and
`<COLUMN>_STRICT_WORD_SIMILARITY_ASC/_DESC` orderBy values, and fields taking
the table's orderBy gain `similarTo: <Type>SimilarToInput`, one reference
text per column:

```graphql
{
  allAccounts(similarTo: {fullName: "Ada Lovlace"}, orderBy: [FULL_NAME_SIMILARITY_DESC], first: 5) {
    nodes { fullName }
  }
}
```

`_DESC` puts the most similar rows first. The values compile to the pg_trgm
distance operators (`<->`, `<->>`, `<->>>`), which a GiST trigram index
answers directly (nearest-first, like spatial `near:`). Ordering by
similarity without a reference text for that column is an error. Combine
with a `similarTo` filter to drop weak matches.

## Mutations

Inputs go through each type's constructor or cast: `hstore($1::text[])` from
the JSON object (`null` values allowed), `int4range($1::int4, $2::int4,
$3::text)` from the bound objects, `int4multirange(...)` from a list of them,
`$1::"ext".citext`, `$1::"ext".ltree`, `$1::inet`, `$1::tsvector`. Nested
mutations use the same path. Composite attributes of these types are sent
in their text form inside the composite's jsonb parameter (`"k"=>"v"` for
hstore, `["1","3")` for ranges).

## Settings

The `contrib` plugin (priority 170) adds the vocabulary operators, the rank
fields and the rank and similarity ordering; the type mapping, reads, writes
and generic comparisons are core. `plugins.disabled: [contrib]` removes the
plugin's surface.

| Key | Default | Effect |
|---|---|---|
| `plugins.settings.contrib.filters` | `true` | vocabulary operators, `StringTrgmFilterOps`, `<field>Rank` fields (and so rank ordering) |
| `plugins.settings.contrib.ordering` | `true` | `_RANK_` and `_SIMILARITY_` orderBy values and the `similarTo:` argument |

`@omit filter` / `@omit order` on a column removes its filter / ordering
surface; `@omit read` also removes its rank field and its rank and
similarity orderings, which sort against a reference of the client's
choosing.
