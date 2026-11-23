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

**SRIDs.** GeoJSON is WGS84 (4326) per RFC 7946, unless it carries an
explicit legacy `crs` member (`EPSG:n` or `urn:ogc:def:crs:EPSG::n`). EWKT
(`SRID=3857;POINT(...)`) carries its own SRID and is passed through
unchanged. Bare WKT is rewritten to EWKT before it is bound: on a `geometry`
column it is tagged with the column's SRID (it stays SRID 0 on an
unconstrained column), and on a `geography` column with 4326, since
geography requires a real CRS. The SRID therefore always travels inside the
parameter, never in the SQL text.

Every spatial value compiles to the same expression whatever its input form,
so the SQL text depends only on the column type (one prepared statement
serves GeoJSON, WKT and EWKT alike):

| column | value expression |
|---|---|
| `geometry` (SRID 0) | `CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END` |
| `geometry(..., 3857)` | `CASE WHEN left($1, 1) = '{' THEN ST_Transform(ST_GeomFromGeoJSON($1), 3857) ELSE ST_GeomFromEWKT($1) END` |
| `geography` | `(CASE WHEN left($1, 1) = '{' THEN ST_GeomFromGeoJSON($1) ELSE ST_GeomFromEWKT($1) END)::geography` |

When the column's type modifier fixes an SRID (`geometry(Point, 3857)`), every
value bound for that column (mutation inputs, filter operands, the `near:`
geometry) follows it:

| input | on `geometry(..., 3857)` |
|---|---|
| bare WKT `POINT(1 2)` | bound as `SRID=3857;POINT(1 2)` |
| EWKT `SRID=4326;POINT(1 2)` | keeps 4326; PostgreSQL rejects the mismatch |
| GeoJSON (4326, or its `crs`) | reprojected by the `ST_Transform(..., 3857)` branch (a no-op when already 3857) |
| bounding box without `srid` | built in 3857 |

An unconstrained `geometry` column (SRID 0) gets the plain rules above.
Geography columns are always 4326, so nothing is reprojected for them.
Function arguments have no type modifier, so they get the plain rules too. A
geometry that PostgreSQL rejects (mixed SRIDs in one operation, an invalid
shape) is reported to clients as `invalid geometry` (`invalid input value`
with `errors.detail: strict`).

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
| `bboxLeftOf` | `GeoJSON` | `<<` | ✔ |
| `bboxRightOf` | `GeoJSON` | `>>` | ✔ |
| `bboxBelow` | `GeoJSON` | `<<\|` | ✔ |
| `bboxAbove` | `GeoJSON` | `\|>>` | ✔ |
| `bboxOverlapsOrLeftOf` | `GeoJSON` | `&<` | ✔ |
| `bboxOverlapsOrRightOf` | `GeoJSON` | `&>` | ✔ |
| `bboxOverlapsOrBelow` | `GeoJSON` | `&<\|` | ✔ |
| `bboxOverlapsOrAbove` | `GeoJSON` | `\|&>` | ✔ |
| `bboxEquals` | `GeoJSON` | `~=` | ✔ |
| `exactlyEquals` | `GeoJSON` | `=` | B-tree |
| `orderingEquals` | `GeoJSON` | `ST_OrderingEquals` | ✔ |
| `intersects3D` | `GeoJSON` | `ST_3DIntersects` | ✔ |
| `bboxIntersectsND` | `GeoJSON` | `&&&` | n-D GiST |
| `bboxIntersects2D` | `GeoJSON` | `&&` | ✔ |

The directional and equality bounding-box operators (`bboxLeftOf` through
`bboxIntersects2D`) compare against the **bounding box of a geometry**, not a
`BBoxInput`, as `postgraphile-plugin-connection-filter-postgis` does; pass an
envelope polygon to compare against a box. `bboxIntersects2D` is the same `&&`
as `bboxIntersects`, with a GeoJSON or WKT operand instead of corner
coordinates. `exactlyEquals` (`=`) is true only
for the same coordinates in the same order with the same SRID, and is served
by a B-tree rather than a GiST index. `bboxIntersectsND` (`&&&`) includes Z
and M in the comparison and needs an n-D GiST index
(`USING gist (col gist_geometry_ops_nd)`); `intersects3D` prefilters on the 2D
box with the default index.

Spatial columns get **only** these operators: `equalTo`, `lessThan`, `in` and
friends are not generated, because PostGIS types have no meaningful scalar
ordering (and Postgres would reject most of them). For the same reason spatial
columns never appear in `condition:` or `distinctOn:`.

### Bounding box (viewport queries)

`BBoxInput` is `{minX, minY, maxX, maxY, srid}` — `srid` defaults to the
column's typmod SRID, or 4326 for an unconstrained column.
This is the cheap first pass for "what is on screen":

```graphql
{
  allPlaces(filter: {
    location: {bboxIntersects: {minX: -0.5, minY: 51.3, maxX: 0.3, maxY: 51.7}}
  }) { nodes { name } }
}
```

### Geography columns

PostGIS provides geography overloads only for `bboxIntersects`,
`bboxIntersects2D`, `intersects`, `covers`, `coveredBy`, `dwithin`, `beyond`
and `exactlyEquals`; these run natively (spheroidal, metres). Every other predicate (`within`, `contains`,
`crosses`, `touches`, `disjoint`, `bboxContains`, `bboxLeftOf`,
`intersects3D`, ...) casts the column and the operand to `geometry`, so it
uses planar semantics; a GiST index on the geography column cannot serve
those (an expression index on `(col::geometry)` can).

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
| `<field>Length` | `Float` | `ST_Length` (metres for geography, SRID units for geometry) |
| `<field>Perimeter` | `Float` | `ST_Perimeter` (metres for geography, SRID units for geometry) |
| `<field>Centroid` | `GeoJSON` | `ST_Centroid` (spheroidal for geography) |
| `<field>Envelope` | `GeoJSON` | `ST_Envelope` |
| `<field>Srid` | `Int` | `ST_SRID` |
| `<field>GeometryType` | `String` | `ST_GeometryType` (e.g. `ST_Point`) |
| `<field>IsValid` | `Boolean` | `ST_IsValid` |
| `<field>X` / `<field>Y` | `Float` | `ST_X` / `ST_Y` (points only) |
| `<field>Z` / `<field>M` | `Float` | `ST_Z` / `ST_M` (points only) |
| `<field>NumPoints` | `Int` | `ST_NPoints` |
| `<field>NumGeometries` | `Int` | `ST_NumGeometries` |
| `<field>StartPoint` / `<field>EndPoint` | `GeoJSON` | `ST_StartPoint` / `ST_EndPoint` (curves only) |
| `<field>PointOnSurface` | `GeoJSON` | `ST_PointOnSurface` |
| `<field>Wkt` | `String` | `ST_AsText` |
| `<field>Ewkt` | `String` | `ST_AsEWKT` (with `SRID=n;`) |
| `<field>IsEmpty` | `Boolean` | `ST_IsEmpty` |
| `<field>IsSimple` | `Boolean` | `ST_IsSimple` |
| `<field>Xmin` / `<field>Ymin` / `<field>Xmax` / `<field>Ymax` | `Float` | `ST_XMin` / `ST_YMin` / `ST_XMax` / `ST_YMax` |

```graphql
{
  allPlaces {
    nodes { name locationCentroid locationSrid areaArea }
  }
}
```

On geography columns `Area`, `Length` and `Perimeter` use the native
geography overloads and measure in metres on the spheroid, and `Centroid` is
the spheroidal centroid; a geography `GeometryCollection`, which the
geography overload rejects, falls back to the planar centroid of its
`geometry` cast.

A real column that already owns one of these names keeps it; the accessor is
skipped rather than shadowing your data.

**Type-specific accessors follow the column's type modifier.** `X`, `Y`, `Z`
and `M` only answer for points, `StartPoint` and `EndPoint` only for
LineString, CircularString and CompoundCurve. On an unconstrained column
(`geometry`, `geography`, `geometry(Geometry, 4326)`) they are all generated
and yield `null` for any other geometry type. When the typmod rules them out
they are not generated at all:

| Column | Gets | Omitted |
|---|---|---|
| `geometry(Point, 4326)` | `X`, `Y` | `Z`, `M`, `StartPoint`, `EndPoint` |
| `geometry(PointZ, 4326)` | `X`, `Y`, `Z` | `M`, `StartPoint`, `EndPoint` |
| `geometry(LineString, 4326)` | `StartPoint`, `EndPoint` | `X`, `Y`, `Z`, `M` |
| `geometry(Polygon, 4326)` | — | all six |
| `geography` | all six | — |

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

## Typed geometry

With `plugins.settings.postgis.typed_geometry: true`, spatial column fields
return objects instead of the `GeoJSON` scalar, in the shape of PostGraphile's
`@graphile/postgis` plugin. The default stays off, and with it off the schema
is byte-for-byte the same as without the setting.

```yaml
plugins:
  settings:
    postgis:
      typed_geometry: true
```

```graphql
{
  allPlaces {
    nodes {
      location { x y srid }            # geometry(Point, 4326) -> GeometryPoint
      area {                           # unconstrained geography -> GeographyInterface
        __typename
        geojson
        ... on GeographyPolygon {
          exterior { points { longitude latitude } }
          interiors { points { longitude latitude } }
        }
      }
    }
  }
}
```

**Types.** Every type has `geojson: GeoJSON` and `srid: Int!`. Geometry types
are generated for geometry columns and an identical `Geography…` family for
geography columns:

| Type | Fields |
|---|---|
| `GeometryInterface` | interface implemented by every geometry type |
| `GeometryGeometry`, `…GeometryZ`, `…GeometryM`, `…GeometryZM` | one interface per coordinate dimension |
| `GeometryPoint` | `x: Float!`, `y: Float!` (`z: Float!` on `PointZ`/`PointZM`) |
| `GeometryLineString` | `points: [GeometryPoint]` |
| `GeometryPolygon` | `exterior: GeometryLineString`, `interiors: [GeometryLineString]` |
| `GeometryMultiPoint` | `points: [GeometryPoint]` |
| `GeometryMultiLineString` | `lines: [GeometryLineString]` |
| `GeometryMultiPolygon` | `polygons: [GeometryPolygon]` |
| `GeometryGeometryCollection` | `geometries: [GeometryGeometry]` |

Every concrete type has `Z`, `M` and `ZM` variants (`GeometryPolygonZ`, whose
rings are `GeometryLineStringZ`, and so on), and implements its dimension
interface and `GeometryInterface`. On geography types the point fields are
`longitude`, `latitude` and `height`. As in `@graphile/postgis`, points have
no `m` field: `ST_AsGeoJSON` drops the M coordinate.

**Field type from the typmod.** The column's type modifier decides the field
type:

| Column | Field type |
|---|---|
| `geometry(Point, 4326)` | `GeometryPoint` |
| `geometry(MultiPolygonZ, 4326)` | `GeometryMultiPolygonZ` |
| `geometry(Geometry, 4326)` | `GeometryGeometry` (dimensions fixed, subtype not) |
| `geometry`, `geography` | `GeometryInterface`, `GeographyInterface` |
| `geometry(CircularString, 4326)`, `geometry(Tin, 4326)`, other curve and surface subtypes | stays `GeoJSON` |

Curve and surface subtypes stay on the scalar because GeoJSON has no
representation for most of them (`ST_AsGeoJSON` rejects curves and
PolyhedralSurface). Such a value in an unconstrained column fails to encode
exactly as it does with the scalar.

When the field type is an interface, select type-specific fields with inline
fragments. `__typename` comes from the value's actual type and dimensions. A
value whose GeoJSON type is not one of the seven simple-feature types
resolves to `null` rather than to a type the schema does not define.

**Empty geometries.** An empty point (`POINT EMPTY`) is `null`: its
coordinates do not exist, and `x`/`y` are non-null. The same applies to
empty points inside `points` and `geometries` lists. Other empty geometries
are objects with empty lists (`LINESTRING EMPTY` has `points: []`), and an
empty polygon has `exterior: null`. A `NOT NULL` point column holding
`POINT EMPTY` therefore violates its non-null field type.

**Cost.** The list fields of geometry objects (`points`, `interiors`, `lines`,
`polygons`, `geometries`) are read from a value already in hand, so they do
not add the ×10 list fan-out to the query cost estimate.

**Single statement.** Each selected spatial field compiles into one scalar
subquery inside the same statement. The value is encoded once with
`ST_AsGeoJSON` (after any `transform:`/`simplify:`), and every nested field
(`x`, `points`, `exterior`, `geometries`, …) is read from that jsonb with
JSON operators, so only the selected fields are built. Interface fields
dispatch on `__typename` with a `CASE` that has an arm only for the types
whose selections differ.

**Unchanged:** inputs (filters, `createX`, `updateX`) still take GeoJSON or
WKT, derived accessors (`locationCentroid`, …) still return `GeoJSON`, and
spatial arrays, composite-type attributes and function results stay on the
scalar. If a table or composite type already uses one of the generated names
(say a `geometry_points` table, whose type is `GeometryPoint`), that flavour
keeps the scalar and pdbq logs a warning.

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
      typed_geometry: false  # typed geometry objects instead of GeoJSON
```

Per-column control uses the usual [smart comments](smart-comments.md):

```sql
COMMENT ON COLUMN places.secret_location IS '@omit filter,order';
```

`@omit filter` removes the column's spatial filter and `@omit order` its
distance ordering. `@omit read` hides the column field and every derived
accessor (`<field>Wkt`, `<field>X`, …), and also its distance ordering,
which reads the value back by sorting against a point of the client's
choosing. A bare `@omit` hides all of it.

## Notes and limits

- **Injection safety.** Geometries, bounding-box corners and distances are all
  bind parameters; the compiled SQL text is a pure function of the query shape
  and of the input's form (GeoJSON, bare WKT or EWKT), never of its content.
  This is enforced by `FuzzSpatial` in `internal/compile`, the spatial twin of
  the `FuzzFilter` invariant.
- **Malformed input fails fast.** A value that is neither valid GeoJSON nor
  plausible WKT is rejected at compile time with a GraphQL error, rather than
  reaching Postgres as a type error mid-statement.
- **Not covered:** raster and topology types. Unless
  [typed geometry](#typed-geometry) is on, `geometry(TypeName, SRID)` type
  modifiers only decide which derived accessors are generated, and every
  spatial column is the same `GeoJSON` scalar whatever its declared subtype.
