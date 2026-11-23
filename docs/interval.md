# Intervals

`interval` columns surface as the PostGraphile `Interval` object, with an
`IntervalInput` for writes and filters:

```graphql
type Interval {
  seconds: Float
  minutes: Int
  hours: Int
  days: Int
  months: Int
  years: Int
}
input IntervalInput { seconds: Float  minutes: Int  hours: Int  days: Int  months: Int  years: Int }
```

Every field is optional. `seconds` is the only non-integer field.

## Output

The fields are PostgreSQL's own decomposition of the stored value
(`extract(year|month|day|hour|minute|second FROM x)`), the same split
PostGraphile reads from the interval's text form. Nothing is justified:
`'14 months'` is `{years: 1, months: 2}`, `'100 hours'` is `{hours: 100}`
(hours never overflow into days), and the time fields share one sign
(`'-04:05:06.789'` is `{hours: -4, minutes: -5, seconds: -6.789}`). A
`NULL` interval is `null`. Unselected fields cost nothing.

## Input

An `IntervalInput` becomes `make_interval(years => $1, months => $2, days =>
$3, hours => $4, mins => $5, secs => $6)` over bound parameters; omitted
fields are 0. Fields add up as PostgreSQL adds them, so `{hours: 1, minutes:
90}` reads back as `{hours: 2, minutes: 30}`. `years`, `months`, `days`,
`hours` and `minutes` must be 32-bit integers. `seconds` is a double, so
sub-microsecond digits are rounded by PostgreSQL.

## Where it applies

- Columns, `interval[]` columns (`[Interval!]` / `[IntervalInput!]`),
  composite attributes, create and patch inputs, `condition:`.
- Function arguments, results and computed columns.
- Filters: `IntervalFilterOps` has `equalTo`, `notEqualTo`, `in`, `notIn`,
  `isNull`, `lessThan`, `lessThanOrEqualTo`, `greaterThan` and
  `greaterThanOrEqualTo` with `IntervalInput` operands, compared by
  PostgreSQL's interval ordering (`'1 month'` equals `'30 days'`).
  `interval[]` columns use `IntervalListFilterOps`
  (`equalTo`, `notEqualTo`, `contains`, `containedBy`, `overlaps`, `isNull`).
- orderBy on the column.
- Aggregates: `sum`, `min` and `max` return `Interval`; aggregate filters
  take `IntervalFilterOps`.

Domains over `interval` keep the `Interval` type. If a table, type or
enum is already named `Interval` or `IntervalInput`, the column falls back
to `String` and a warning is logged.
