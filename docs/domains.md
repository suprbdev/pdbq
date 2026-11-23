# Domains

A domain (`CREATE DOMAIN`) over a built-in scalar type becomes a custom scalar
named after it, the way PostGraphile v4 aliases a domain's base type:

```sql
CREATE DOMAIN email AS text CHECK (VALUE ~ '^[^@]+@[^@]+$');
COMMENT ON DOMAIN email IS 'An email address.';
CREATE DOMAIN positive_int AS int CHECK (VALUE > 0);
CREATE DOMAIN small_positive AS positive_int CHECK (VALUE < 100);
COMMENT ON DOMAIN small_positive IS E'@name Percent\nA percentage.';

CREATE TABLE people (id int PRIMARY KEY, email email NOT NULL, aliases email[], score small_positive);
```

```graphql
"""
An email address.

Constraints:
- `CHECK ((VALUE ~ '^[^@]+@[^@]+$'::text))`
"""
scalar Email

"""
A percentage.

Constraints:
- `CHECK (((VALUE)::integer < 100))`
- `CHECK ((VALUE > 0))`
"""
scalar Percent

type Person implements Node {
  email: Email!
  aliases: [Email!]
  score: Percent
}
```

- **Name**: `UpperCamel(domain)`. Rename it with `@name` in the domain's comment
  (see [smart comments](smart-comments.md#domains)) or with an inflection plugin
  (kind `domain_type_name`). A name already taken by another type (a table's
  object type, a built-in scalar such as `Date`) logs a warning and the domain
  keeps its base type.
- **Description**: the domain's comment without smart tags, then its `NOT NULL`
  and `CHECK` constraints and those of the domains it is declared over.
- **Values**: the scalar reads and writes exactly like the base type (an
  `int8` domain is a string-serialized `BigInt`-like scalar, a `jsonb` domain
  takes JSON), and inputs take the base type's literals. PostgreSQL enforces the
  domain's constraints; a violation surfaces as the usual constraint error.
- **Where**: table, view and composite columns, arrays of domains
  (`[Email!]`), condition and mutation inputs, function arguments and results,
  computed columns and OUT/TABLE record fields. A domain over a domain gets its
  own scalar.
- **Filters** use the base type's operator input (`StringFilter` for
  `email`, with `like`/`startsWith`/…), so a domain column filters exactly like
  a column of its base type.

Domains that keep their base type's GraphQL type, because that type carries
its own representation or operators: domains over arrays (`CREATE DOMAIN d AS
int[]` stays `[Int!]`), enums, composites, ranges, PostGIS types (`GeoJSON` or
typed geometry), and extension types such as `citext` (`String`), `hstore`,
`ltree` and `tsvector`.

Set `schema.domains: false` to type every domain as its base type
(`email` columns become `String`, `small_positive` ones `Int`).
