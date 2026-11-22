# PostGIS

pdbq maps PostGIS `geometry` and `geography` columns to a `GeoJSON` scalar and
generates the spatial query surface on top of them: bounding-box search,
DE-9IM predicates, radius search, derived accessors, reprojection and distance
ordering — all compiled into the same **single SQL statement** as the rest of
the query.

Nothing needs to be configured. If the database has a spatial column, the
surface appears; if it does not, the schema is byte-identical to what pdbq
generates without PostGIS installed.

## The GeoJSON scalar

| Direction | Representation |
|---|---|
| output | a GeoJSON object (`ST_AsGeoJSON`), never WKB hex |
| input | a GeoJSON object **or** a WKT/EWKT string |

```graphql
{
  allPlaces {
    nodes { name location }
  }
}
```
```json
{"name": "Trafalgar Square",
 "location": {"type": "Point", "coordinates": [-0.1281, 51.508]}}
```

Both input forms are accepted anywhere a spatial value is taken (filters,
`createX`, `updateX`):

```graphql
mutation {
  createPlace(input: {
    name: "Trafalgar Square"
    location: {type: "Point", coordinates: [-0.1281, 51.508]}
  }) { place { id } }
}

mutation {
  createPlace(input: {name: "Elsewhere", location: "SRID=4326;POINT(1 2)"}) {
    place { id }
  }
}
```

GeoJSON `Feature` and `FeatureCollection` documents are unwrapped
automatically (a `FeatureCollection` becomes a `GeometryCollection`), so a
value pasted straight out of a map library works without editing.

**SRIDs.** GeoJSON is WGS84 (4326) per RFC 7946, so GeoJSON input is tagged
`ST_SetSRID(..., 4326)` unless it carries an explicit legacy `crs` member.
EWKT (`SRID=3857;POINT(...)`) carries its own SRID and is passed through
unchanged. Bare WKT on a `geography` column is tagged 4326, since geography
requires a real CRS.

## Filtering

Every spatial column takes a `GeometryFilterOps` operand set. Operators marked
**index** can be answered by a GiST index; the rest are exact but scan more
rows, so pair them with a bounding-box operator when the table is large.

| Operator | Operand | SQL | Index |
|---|---|---|---|
| `bboxIntersects` | `BBoxInput` | `&&` | ✔ |
| `bboxContains` | `BBoxInput` | `~` | ✔ |
| `bboxContainedBy` | `BBoxInput` | `@` | ✔ |
| `intersects` | `GeoJSON` | `ST_Intersects` | ✔ |
| `disjoint` | `GeoJSON` | `ST_Disjoint` | |
| `contains` | `GeoJSON` | `ST_Contains` | ✔ |
| `containsProperly` | `GeoJSON` | `ST_ContainsProperly` | ✔ |
| `within` | `GeoJSON` | `ST_Within` | ✔ |
| `covers` | `GeoJSON` | `ST_Covers` | ✔ |
| `coveredBy` | `GeoJSON` | `ST_CoveredBy` | ✔ |
| `crosses` | `GeoJSON` | `ST_Crosses` | ✔ |
| `overlaps` | `GeoJSON` | `ST_Overlaps` | ✔ |
| `touches` | `GeoJSON` | `ST_Touches` | ✔ |
| `equals` | `GeoJSON` | `ST_Equals` | |
| `dwithin` | `DWithinInput` | `ST_DWithin` | ✔ |
| `beyond` | `DWithinInput` | `NOT ST_DWithin` | |
| `isNull` | `Boolean` | `IS NULL` | |

Spatial columns get **only** these operators: `equalTo`, `lessThan`, `in` and
friends are not generated, because PostGIS types have no meaningful scalar
ordering (and Postgres would reject most of them). For the same reason spatial
columns never appear in `condition:` or `distinctOn:`.

### Bounding box (viewport queries)

`BBoxInput` is `{minX, minY, maxX, maxY, srid}` — `srid` defaults to 4326.
This is the cheap first pass for "what is on screen":

```graphql
{
  allPlaces(filter: {
    location: {bboxIntersects: {minX: -0.5, minY: 51.3, maxX: 0.3, maxY: 51.7}}
  }) { nodes { name } }
}
```

### Geography columns

PostGIS provides geography overloads only for `bboxIntersects`, `intersects`,
`covers`, `coveredBy`, `dwithin` and `beyond`; these run natively (spheroidal,
metres). Every other predicate (`within`, `contains`, `crosses`, `touches`,
`disjoint`, `bboxContains`, ...) casts the column and the operand to
`geometry`, so it uses planar semantics.

### Radius search

`DWithinInput` is `{geometry, distance, useSpheroid}`.

**Units matter:** on a `geography` column the distance is **metres**; on a
`geometry` column it is in the column's **SRID units** (for EPSG:4326 that is
degrees, which is rarely what you want). Set `useSpheroid: true` to measure a
geometry column in metres — pdbq casts both sides to `geography`:

```graphql
{
  allPlaces(filter: {
    location: {dwithin: {
      geometry: {type: "Point", coordinates: [-0.1281, 51.5080]}
      distance: 5000        # metres, because useSpheroid is set
      useSpheroid: true
    }}
  }) { nodes { name } }
}
```

## Derived accessors

Each spatial column gains sibling fields computed in the same statement — no
extra round trip:

| Field | Returns | PostGIS |
|---|---|---|
| `<field>Area` | `Float` | `ST_Area` (m² for geography, SRID units for geometry) |
| `<field>Length` | `Float` | `ST_Length` |
| `<field>Perimeter` | `Float` | `ST_Perimeter` |
| `<field>Centroid` | `GeoJSON` | `ST_Centroid` |
| `<field>Envelope` | `GeoJSON` | `ST_Envelope` |
| `<field>Srid` | `Int` | `ST_SRID` |
| `<field>GeometryType` | `String` | `ST_GeometryType` (e.g. `ST_Point`) |
| `<field>IsValid` | `Boolean` | `ST_IsValid` |

```graphql
{
  allPlaces {
    nodes { name locationCentroid locationSrid areaArea }
  }
}
```

A real column that already owns one of these names keeps it; the accessor is
skipped rather than shadowing your data.

## Reprojection and simplification

Spatial fields take `transform:` (an SRID) and `simplify:` (a tolerance),
applied before the value is encoded as GeoJSON. Aliases let you fetch several
representations at once:

```graphql
{
  allPlaces {
    nodes {
      webMercator: location(transform: 3857)
      coarse:      location(simplify: 0.01)
    }
  }
}
```

`simplify` uses `ST_SimplifyPreserveTopology`, so the result is never an
invalid geometry.

## Distance ordering

Each spatial column adds `<COLUMN>_DISTANCE_ASC` / `_DESC` to the table's
`orderBy` enum, plus a `near:` argument supplying the reference geometry.
Ordering compiles to the KNN operator `<->`, which a GiST index answers
directly — this is the fast "nearest N" path:

```graphql
{
  allPlaces(
    near: {geometry: "SRID=4326;POINT(-0.1281 51.5080)"}
    orderBy: [LOCATION_DISTANCE_ASC]
    first: 10
  ) { nodes { name } }
}
```

`near:` is required for these values. Without it the distance term is dropped
(falling back to the primary-key order) rather than inventing a reference
point and returning a result that looks sorted but is not.

## Indexing

Spatial filters and ordering respect `filters.indexed_only` exactly like
scalar columns, so with that policy on, a spatial column needs a GiST index to
be filterable at all:

```sql
CREATE INDEX places_location_idx ON places USING gist (location);
```

This is worth doing regardless: without it, every spatial predicate is a
sequential scan.

## Configuration

The surface is generated by the built-in `postgis` plugin (priority 160).
Disabling it removes filters, accessors, projection arguments and distance
ordering; reading and writing spatial columns as GeoJSON is core behaviour and
keeps working.

```yaml
plugins:
  settings:
    postgis:
      filters: true      # GeometryFilterOps on spatial columns
      derived: true      # <field>Area, <field>Centroid, ...
      ordering: true     # <COLUMN>_DISTANCE_ASC/_DESC + near:
      projection: true   # transform:/simplify: field arguments
```

Per-column control uses the usual [smart comments](smart-comments.md):

```sql
COMMENT ON COLUMN places.secret_location IS '@omit filter,order';
```

## Notes and limits

- **Injection safety.** Geometries, bounding-box corners and distances are all
  bind parameters; the compiled SQL text is a pure function of the query shape.
  This is enforced by `FuzzSpatial` in `internal/compile`, the spatial twin of
  the `FuzzFilter` invariant.
- **Malformed input fails fast.** A value that is neither valid GeoJSON nor
  plausible WKT is rejected at compile time with a GraphQL error, rather than
  reaching Postgres as a type error mid-statement.
- **Not covered:** raster and topology types, and `geometry(TypeName, SRID)`
  type modifiers are not reflected in the GraphQL type (every spatial column is
  the same `GeoJSON` scalar regardless of its declared geometry subtype).
