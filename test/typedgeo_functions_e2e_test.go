package e2e

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

const geoFnSchema = "geofn_e2e"

func setupGeoFunctions(t *testing.T) {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+geoFnSchema+` CASCADE`)
		_ = conn.Close(ctx)
	})
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+geoFnSchema+` CASCADE`,
		`CREATE SCHEMA `+geoFnSchema,
		`SET search_path TO `+geoFnSchema+`, public`,
		`CREATE TYPE site AS (label text, spot geometry(Point, 4326), extent geography, trail geometry[])`,
		`CREATE TABLE spots (
			id int PRIMARY KEY,
			name text NOT NULL,
			loc geometry(Point, 4326),
			site site
		)`,
		`INSERT INTO spots VALUES
			(1, 'a', 'SRID=4326;POINT(1 2)', ROW('hq', 'SRID=4326;POINT(3 4)', 'SRID=4326;POLYGON((0 0, 1 0, 1 1, 0 0))',
				ARRAY['SRID=4326;POINT(5 6)'::geometry])::site),
			(2, 'b', NULL, NULL)`,
		`CREATE FUNCTION spots_centroid(s spots) RETURNS geometry
			LANGUAGE sql STABLE AS $$ SELECT s.loc $$`,
		`CREATE FUNCTION spots_outline(s spots) RETURNS SETOF geometry
			LANGUAGE sql STABLE AS $$ SELECT s.loc UNION ALL SELECT ST_Translate(s.loc, 10, 10) $$`,
		`CREATE FUNCTION make_pt(x float8, y float8) RETURNS geometry
			LANGUAGE sql STABLE AS $$ SELECT ST_SetSRID(ST_MakePoint(x, y), 3857) $$`,
		`CREATE FUNCTION make_pt_z(x float8, y float8, z float8) RETURNS geometry
			LANGUAGE sql STABLE AS $$ SELECT ST_SetSRID(ST_MakePoint(x, y, z), 4326) $$`,
		`CREATE FUNCTION grid_pts(n int) RETURNS SETOF geography
			LANGUAGE sql STABLE AS $$ SELECT ST_MakePoint(i, i)::geography FROM generate_series(1, n) AS i $$`,
		`CREATE FUNCTION snap_pt(pt geometry) RETURNS geometry
			LANGUAGE sql VOLATILE AS $$ SELECT ST_SnapToGrid(pt, 1) $$`,
		`CREATE FUNCTION pt_list() RETURNS geometry[]
			LANGUAGE sql STABLE AS $$ SELECT ARRAY[ST_MakePoint(1, 2)] $$`,
		`CREATE FUNCTION no_pt() RETURNS geometry
			LANGUAGE sql STABLE AS $$ SELECT NULL::geometry $$`,
		`CREATE FUNCTION spots_home(s spots) RETURNS site
			LANGUAGE sql STABLE AS $$ SELECT s.site $$`,
		`RESET search_path`,
	)
}

// TestTypedGeometryFunctions covers typed_geometry for geometry values that
// are not table columns: computed columns, query and mutation functions,
// composite attributes, and the same values with the setting off.
func TestTypedGeometryFunctions(t *testing.T) {
	setupGeoFunctions(t)
	server := func(typed bool) string {
		return testServer(t, func(c *config.Config) {
			c.Schema.Schemas = []string{geoFnSchema}
			c.Server.ExposeSchema = true
			c.Filters.IndexedOnly = false
			if typed {
				c.Plugins.Settings.Postgis.TypedGeometry = true
			}
		}).URL
	}
	typed, plain := server(true), server(false)

	check := func(t *testing.T, url, query, field, want string) {
		t.Helper()
		res := post(t, url, query, nil)
		requireNoErrors(t, res)
		if got, w := normalize(t, res.Data[field]), normalizeStr(t, want); got != w {
			t.Errorf("%s:\n got %s\nwant %s", field, got, w)
		}
	}

	t.Run("computed column", func(t *testing.T) {
		check(t, typed, `{ spotById(id: 1) { centroid { __typename srid ... on GeometryPoint { x y } } } }`, "spotById",
			`{"centroid": {"__typename": "GeometryPoint", "srid": 4326, "x": 1, "y": 2}}`)
		check(t, typed, `{ spotById(id: 2) { centroid { srid } } }`, "spotById", `{"centroid": null}`)
	})
	t.Run("setof computed column", func(t *testing.T) {
		check(t, typed, `{ spotById(id: 1) { outline { __typename ... on GeometryPoint { x y } } } }`, "spotById",
			`{"outline": [{"__typename": "GeometryPoint", "x": 1, "y": 2}, {"__typename": "GeometryPoint", "x": 11, "y": 12}]}`)
	})
	t.Run("scalar function", func(t *testing.T) {
		check(t, typed, `{ makePt(x: 1, y: 2) { __typename srid geojson ... on GeometryPoint { x y } } }`, "makePt",
			`{"__typename": "GeometryPoint", "srid": 3857, "geojson": {"type": "Point", "coordinates": [1, 2], "crs": {"type": "name", "properties": {"name": "EPSG:3857"}}}, "x": 1, "y": 2}`)
		check(t, typed, `{ makePtZ(x: 1, y: 2, z: 3) { __typename ... on GeometryPointZ { x y z } } }`, "makePtZ",
			`{"__typename": "GeometryPointZ", "x": 1, "y": 2, "z": 3}`)
		check(t, typed, `{ noPt { __typename } }`, "noPt", `null`)
	})
	t.Run("setof function", func(t *testing.T) {
		check(t, typed, `{ gridPts(n: 2) { __typename ... on GeographyPoint { longitude latitude } } }`, "gridPts",
			`[{"__typename": "GeographyPoint", "longitude": 1, "latitude": 1}, {"__typename": "GeographyPoint", "longitude": 2, "latitude": 2}]`)
	})
	t.Run("mutation function", func(t *testing.T) {
		check(t, typed, `mutation { snapPt(input: {pt: {type: "Point", coordinates: [1.2, 2.7]}}) {
			geoJson { __typename ... on GeometryPoint { x y } } } }`, "snapPt",
			`{"geoJson": {"__typename": "GeometryPoint", "x": 1, "y": 3}}`)
	})
	t.Run("composite attribute", func(t *testing.T) {
		const q = `{ spotById(id: 1) { site { label
			spot { __typename srid x y }
			extent { __typename ... on GeographyPolygon { exterior { points { longitude } } } }
			trail } } }`
		check(t, typed, q, "spotById", `{"site": {"label": "hq",
			"spot": {"__typename": "GeometryPoint", "srid": 4326, "x": 3, "y": 4},
			"extent": {"__typename": "GeographyPolygon", "exterior": {"points": [
				{"longitude": 0}, {"longitude": 1}, {"longitude": 1}, {"longitude": 0}]}},
			"trail": [{"type": "Point", "coordinates": [5, 6], "crs": {"type": "name", "properties": {"name": "EPSG:4326"}}}]}}`)
		check(t, typed, `{ spotById(id: 2) { site { spot { x } } } }`, "spotById", `{"site": null}`)
	})
	t.Run("composite returned by a function", func(t *testing.T) {
		check(t, typed, `{ spotById(id: 1) { home { spot { x y } } } }`, "spotById", `{"home": {"spot": {"x": 3, "y": 4}}}`)
	})
	t.Run("arrays stay GeoJSON", func(t *testing.T) {
		check(t, typed, `{ ptList }`, "ptList", `[{"type": "Point", "coordinates": [1, 2]}]`)
	})
	t.Run("setting off keeps GeoJSON", func(t *testing.T) {
		check(t, plain, `{ makePt(x: 1, y: 2) }`, "makePt", `{"type": "Point", "coordinates": [1, 2], "crs": {"type": "name", "properties": {"name": "EPSG:3857"}}}`)
		check(t, plain, `{ spotById(id: 1) { centroid site { spot } } }`, "spotById",
			`{"centroid": {"type": "Point", "coordinates": [1, 2]}, "site": {"spot": {"type": "Point", "coordinates": [3, 4]}}}`)
	})
}
