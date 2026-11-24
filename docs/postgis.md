# PostGIS

pdbq maps PostGIS `geometry` and `geography` columns to a `GeoJSON` scalar and
generates the spatial query surface on top of them: bounding-box search,
DE-9IM predicates, radius search, derived accessors, reprojection and distance
ordering — all compiled into the same **single SQL statement** as the rest of
the query.

Raster columns (`postgis_raster`) get metadata, pixel value, statistics and
intersects surface, never their raw bytes (see [Rasters](#rasters)).

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
  createPlace(input: {place: {
    name: "Trafalgar Square"
    location: {type: "Point", coordinates: [-0.1281, 51.508]}
  }}) { place { id } }
}

mutation {
  createPlace(input: {place: {name: "Elsewhere", location: "SRID=4326;POINT(1 2)"}}) {
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
| EWKT `SRID=4326;POINT(1 2)` | rejected as `invalid_argument` in filters and `near:` (send it in 3857, or as GeoJSON to have it reprojected); in a mutation input PostgreSQL rejects the mismatch |
| GeoJSON (4326, or its `crs`) | reprojected by the `ST_Transform(..., 3857)` branch (a no-op when already 3857) |
| bounding box without `srid` | built in 3857 |

An unconstrained `geometry` column (SRID 0) gets the plain rules above.
Geography columns are always 4326, so nothing is reprojected for them.
Function arguments have no type modifier, so they get the plain rules too. A
geometry that PostgreSQL rejects (mixed SRIDs in one operation, an invalid
shape) is reported to clients as `invalid geometry` (`invalid input value`
with `errors.detail: strict`).

## Filtering

Every spatial column takes a `GeoJSONFilter` operand set. Operators marked
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

## Projection arguments

Spatial column fields take four projection arguments, applied before the
value is encoded as GeoJSON. Aliases let you fetch several representations
at once:

| Argument | SQL | Notes |
|---|---|---|
| `buffer: Float` | `ST_Buffer(col, $n::float8)` | the area within that distance: metres on `geography` (the value stays geography), SRID units on `geometry`; negative distances shrink polygons and erase points and lines |
| `simplify: Float` | `ST_SimplifyPreserveTopology(col, $n)` | vertex reduction that never yields an invalid geometry (geography is cast to geometry first) |
| `transform: Int` | `ST_Transform(col, <srid>)` | reproject; the SRID is an integer literal of the query shape |
| `precision: Int` | `ST_AsGeoJSON(col, $n::int4)` | decimal digits of every coordinate, 0 to 15; PostGIS's default is 9, and 6 (about 10 cm on lon/lat data) shrinks payloads substantially |

They apply in that order: buffer, then simplify, then transform, then the
GeoJSON encoding with the given precision.

```graphql
{
  allPlaces {
    nodes {
      webMercator: location(transform: 3857)
      coarse:      location(simplify: 0.01)
      compact:     location(precision: 6)
      around:      area(buffer: 250, precision: 6)
    }
  }
}
```

The buffer distance, the tolerance and the precision are bind parameters, so
the SQL text depends only on which arguments a field has (a `null` argument
counts as absent). An out-of-range precision, a non-integer precision or a
non-finite buffer is a GraphQL error. With [typed geometry](#typed-geometry)
the arguments apply the same way, and `precision:` rounds every coordinate
field of the object (`x`, `y`, `points`, `geojson`, ...), which is read back
out of the one rounded encoding. `ST_Buffer` builds a new polygon around
every vertex of the value, so the cost estimator charges each buffered field
5 per row on top of the column read (pdbcore's `postgis.BufferCost`); the
other arguments cost nothing extra.

## Distance ordering

Each spatial column adds `<COLUMN>_DISTANCE_ASC` / `_DESC` to the table's
`orderBy` enum and a field to the table's `<Type>NearInput`, the type of the
`near:` argument of every list taking that `orderBy`: one reference
geometry per spatial column, named after the column (GeoJSON or WKT; the
argument is shared with the [contrib references](contrib.md#references-the-near-argument)).
Ordering compiles to the KNN operator `<->`, which a GiST index answers
directly — this is the fast "nearest N" path:

```graphql
{
  allPlaces(
    near: {location: "SRID=4326;POINT(-0.1281 51.5080)"}
    orderBy: [LOCATION_DISTANCE_ASC]
    first: 10
  ) { nodes { name } }
}
```

The column's geometry in `near:` is required for these values: without it the
query is an error (`orderBy LOCATION_DISTANCE_ASC requires near: {location:
…}`) rather than a result that looks sorted but is not.

### Selecting the distance

Each readable spatial column also gets a `<field>Distance: Float` field: the
distance of the row's geometry from the column's `near:` geometry in the
list it was read through, so "places near me, with the distance" is one query:

```graphql
{
  allPlaces(
    near: {location: "SRID=4326;POINT(-0.1281 51.5080)", area: "SRID=4326;POINT(-0.1281 51.5080)"}
    orderBy: [LOCATION_DISTANCE_ASC]
    first: 10
  ) {
    nodes {
      name
      locationDistance                     # SRID units (degrees for 4326)
      metres: locationDistance(useSpheroid: true)
      areaDistance                         # geography: metres
    }
  }
}
```

It compiles to `ST_Distance(column, <near>)` in the same statement, with the
reference bound as its own parameter. Units follow the column: metres on a
`geography` column (on the spheroid), SRID units on a `geometry` column.
Geometry columns take `useSpheroid: true` to measure in metres instead
(`ST_Distance(column::geography, <near>::geography)`, which needs a lon/lat
SRID such as 4326). Like the `useSpheroid` flag of `dwithin`, the argument
picks the SQL form, so it is part of the query shape.

Selecting the field through a list whose `near:` holds no geometry for the
column is an error (`field locationDistance requires near: {location: …}`);
rows not read through a list that takes `near:` (single-row lookups,
forward relations, mutation payloads) read `null`. Backward relation
connections take `near:` themselves.
It is a projection of the returned rows, so it does not need a GiST index
and is offered whether or not distance ordering is (`@omit order` and
`filters.indexed_only` leave it alone); `@omit read` removes it, and
`ordering: false` removes it with `near:`. The cost estimator prices it like
a column.

## Typed geometry

> **Experimental.** Typed geometry is outside the 1.0 compatibility promise and may change in a minor release. See [Compatibility and versioning](compatibility.md#experimental-features).

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

**Function results and composite attributes.** The same typing applies to
every other non-array geometry or geography output, as in
`@graphile/postgis`:

| Output | Field type |
|---|---|
| computed column (`places_centroid(p places) RETURNS geometry`) | `GeometryInterface` |
| query function (`RETURNS geometry`, `RETURNS SETOF geography`) | `GeometryInterface`, `[GeographyInterface!]!` |
| mutation function's result field | `GeometryInterface` |
| `OUT`/`TABLE` parameter of a record-returning function | by flavour, interface |
| composite attribute `spot geometry(Point, 4326)` | `GeometryPoint` |
| composite attribute `extent geography` | `GeographyInterface` |

A function declares `geometry` without a type modifier, so its result is
unconstrained and dispatches on `__typename` like an unconstrained column
(select subtype fields with inline fragments). A composite attribute keeps
its typmod, so it resolves to the same concrete type a table column would.
Names are unchanged: a volatile function returning `geometry` still has the
result field `geoJson`. Curve and surface subtypes, and NULL results, behave
as for columns (`null` for a NULL value).

The `transform:` and `simplify:` arguments exist only on table column fields.
Function results, computed columns and composite attributes take none, so
they are encoded as the value comes out of the database; reproject or
simplify inside the function when needed.

**Unchanged:** inputs (filters, `createX`, `updateX`, function arguments)
still take GeoJSON or WKT, derived accessors (`locationCentroid`, …) still
return `GeoJSON`, and spatial arrays (`geometry[]` columns, attributes and
function results) stay on the scalar. If a table or composite type already
uses one of the generated names (say a `geometry_points` table, whose type
is `GeometryPoint`), that flavour keeps the scalar and pdbq logs a warning.

## Rasters

> **Experimental.** Raster support is outside the 1.0 compatibility promise and may change in a minor release. See [Compatibility and versioning](compatibility.md#experimental-features).

`postgis_raster` columns get a read surface of their own. **The raster value
itself is never returned**: a raster is a pixel grid, often megabytes, so
the column has no output field. To serve the bytes, opt in with a computed
column, for example
`CREATE FUNCTION tiles_png(t tiles) RETURNS text STABLE LANGUAGE sql AS
$$ SELECT encode(ST_AsPNG(t.rast), 'base64') $$` (or `ST_AsGDALRaster`,
`ST_AsHexWKB`). Writes keep the raster's text form: `createTile(input:
{tile: {rast: "0100..."}})` takes hex WKB as a `String`. Raster columns get no
generic comparison filter, `condition:` field, orderBy or `distinctOn:` value
and no aggregates (raster has no btree operator class).

| Field | Returns | PostGIS |
|---|---|---|
| `<field>Width` / `<field>Height` | `Int` | `ST_Width` / `ST_Height` (pixels) |
| `<field>NumBands` | `Int` | `ST_NumBands` |
| `<field>Srid` | `Int` | `ST_SRID` |
| `<field>PixelWidth` / `<field>PixelHeight` | `Float` | `ST_PixelWidth` / `ST_PixelHeight` (SRID units) |
| `<field>Envelope` | `GeoJSON` | `ST_Envelope` |
| `<field>Value(point: GeoJSON!, band: Int = 1)` | `Float` | `ST_Value` of the band at the point |
| `<field>SummaryStats(band: Int = 1)` | `RasterSummaryStats` | `ST_SummaryStats` of the band (NODATA excluded) |

```graphql
{
  allTiles(filter: {rast: {intersects: "SRID=4326;POLYGON((0 0, 1 0, 1 1, 0 1, 0 0))"}}) {
    nodes {
      id rastWidth rastSrid rastEnvelope
      rastValue(point: {type: "Point", coordinates: [0.5, 0.5]})
      rastSummaryStats(band: 1) { count mean min max }
    }
  }
}
```

The metadata accessors read the raster header only. `<field>Value` samples
the pixel at the point, reprojected to the raster's SRID when both carry a
different one (so a GeoJSON point, WGS 84, samples a raster in any
projection); it is `null` for NODATA, a point outside the raster or a
missing band. `RasterSummaryStats` is `{count: Int, sum, mean, stddev, min,
max: Float}`, `null` for a NULL raster or a missing band; it reads every
pixel of the band. The point, the band and every other value are bind
parameters, and a band below 1 is a client error.

The filter is `RasterFilter { isNull, intersects: GeoJSON }`:
`intersects` is `ST_Intersects(rast, geometry)`, which tests the raster's
convex hull. **The geometry must be in the raster's SRID**: it is not
reprojected, so give it as EWKT (`SRID=3857;POLYGON(...)`) unless the raster
is in WGS 84. A row whose bounding box overlaps a geometry in another SRID
raises an error, reported as a client error; other rows just do not match.
Index it with an expression index on the convex hull:

```sql
CREATE INDEX tiles_rast_hull_idx ON tiles USING gist (ST_ConvexHull(rast));
```

An expression index leads no column in the catalog, so under
`filters.indexed_only` the raster filter is offered only on columns listed
in `filters.allow_columns`. The accessors, value and statistics follow the
`derived` setting, the filter the `filters` setting. `@omit read` on a raster
column removes every one of these fields and the filter, `@omit filter` the
filter.

**Cost.** The metadata accessors cost a column each. The cost estimator
charges `<field>Value` 10 and `<field>SummaryStats` 100 per row (pdbcore's
`RasterValueCost` and `RasterSummaryStatsCost`), multiplied by the page like
any field, so a large page of statistics hits `limits.max_cost`. Filter
predicates are not priced (only relation-aggregate filters, which run a
subquery per row, are); the raster `intersects` filter, which tests the
convex hull like a geometry predicate, is left unpriced like the spatial
filters.

Function results of type `raster` keep the text form: a function returning
a raster is the author's choice to return it. Composite attributes of type
`raster`, like the raster columns of rows held in other values (`TileRow`),
have no output field; composite inputs keep the text form.

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
Disabling it removes filters, accessors, projection arguments, distance fields and distance
ordering; reading and writing spatial columns as GeoJSON is core behaviour and
keeps working.

```yaml
plugins:
  settings:
    postgis:
      filters: true      # GeoJSONFilter on spatial columns, RasterFilter
      derived: true      # <field>Area, <field>Centroid, ..., raster accessors
      ordering: true     # <COLUMN>_DISTANCE_ASC/_DESC, <field>Distance + near:
      projection: true   # transform:/simplify: field arguments
      typed_geometry: false  # typed geometry objects instead of GeoJSON
```

Per-column control uses the usual [smart comments](smart-comments.md):

```sql
COMMENT ON COLUMN places.secret_location IS '@omit filter,order';
```

`@omit filter` removes the column's spatial filter and `@omit order` its
distance ordering. `@omit read` hides the column field and every derived
accessor (`<field>Wkt`, `<field>X`, …), and also its `<field>Distance` field
and distance ordering, which read the value back by measuring it against a
point of the client's choosing. A bare `@omit` hides all of it.

## Notes and limits

- **Injection safety.** Geometries, bounding-box corners, distances and the
  buffer, simplify and precision projection arguments are all bind parameters; the compiled SQL text is a pure function of the query shape
  and of the input's form (GeoJSON, bare WKT or EWKT), never of its content.
  This is enforced by `FuzzSpatial` in `internal/compile`, the spatial twin of
  the `FuzzFilter` invariant.
- **Malformed input fails fast.** A value that is neither valid GeoJSON nor
  plausible WKT is rejected at compile time with a GraphQL error, rather than
  reaching Postgres as a type error mid-statement.
- **Not covered:** topology types, and raster output other than through a
  computed column (see [Rasters](#rasters)). Unless
  [typed geometry](#typed-geometry) is on, `geometry(TypeName, SRID)` type
  modifiers only decide which derived accessors are generated, and every
  spatial column is the same `GeoJSON` scalar whatever its declared subtype.
