# Full-text search, trigrams, unaccent, fuzzystrmatch, hstore, ltree, intarray, ranges, network types, vectors, cubes and earthdistance

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
| the same with `unaccent` installed | `String` | `String` | `StringUnaccentFilterOps` / `StringTrgmUnaccentFilterOps` (see [Unaccent](#unaccent)) |
| the same with `fuzzystrmatch` installed | `String` | `String` | `String[Trgm][Unaccent]FuzzyFilterOps` (see [fuzzystrmatch](#fuzzystrmatch)) |
| `int4[]` with `intarray` installed | `[Int!]` | `[Int!]` | `IntListFilterOps`, `IntListIntarrayFilterOps` with `matchesQueryInt` (see [intarray](#intarray)) |
| `vector`, `halfvec` (pgvector) | `[Float!]` | `[Float!]` | `VectorFilterOps` (`isNull` only), `VectorDistanceFilterOps` with distance thresholds (see [pgvector](#pgvector)) |
| `sparsevec` (pgvector) | `String` (`"{1:1.5,3:2}/5"`) | `String` | `VectorFilterOps`, `SparseVectorDistanceFilterOps` with distance thresholds |
| `bit`, `varbit` with pgvector installed | `String` (`"101"`) | `String` | `StringFilterOps`, `BitDistanceFilterOps` with Hamming/Jaccard thresholds (see [Bit vectors](#bit-vectors)) |
| `cube` | `Cube` (JSON: `[1, 2, 3]` for a point, `[[1, 2], [3, 4]]` for a box) | `Cube` (the same, or the text form `"(1, 2), (3, 4)"`) | `CubeFilterOps`, `CubeDistanceFilterOps` with distance thresholds (see [cube](#cube)) |
| earthdistance's `earth` (and domains over it) | `LatLon { lat lon }` | `LatLonInput` | `LatLonFilterOps` (`isNull` only), `LatLonDistanceFilterOps` with the radius filter (see [earthdistance](#earthdistance)) |
| `point` with earthdistance installed | `String` (unchanged) | `String` | `StringFilterOps`, `PointDistanceFilterOps` with the `<@>` radius filter on admitted columns (see [earthdistance](#earthdistance)) |

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
included. `citext`, `hstore`, `ltree`, the pgvector types, `cube` and `earth` map only
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
| `StringUnaccentFilterOps`, `StringTrgmUnaccentFilterOps` | every `StringFilterOps` / `StringTrgmFilterOps` operator, `unaccentEqualTo`, `unaccentLike`, `unaccentIncludes`, `unaccentStartsWith` (see [Unaccent](#unaccent)) |
| `StringFuzzyFilterOps`, `StringTrgmFuzzyFilterOps`, `StringUnaccentFuzzyFilterOps`, `StringTrgmUnaccentFuzzyFilterOps` | the operators of the input without `Fuzzy`, `levenshteinDistanceLessThan`, `soundexEqualTo`, `metaphoneEqualTo`, `doubleMetaphoneEqualTo` (see [fuzzystrmatch](#fuzzystrmatch)) |
| `IntListIntarrayFilterOps` | every `IntListFilterOps` operator, `matchesQueryInt` (`@@ query_int`, see [intarray](#intarray)) |
| `KeyValueHashFilterOps` | `containsKey` (`?`), `containsAllKeys` (`?&`), `containsAnyKeys` (`?\|`), `contains` (`@>`), `containedBy` (`<@`), `containsDefinedKey` (`defined(col, key)`: present with a non-null value) |
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
  `KeyValueHashFilterOps.containedBy` / `containsDefinedKey`,
  `LtreeFilterOps.ancestorOfAny` / `descendantOfAny`,
  `LtreeListFilterOps.ancestorOf`;
- the unaccent and fuzzystrmatch operators, which no index can serve, are
  offered only on columns listed in `filters.allow_columns`, and so are the
  ltree, hstore and intarray value filters and orderings (`<field>Nlevel`,
  `<field>Value`, `<field>Icount`) and Levenshtein distance ordering;
- an `int4[]` column takes `IntListIntarrayFilterOps` only when an index
  led by it is a GIN or GiST index (build it with `gin__int_ops` /
  `gist__int_ops`);
- a text column takes `StringTrgmFilterOps` only when an index led by it is a
  GIN or GiST index (build it with `gin_trgm_ops` / `gist_trgm_ops`), so the
  common btree-indexed text column keeps `StringFilterOps`;
- trigram ordering needs a GiST index (only GiST serves the KNN distance
  operators);
- each vector distance ordering needs an index led by the column that is
  built for that metric: HNSW with the metric's operator class
  (`vector_l2_ops`, `vector_cosine_ops`, `bit_hamming_ops`, ...) for any
  of them, IVFFlat for all but L1 and `sparsevec` (and, on bit columns,
  Hamming only);
- the vector and bit distance thresholds, which no index serves, are offered
  only on columns listed in `filters.allow_columns`;
- the cube distance thresholds, distance orderings and the earth radius filter
  and distance ordering need a GiST index led by the column (or an allowlist
  entry); the `point` surface of earthdistance, which no index serves, is
  offered only on allowlisted columns.

Columns listed in `filters.allow_columns` get everything on their own inputs
(trigram, unaccent, fuzzystrmatch, intarray, vector, bit, cube, earth and
point surface, and the value filters and orderings; the hstore and ltree operators above live on
shared inputs and stay out). Indexes are matched by access method and by
the leading column's operator class: a btree_gist `gist_text_ops` index
does not make a text column a trigram column, a GIN `array_ops` index does
not serve intarray's `@@`, and an HNSW index serves only the metric of its
operator class.

Full-text search over plain text, `json` or `jsonb` columns
(`to_tsvector(col) @@ ...`; pdbcore's `textsearch` and `jsonsearch`
vocabularies) is not exposed: it cannot use an index unless an expression
index matches the exact configuration. Add a generated column instead
(`to_tsvector('<config>', jsonb)` is immutable too, and indexes the
document's string values):

```sql
ALTER TABLE articles ADD COLUMN search tsvector
  GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || body)) STORED;
ALTER TABLE events ADD COLUMN payload_search tsvector
  GENERATED ALWAYS AS (to_tsvector('english', payload)) STORED;
CREATE INDEX ON articles USING gin (search);
```

pdbr, the REST sibling, offers these searches (as PostgREST does) on columns
the [indexed-only policy](filtering.md#the-indexed-only-policy) admits.

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

## Full-text headlines

Every `tsvector` column with a `<field>Rank` field also gets a highlighted
snippet field, `<field>Headline(document: <Type>HeadlineDocument!, options:
FullTextHeadlineOptions): String`, when its table has text columns.
`document` names one of the table's readable `text`, `varchar`, `char(n)` or
`citext` columns (the source the `tsvector` was built from, typically), and
the field renders `ts_headline` of that column against the same match the
rank uses: the first `matches*` operator on the `tsvector` column at the
conjunctive top level of the list's filter, with its query and `config`.

```graphql
{
  allArticles(filter: {search: {matchesWebsearch: "postgres index", config: "english"}}, orderBy: [SEARCH_RANK_DESC], first: 10) {
    nodes {
      title
      searchRank
      snippet: searchHeadline(document: BODY, options: {maxWords: 20, minWords: 5, startSel: "<mark>", stopSel: "</mark>"})
    }
  }
}
```

`FullTextHeadlineOptions` holds the `ts_headline` options; omitted fields
keep PostgreSQL's defaults:

| Field | Range | Default |
|---|---|---|
| `maxWords`, `minWords` | 1 to 100, `minWords` < `maxWords` | 35, 15 |
| `shortWord` | 0 to 100 | 3 |
| `maxFragments` | 0 to 10 (0 shows one headline) | 0 |
| `highlightAll` | the whole document, ignoring the word and fragment limits | false |
| `startSel`, `stopSel`, `fragmentDelimiter` | 1 to 32 bytes, no `"`, `\` or control characters | `<b>`, `</b>`, `" ... "` |

Out-of-range options are a GraphQL error. The options are rendered into
`ts_headline`'s options text and bound as one parameter, the query and
config reuse the match's parameters, and the document is a column, so the
SQL text does not depend on any of the values.

The field is `null` where `<field>Rank` is: without such a match (a match
under `or`, `not` or a relation filter does not count), for rows read
outside a filtered list (relations, lookups, mutation payloads) and for a
`NULL` document. Document columns with `@omit read` (or hidden by a schema
contract) are not in the enum; `@omit read` or `@omit filter` on the
`tsvector` column removes the field with its rank. `json` and `jsonb`
columns are not documents here because the field is a `String` (pdbr
returns their highlighted document as JSON).

`ts_headline` parses the whole document on every call, whatever the
options, so it is bounded three ways: it runs only for the rows of the page
(plus the one look-ahead row): it is a projection of the page subquery,
which PostgreSQL evaluates above the sort and the `LIMIT`; the options cap
what it returns per row; and the cost estimator charges each headline 10 per
row on top of the column reads (pdbcore's `contrib.HeadlineCost`), so a
`first: 100` page of headlines costs about 1,000.

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

### Similarity fields

The scores themselves are fields: `<field>Similarity`,
`<field>WordSimilarity` and `<field>StrictWordSimilarity: Float` (0 to 1,
higher is more similar) evaluate `similarity`, `word_similarity` and
`strict_word_similarity` against the `similarTo:` text for the same column
of the list the row was read through:

```graphql
{
  allAccounts(
    filter: {fullName: {similarTo: "Ada Lovlace"}}
    similarTo: {fullName: "Ada Lovlace"}
    orderBy: [FULL_NAME_SIMILARITY_DESC]
    first: 5
  ) {
    nodes { fullName fullNameSimilarity }
  }
}
```

A field is `null` when the list has no reference for its column, and for
rows read outside such a list (single-row lookups, forward relations,
mutation payloads). The fields exist on every column that takes the trigram
operators (a GIN or GiST trigram index, or an allowlisted column, under
`filters.indexed_only`), so `similarTo:` also names columns that have
similarity fields but no similarity ordering (a GIN-indexed column). They
are projections of the returned rows: `@omit order` keeps them, `@omit
read` removes them, and the cost estimator prices each like a column.

## Unaccent

With the `unaccent` extension installed, text columns (`text`, `varchar`,
`char(n)`, `citext`) can take four comparisons that ignore both accents and
case:

| Operator | SQL |
|---|---|
| `unaccentEqualTo` | `lower(unaccent(col)) = lower(unaccent($1))` |
| `unaccentLike` | `unaccent(col) ILIKE unaccent($1)`: the operand is a LIKE pattern (`%`, `_`) |
| `unaccentIncludes` | the same with `%value%`, the operand's wildcards escaped |
| `unaccentStartsWith` | the same with `value%`, the operand's wildcards escaped |

```graphql
{
  allDishes(filter: {name: {unaccentIncludes: "creme brulee"}}) {
    nodes { name }   # "Crème Brûlée"
  }
}
```

`unaccent` is `STABLE`, so no index can serve these operators (an expression
index over an `IMMUTABLE` wrapper does not match what pdbq emits). With
`filters.indexed_only` on (the default) they are therefore offered only on
columns listed in `filters.allow_columns`; with it off, on every text column.
The column then takes `StringUnaccentFilterOps`, or
`StringTrgmUnaccentFilterOps` when it also takes the trigram operators; other
text columns keep their input, so a database without unaccent, or without an
admitted column, gets the schema it got before. pdbq calls the two-argument
`unaccent('"ext"."unaccent"'::regdictionary, ...)`, so the dictionary
resolves when the extension's schema is off the search_path.

## pgvector

pgvector columns map without configuration when the `vector` extension is
installed: `vector` and `halfvec` read and write as lists of numbers
(`[Float!]`, output straight from the value's text form), `sparsevec` as its
text form (`"{1:1.5,3:2}/5"`, a `String`). Function arguments and results of
these types map the same way. Inputs are bound as one text parameter cast to
the column's type (`$1::"ext".vector`), so a value of the wrong dimension is
reported by Postgres as a client error. Without pgvector installed nothing
changes, and pdbq does not need it to run.

Vectors have no meaningful scalar comparison, so like spatial columns they
get no `equalTo`/`lessThan`/`in` operators, no `condition:` field, no
`distinctOn:` value and no `<COLUMN>_ASC` ordering; their filter input,
`VectorFilterOps`, holds `isNull` only. What they get is nearest-neighbour
ordering:

| orderBy value | pgvector operator |
|---|---|
| `<COLUMN>_L2_DISTANCE_ASC/_DESC` | `<->` (Euclidean) |
| `<COLUMN>_NEGATIVE_INNER_PRODUCT_ASC/_DESC` | `<#>` (negative inner product; smallest is most similar) |
| `<COLUMN>_COSINE_DISTANCE_ASC/_DESC` | `<=>` (1 - cosine similarity) |
| `<COLUMN>_L1_DISTANCE_ASC/_DESC` | `<+>` (taxicab; pgvector 0.7 and later) |

Fields taking the table's orderBy gain `nearVector: <Type>NearVectorInput`,
one reference vector per column with distance ordering (`[Float!]`, or a
`String` for `sparsevec`):

```graphql
{
  allItems(nearVector: {embedding: [0.12, -0.4, 0.9]}, orderBy: [EMBEDDING_COSINE_DISTANCE_ASC], first: 10) {
    nodes { id embedding }
  }
}
```

`_ASC` is nearest first. It compiles to `ORDER BY (embedding) OPERATOR("ext".<=>)
$1::"ext".vector LIMIT n`, which an HNSW or IVFFlat index built with the
matching operator class (`vector_cosine_ops` here) answers as an approximate
nearest-neighbour scan. Ordering by a distance without a reference for that
column is an error, and the reference must have the column's dimensions.
Keyset cursors work as with similarity ordering. Rows whose vector is `NULL`
have no distance: a sequential scan sorts them last, an index scan skips
them, so filter them out (`{embedding: {isNull: false}}`) when that
difference matters. Under `filters.indexed_only` each metric is offered only
when an index led by the column is built for it (see
[above](#filtersindexed_only)). The `dims` and `norm` accessors of pdbcore's
vocabulary are not exposed, like the inet and range accessors (the ltree,
hstore and intarray values are, see [below](#ltree-paths)).

### Distance fields

Every readable vector column gets one `Float` field per metric, measured
from the `nearVector:` reference for that column of the list the row was
read through: `<field>L2Distance`, `<field>NegativeInnerProduct`,
`<field>CosineDistance` and `<field>L1Distance` (pgvector 0.7 and later).
They are `null` without a reference and for rows read outside such a list,
and exist whether or not the metric's ordering is offered, so `nearVector:`
names every vector column (a reference for a column without ordering only
feeds its fields):

```graphql
{
  allItems(nearVector: {embedding: [0.12, -0.4, 0.9]}, orderBy: [EMBEDDING_COSINE_DISTANCE_ASC], first: 10) {
    nodes { id embeddingCosineDistance }
  }
}
```

### Distance thresholds

"Similar enough" cut-offs are filters: on columns the policy admits them
for, the filter takes `VectorDistanceFilterOps` (`vector`, `halfvec`) or
`SparseVectorDistanceFilterOps` (`sparsevec`), `isNull` plus one
`<distance>LessThan` operator per metric, each taking its own reference and
an exclusive bound:

```graphql
{
  allItems(
    filter: {embedding: {cosineDistanceLessThan: {vector: [0.12, -0.4, 0.9], distance: 0.2}}}
    nearVector: {embedding: [0.12, -0.4, 0.9]}
    orderBy: [EMBEDDING_COSINE_DISTANCE_ASC]
    first: 10
  ) { nodes { id embeddingCosineDistance } }
}
```

| Operator | SQL |
|---|---|
| `l2DistanceLessThan` | `(embedding <-> $1::vector) < $2` |
| `negativeInnerProductLessThan` | `(embedding <#> $1::vector) < $2` |
| `cosineDistanceLessThan` | `(embedding <=> $1::vector) < $2` |
| `l1DistanceLessThan` | `(embedding <+> $1::vector) < $2` (pgvector 0.7 and later) |

The operand is `VectorDistanceInput {vector: [Float!]!, distance: Float!}`
(`SparseVectorDistanceInput` takes the `vector` as a `String`). pgvector
indexes serve ordering, not these predicates, so on their own they scan;
with `filters.indexed_only` on they are offered only on columns listed in
`filters.allow_columns` (every vector column with it off), and other
columns keep `VectorFilterOps`. Combined with nearest-first ordering by the
same distance and `first:`, an approximate index scan feeds the filter.

### Bit vectors

With pgvector 0.7 or later installed, `bit` and `varbit` columns (binary
embeddings) take pgvector's Hamming (`<~>`) and Jaccard (`<%>`) distances:
`<COLUMN>_HAMMING_DISTANCE_ASC/_DESC` and `<COLUMN>_JACCARD_DISTANCE_ASC/_DESC`
orderings, `<field>HammingDistance` / `<field>JaccardDistance` fields and
`hammingDistanceLessThan` / `jaccardDistanceLessThan` thresholds (on
`BitDistanceFilterOps`, `StringFilterOps` plus the two). References are bit
strings (`"1010"`), sent as `$1::varbit`; one of another length is rejected
by pgvector. A bit column gets this surface only where an index serves a
metric (HNSW with `bit_hamming_ops` / `bit_jaccard_ops`, IVFFlat with
`bit_hamming_ops`) or the policy admits it (allowlisted, or
`indexed_only` off), so installing pgvector leaves other bit columns alone.

Binary quantization keeps such a column next to the full vector and
re-ranks a coarse Hamming search by the exact distance:

```sql
ALTER TABLE items ADD COLUMN code bit(1536)
  GENERATED ALWAYS AS (binary_quantize(embedding)::bit(1536)) STORED;
CREATE INDEX ON items USING hnsw (code bit_hamming_ops);
```

```graphql
{
  allItems(nearVector: {code: "0110…", embedding: [0.12, …]}, orderBy: [CODE_HAMMING_DISTANCE_ASC], first: 100) {
    nodes { id codeHammingDistance embeddingCosineDistance }
  }
}
```

Here the index answers the 100 nearest codes and the client re-ranks them
by `embeddingCosineDistance`; the reference code is the sign bits of the
query vector (`binary_quantize` of it). To re-rank inside the database in
one statement, wrap both stages in a function, exposed as a root field
(`searchItems(q: [...], n: 10)`):

```sql
CREATE FUNCTION search_items(q vector(1536), n int DEFAULT 10) RETURNS SETOF items
  STABLE LANGUAGE sql AS $$
  SELECT * FROM (
    SELECT * FROM items ORDER BY code <~> binary_quantize(q)::bit(1536) LIMIT n * 10
  ) AS candidates
  ORDER BY embedding <=> q LIMIT n
$$;
```

## cube

With the `cube` extension installed, `cube` columns read and write as the
`Cube` scalar: a point is a JSON list of its coordinates (`[1, 2, 3]`), a box
the pair of its opposite corners (`[[1, 2], [3, 4]]`). Inputs also accept the
cube text form as a string (`"(1, 2), (3, 4)"`), which Postgres validates;
every input is one text parameter cast to the type
(`$1::"ext".cube`). Output is built from the value's text form in the same
statement.

`CubeFilterOps` holds `equalTo`, `notEqualTo` and `isNull` (cube's own
equality) plus `contains` (`@>`), `containedBy` (`<@`) and `overlaps` (`&&`),
each taking a `Cube`. Like vectors, cube columns get no `condition:` field,
`distinctOn:` value or plain `<COLUMN>_ASC` ordering: cube's btree order is a
storage order with no meaning. What they get is distance ordering against
the `nearVector:` reference for the column, which takes a `Cube`:

| orderBy value | cube operator | field |
|---|---|---|
| `<COLUMN>_L2_DISTANCE_ASC/_DESC` | `<->` (Euclidean) | `<field>L2Distance` |
| `<COLUMN>_L1_DISTANCE_ASC/_DESC` | `<#>` (taxicab) | `<field>L1Distance` |
| `<COLUMN>_CHEBYSHEV_DISTANCE_ASC/_DESC` | `<=>` (Chebyshev, L-infinity) | `<field>ChebyshevDistance` |

```graphql
{
  allShapes(
    filter: {pos: {l2DistanceLessThan: {cube: [1, 2], distance: 5}, containedBy: [[0, 0], [10, 10]]}}
    nearVector: {pos: [1, 2]}
    orderBy: [POS_L2_DISTANCE_ASC]
    first: 10
  ) { nodes { id pos posL2Distance } }
}
```

A GiST index on the column serves all of it: the operators directly, the
orderings as KNN scans (`ORDER BY pos <-> $1::cube LIMIT n`), and the
`<distance>LessThan: {cube, distance}` thresholds of `CubeDistanceFilterOps`
through an overlap prefilter with the reference enlarged by the distance
(`pos && cube_enlarge($1, $2, cube_dim($1)) AND (pos <-> $1) < $2`). The
KNN scans need PostgreSQL 17 or later, which adds the primary-key
tiebreaker every ordering carries by an incremental sort over the index
order; earlier versions sort the filtered rows instead (the same holds for
the earth ordering below). Under
`filters.indexed_only` the orderings and thresholds are therefore offered only
on columns led by a GiST index (or allowlisted); other filterable cube
columns keep `CubeFilterOps`. The distance fields come with every readable
cube column, `null` without a reference, and the cost estimator prices each
like a column. Cube columns take no aggregates either.

## earthdistance

With `earthdistance` (which needs `cube`) installed, columns declared as its
`earth` domain, or a domain over it, read and write as `LatLon { lat lon }`
(degrees) with input `LatLonInput`; an `earth` value is written through
`ll_to_earth($1::float8, $2::float8)` and read back with `latitude()` and
`longitude()`. Latitude must lie within ±90 and longitude within ±180.
`LatLonFilterOps` holds `isNull`; `LatLonDistanceFilterOps` adds the radius
filter `earthDistanceLessThan: {lat, lon, distance}` (metres), compiled to the
index-friendly `earth_box(ll_to_earth($1, $2), $3) @> col` prefilter refined
by `earth_distance(col, ll_to_earth($1, $2)) < $3`.

Distance to a reference location is an ordering and a score. Fields taking
the table's orderBy gain `nearPoint: <Type>NearPointInput`, one `LatLonInput`
per earth (and admitted point) column:

```graphql
{
  allStores(
    filter: {location: {earthDistanceLessThan: {lat: 51.5074, lon: -0.1278, distance: 5000}}}
    nearPoint: {location: {lat: 51.5074, lon: -0.1278}}
    orderBy: [LOCATION_EARTH_DISTANCE_ASC]
    first: 10
  ) { nodes { id location { lat lon } locationEarthDistance } }
}
```

`<field>EarthDistance: Float` is the great-circle distance in metres
(`earth_distance`). `<COLUMN>_EARTH_DISTANCE_ASC/_DESC` orders by cube's `<->`
distance between the two points instead, the chord through the Earth, which
orders rows exactly like the great-circle distance and which a GiST index on
the column answers as a KNN scan; keyset cursors compare that same chord
value. Under `filters.indexed_only` the radius filter and the ordering need a
GiST index led by the column (or an allowlist entry); the distance field
comes with every readable earth column. A reference outside the latitude and
longitude ranges is a client error, and ordering without a reference for the
column is an error. Earth values inside composite inputs are rejected (their
text form is a cube of Earth-centred coordinates).

earthdistance before 1.2 (PostgreSQL 15 and earlier) calls the cube
functions unqualified, so its schema and cube's must be on the search_path
there; from 1.2 on the extensions can live in any schema, and pdbq
schema-qualifies everything it emits.

`point` columns holding (longitude, latitude) take earthdistance's `<@>`
operator, in statute miles: `<COLUMN>_EARTH_DISTANCE_MILES_ASC/_DESC`,
`<field>EarthDistanceMiles` and `earthDistanceMilesLessThan: {lat, lon,
distance}` on `PointDistanceFilterOps` (`StringFilterOps` plus the radius
filter), all against `nearPoint:`. No index serves `<@>`, so like the bit
surface of pgvector it is generated only where the policy admits unindexed
surface (an allowlisted column, or `indexed_only` off): installing
earthdistance leaves other point columns' schema unchanged. For
index-assisted radius search store an `earth` column (a generated column
over `ll_to_earth(lat, lon)` works) and index it with GiST.

## fuzzystrmatch

With the `fuzzystrmatch` extension installed, text columns (`text`,
`varchar`, `char(n)`, `citext`) can take edit-distance and phonetic
comparisons:

| Operator | SQL |
|---|---|
| `levenshteinDistanceLessThan: {text, distance}` | `levenshtein_less_equal(col, $1, $2) < $2`: fewer than `distance` single-character edits away from `text` |
| `soundexEqualTo` | `soundex(col) = soundex($1)` |
| `metaphoneEqualTo` | `metaphone(col, 255) = metaphone($1, 255)` |
| `doubleMetaphoneEqualTo` | `dmetaphone(col) = dmetaphone($1)` (primary code) |

```graphql
{
  allPeople(
    filter: {lastName: {levenshteinDistanceLessThan: {text: "Jonson", distance: 3}}}
    similarTo: {lastName: "Jonson"}
    orderBy: [LAST_NAME_LEVENSHTEIN_DISTANCE_ASC]
  ) { nodes { lastName lastNameLevenshteinDistance } }
}
```

`<COLUMN>_LEVENSHTEIN_DISTANCE_ASC/_DESC` orders by `levenshtein(col,
similarTo)` (`_ASC` is nearest first) and `<field>LevenshteinDistance: Int`
selects it, both against the list's `similarTo:` text for the column, like
the trigram similarities; ordering without one is an error and the field is
`null` without it. fuzzystrmatch rejects arguments longer than 255
characters (`metaphone`: 255 bytes): a longer reference text is a client
error, and a longer column value has no distance (`null`, sorted last) and
matches neither an operator nor its negation under `not`. Levenshtein
comparisons are case-sensitive, also on `citext`; the phonetic codes are
not.

No index serves these operators (an expression index such as
`(soundex(col))` does not match what pdbq emits), so under
`filters.indexed_only` they, the ordering and the distance field are offered
only on columns listed in `filters.allow_columns`; with it off, on every
text column. The column then takes the `…FuzzyFilterOps` variant of its
input, named after everything it adds to `StringFilterOps`
(`StringTrgmUnaccentFuzzyFilterOps` with pg_trgm and unaccent too).

## hstore values

Every readable `hstore` column gets `<field>Value(key: String!): String`, the
value of one key (`col -> $1`), `null` when the key is absent or its value is
`null`. Under the policy below it is also a filter and an ordering:

```graphql
{
  allProducts(
    filter: {attrsValue: {key: "color", in: ["red", "blue"]}}
    hstoreKey: {attrs: "size"}
    orderBy: [ATTRS_VALUE_ASC]
  ) { nodes { id color: attrsValue(key: "color") size: attrsValue(key: "size") } }
}
```

`<field>Value: KeyValueHashValueFilter` takes the `key` plus the
`StringFilterOps` comparisons of its value (`isNull: true` matches rows
without the key); several keys combine with `and`. `<COLUMN>_VALUE_ASC/_DESC`
sorts by the value of the key named for the column in the list's
`hstoreKey: <Type>HstoreKeyInput` argument (`null` values last), and is an
error without it. Keyset cursors work with it. `containsDefinedKey: "k"`
(`defined(col, k)`) on `KeyValueHashFilterOps` matches a key present with a
non-null value.

## ltree paths

`ltree` columns get `<field>Nlevel: Int` (number of labels), `<field>Parent:
String` (the path without its last label: `""` for a single label, `null`
for the empty path), `<field>Lca(path: String!): String` (the longest common
ancestor with the given path) and `<field>Subpath(offset: Int!, length: Int):
String` (the labels from `offset`, counted from the end when negative,
`length` of them or the rest of the path; `null` when `offset` lies outside
the row's path instead of ltree's error; a negative `length` is a client
error). `ltree[]` columns get `<field>Lca: String`, the longest common
ancestor of the non-null elements. `<field>Nlevel` is also a filter
(`<field>Nlevel: IntFilterOps`) and an ordering (`<COLUMN>_NLEVEL_ASC/_DESC`):

```graphql
{
  allCategories(filter: {pathNlevel: {lessThanOrEqualTo: 2}}, orderBy: [PATH_NLEVEL_ASC]) {
    nodes { path pathParent pathSubpath(offset: -1) }
  }
}
```

## intarray

With the `intarray` extension installed, `int4[]` columns can take
`matchesQueryInt`, intarray's boolean query match (`col @@ '1&(2|3)'::query_int`;
`&`, `|`, `!` and parentheses), on `IntListIntarrayFilterOps`. A GIN index
built with `gin__int_ops` (or GiST with `gist__int_ops` / `gist__intbig_ops`)
serves it, so under `filters.indexed_only` the column needs a GIN or GiST
index led by it (or an allowlist entry). A malformed query is a client
error; so is a row whose array contains a `NULL` element, which intarray's
operators reject. `<field>Icount: Int` selects the number of elements and,
like `<field>Nlevel`, is also a filter and an ordering
(`<COLUMN>_ICOUNT_ASC/_DESC`). Containment and overlap stay the core array
operators (`contains`, `containedBy`, `overlaps`).

### Policy and smart comments

The value fields (`<field>Nlevel`, `<field>Parent`, `<field>Lca`,
`<field>Subpath`, `<field>Value`, `<field>Icount`) are projections of the
row: they exist whenever the plugin is enabled and the column is readable,
their arguments are bind parameters, and the cost estimator prices each like
a column. No index serves the value filters and orderings, so under
`filters.indexed_only` they are offered only on columns listed in
`filters.allow_columns` (on every column with it off). `@omit read` removes
the fields, filters and orderings, `@omit filter` the filters and `@omit
order` the orderings.

## Mutations

Inputs go through each type's constructor or cast: `hstore($1::text[])` from
the JSON object (`null` values allowed), `int4range($1::int4, $2::int4,
$3::text)` from the bound objects, `int4multirange(...)` from a list of them,
`$1::"ext".citext`, `$1::"ext".ltree`, `$1::inet`, `$1::tsvector`,
`$1::"ext".vector` from the list of numbers, `$1::"ext".cube` from the cube's
text form, `"ext".ll_to_earth($1::float8, $2::float8)` from a `LatLonInput`.
Nested mutations use the same path. Composite attributes of these types are
sent in their text form inside the composite's jsonb parameter (`"k"=>"v"`
for hstore, `["1","3")` for ranges, `(1, 2)` for cubes); `earth` attributes
are not supported there.

## Settings

The `contrib` plugin (priority 170) adds the vocabulary operators, the rank
fields, the ltree, hstore and intarray value fields, filters and orderings
and the rank, similarity, Levenshtein, vector, cube and earth distance
ordering; the type
mapping, reads, writes and generic comparisons are core. `plugins.disabled: [contrib]` removes the
plugin's surface.

| Key | Default | Effect |
|---|---|---|
| `plugins.settings.contrib.filters` | `true` | vocabulary operators, `StringTrgmFilterOps`, the unaccent, fuzzystrmatch and intarray inputs, the distance-threshold inputs, the value filters (`<field>Nlevel`, `<field>Value`, `<field>Icount`), `<field>Rank` fields (and so rank ordering) |
| `plugins.settings.contrib.ordering` | `true` | `_RANK_`, `_SIMILARITY_`, `_LEVENSHTEIN_DISTANCE_`, `_NLEVEL_`, `_ICOUNT_`, `_VALUE_`, vector and cube `_DISTANCE_`/`_INNER_PRODUCT_` and earthdistance `_EARTH_DISTANCE_`/`_EARTH_DISTANCE_MILES_` orderBy values, the similarity and distance fields and the `similarTo:`, `nearVector:`, `nearPoint:` and `hstoreKey:` arguments |

`@omit filter` / `@omit order` on a column removes its filter / ordering
surface (the similarity, distance and value fields stay); `@omit read` also
removes its rank, similarity, distance and value fields and its rank,
similarity and distance orderings, which measure the value against a
reference of the client's choosing. Schema contracts hide columns the same
way. If a table's `<Type>NearVectorInput` or `<Type>NearPointInput` name is
already taken, its vector and cube or earthdistance ordering is skipped and
the builder logs a warning; the same goes for `<Type>HstoreKeyInput` (hstore
value ordering), the text operator inputs, `TextDistanceInput`,
`KeyValueHashValueFilter`, `IntListIntarrayFilterOps` and the
distance-threshold inputs and operands (`CubeDistanceFilterOps`,
`LatLonDistanceInput`, ...), and for a value field or filter field whose name
a column already takes. A table taking the `Cube`, `LatLon` or `LatLonInput`
name makes cube or earth columns fall back to `String`.
