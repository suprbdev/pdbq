package e2e

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

const cubeSchema = "cube_e2e"

// requireExtensions installs the extensions exts that are not installed yet
// into the default schema (earthdistance before 1.2 calls the cube functions
// unqualified, so both must be on the search_path), dropping them again when
// the test ends. It skips the test when one is not available.
func requireExtensions(t *testing.T, exts ...string) {
	t.Helper()
	conn := testConn(t)
	ctx := context.Background()
	var installed []string
	for _, ext := range exts {
		var available, exists bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = $1),
			EXISTS (SELECT 1 FROM pg_extension WHERE extname = $1)`, ext).Scan(&available, &exists); err != nil {
			t.Fatal(err)
		}
		if !available {
			t.Skipf("extension %s is not available on this server", ext)
		}
		if !exists {
			execAll(t, conn, `CREATE EXTENSION IF NOT EXISTS `+ext)
			installed = append(installed, ext)
		}
	}
	t.Cleanup(func() {
		for _, ext := range slices.Backward(installed) {
			_, _ = conn.Exec(ctx, `DROP EXTENSION IF EXISTS `+ext+` CASCADE`)
		}
	})
}

// setupCube creates cube_e2e.sites: cube and earth columns led by GiST
// indexes (pos, spot), an unindexed earth column (home) and a point column
// (pt, (lon, lat)), holding five cities and a row without locations.
func setupCube(t *testing.T) {
	t.Helper()
	requireExtensions(t, "cube", "earthdistance")
	conn := testConn(t)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+cubeSchema+` CASCADE`) })
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+cubeSchema+` CASCADE`,
		`CREATE SCHEMA `+cubeSchema,
		`CREATE TABLE `+cubeSchema+`.sites (id int PRIMARY KEY, name text NOT NULL, pos cube, spot earth, home earth, pt point)`,
		`CREATE INDEX sites_pos_idx ON `+cubeSchema+`.sites USING gist (pos)`,
		`CREATE INDEX sites_spot_idx ON `+cubeSchema+`.sites USING gist (spot)`,
		`INSERT INTO `+cubeSchema+`.sites
			SELECT id, name, cube(ARRAY[x, y]), ll_to_earth(lat, lon), ll_to_earth(lat, lon), point(lon, lat)
			FROM (VALUES (1, 'London', 51.5074, -0.1278, 0, 0), (2, 'Paris', 48.8566, 2.3522, 1, 1),
				(3, 'Berlin', 52.52, 13.405, 2, 0), (4, 'Madrid', 40.4168, -3.7038, 5, 5),
				(5, 'New York', 40.7128, -74.006, 9, 1)) AS v(id, name, lat, lon, x, y)`,
		`INSERT INTO `+cubeSchema+`.sites (id, name) VALUES (6, 'Nowhere')`,
	)
}

// cubeServer serves cube_e2e under the indexed-only policy with pt
// allowlisted.
func cubeServer(t *testing.T) string {
	t.Helper()
	setupCube(t)
	return testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{cubeSchema}
		cfg.Filters.IndexedOnly = true
		cfg.Filters.AllowColumns = map[string][]string{cubeSchema + ".sites": {"pt"}}
	}).URL
}

// siteNodes decodes the nodes of an allSites connection.
func siteNodes(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var conn struct {
		Nodes []map[string]any `json:"nodes"`
	}
	mustUnmarshal(t, raw, &conn)
	return conn.Nodes
}

func nodeIDs(nodes []map[string]any) []int {
	out := make([]int, len(nodes))
	for i, n := range nodes {
		f, _ := n["id"].(float64)
		out[i] = int(f)
	}
	return out
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestCubeEarthdistance(t *testing.T) {
	url := cubeServer(t)
	query := func(t *testing.T, q string) []map[string]any {
		t.Helper()
		res := post(t, url, `{ allSites`+q+` }`, nil)
		requireNoErrors(t, res)
		return siteNodes(t, res.Data["allSites"])
	}

	t.Run("read", func(t *testing.T) {
		nodes := query(t, `(orderBy: [ID_ASC], filter: {id: {in: [2, 6]}}) { nodes { id pos spot { __typename lat lon } at: home { y: lat } } }`)
		if len(nodes) != 2 {
			t.Fatalf("nodes: %v", nodes)
		}
		paris := nodes[0]
		if got, _ := json.Marshal(paris["pos"]); string(got) != "[1,1]" {
			t.Errorf("pos = %s", got)
		}
		spot, _ := paris["spot"].(map[string]any)
		lat, _ := spot["lat"].(float64)
		lon, _ := spot["lon"].(float64)
		if spot["__typename"] != "LatLon" || !near(lat, 48.8566, 1e-9) || !near(lon, 2.3522, 1e-9) {
			t.Errorf("spot = %v", spot)
		}
		if at, _ := paris["at"].(map[string]any); len(at) != 1 || !near(at["y"].(float64), 48.8566, 1e-9) {
			t.Errorf("aliased home = %v", paris["at"])
		}
		if nodes[1]["pos"] != nil || nodes[1]["spot"] != nil {
			t.Errorf("NULL locations = %v", nodes[1])
		}
	})

	t.Run("write", func(t *testing.T) {
		res := post(t, url, `mutation {
			createSite(input: {site: {id: 7, name: "Box", pos: [[0, 0], [2, 3]], spot: {lat: -33.8688, lon: 151.2093}, pt: "(151.2093,-33.8688)"}}) {
				site { id pos spot { lat lon } }
			}
		}`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["createSite"]); !strings.Contains(got, `"pos":[[0,0],[2,3]]`) || !strings.Contains(got, `"lat":-33.868`) {
			t.Errorf("created = %s", got)
		}
		res = post(t, url, `mutation { updateSiteById(input: {id: 7, sitePatch: {pos: "(4, 5)", spot: null}}) { site { pos spot { lat } } } }`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["updateSiteById"]); got != `{"site":{"pos":[4,5],"spot":null}}` {
			t.Errorf("updated = %s", got)
		}
		res = post(t, url, `mutation { createSite(input: {site: {id: 8, name: "x", spot: {lat: 91, lon: 0}}}) { site { id } } }`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "lat must be between") {
			t.Errorf("out-of-range latitude: %+v", res.Errors)
		}
		res = post(t, url, `mutation { createSite(input: {site: {id: 8, name: "x", pos: "(1, x)"}}) { site { id } } }`, nil)
		if len(res.Errors) == 0 {
			t.Error("malformed cube text accepted")
		}
		post(t, url, `mutation { deleteSiteById(input: {id: 7}) { deletedSiteNodeId } }`, nil)
	})

	t.Run("filters", func(t *testing.T) {
		for _, tc := range []struct {
			name, filter string
			want         []int
		}{
			{"contains", `{pos: {containedBy: [[0, 0], [2, 1]]}}`, []int{1, 2, 3}},
			{"contains box", `{pos: {overlaps: [[4, 4], [10, 10]]}}`, []int{4}},
			{"equalTo", `{pos: {equalTo: [1, 1]}}`, []int{2}},
			{"l2 threshold", `{pos: {l2DistanceLessThan: {cube: [0, 0], distance: 2.1}}}`, []int{1, 2, 3}},
			{"chebyshev threshold", `{pos: {chebyshevDistanceLessThan: {cube: [0, 0], distance: 2}}}`, []int{1, 2}},
			{"l1 threshold", `{pos: {l1DistanceLessThan: {cube: "(1, 1)", distance: 2.5}}}`, []int{1, 2, 3}},
			{"earth radius", `{spot: {earthDistanceLessThan: {lat: 51.5074, lon: -0.1278, distance: 400000}}}`, []int{1, 2}},
			{"earth radius tight", `{spot: {earthDistanceLessThan: {lat: 51.5074, lon: -0.1278, distance: 340000}}}`, []int{1}},
			{"earth isNull", `{spot: {isNull: true}}`, []int{6}},
			{"point miles", `{pt: {earthDistanceMilesLessThan: {lat: 51.5074, lon: -0.1278, distance: 250}}}`, []int{1, 2}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := nodeIDs(query(t, `(orderBy: [ID_ASC], filter: `+tc.filter+`) { nodes { id } }`)); !slices.Equal(got, tc.want) {
					t.Errorf("got %v want %v", got, tc.want)
				}
			})
		}
	})

	t.Run("orderings and scores", func(t *testing.T) {
		nodes := query(t, `(nearPoint: {spot: {lat: 51.5074, lon: -0.1278}, home: {lat: 48.8566, lon: 2.3522}}, orderBy: [SPOT_EARTH_DISTANCE_ASC],
			filter: {spot: {isNull: false}}) { nodes { id spotEarthDistance homeEarthDistance } }`)
		if got := nodeIDs(nodes); !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
			t.Errorf("nearest to London: %v", got)
		}
		if d, _ := nodes[1]["spotEarthDistance"].(float64); !near(d, 343500, 2000) {
			t.Errorf("London-Paris = %v m", nodes[1]["spotEarthDistance"])
		}
		if d, _ := nodes[1]["homeEarthDistance"].(float64); !near(d, 0, 1e-3) {
			t.Errorf("Paris from Paris = %v m", nodes[1]["homeEarthDistance"])
		}
		desc := nodeIDs(query(t, `(nearPoint: {spot: {lat: 51.5074, lon: -0.1278}}, orderBy: [SPOT_EARTH_DISTANCE_DESC]) { nodes { id } }`))
		if !slices.Equal(desc, []int{5, 4, 3, 2, 1, 6}) {
			t.Errorf("farthest from London (NULL last): %v", desc)
		}

		nodes = query(t, `(nearVector: {pos: [0, 0]}, orderBy: [POS_CHEBYSHEV_DISTANCE_ASC, ID_ASC], filter: {pos: {isNull: false}}) {
			nodes { id posL2Distance posL1Distance posChebyshevDistance } }`)
		if got := nodeIDs(nodes); !slices.Equal(got, []int{1, 2, 3, 4, 5}) {
			t.Errorf("cube chebyshev order: %v", got)
		}
		if l2, l1, ch := nodes[3]["posL2Distance"], nodes[3]["posL1Distance"], nodes[3]["posChebyshevDistance"]; !near(l2.(float64), math.Sqrt(50), 1e-9) || l1 != 10.0 || ch != 5.0 {
			t.Errorf("Madrid cube distances = %v %v %v", l2, l1, ch)
		}

		nodes = query(t, `(nearPoint: {pt: {lat: 40.7128, lon: -74.006}}, orderBy: [PT_EARTH_DISTANCE_MILES_ASC], first: 2) { nodes { id ptEarthDistanceMiles } }`)
		if got := nodeIDs(nodes); !slices.Equal(got, []int{5, 1}) {
			t.Errorf("nearest to New York: %v", got)
		}
		if d, _ := nodes[1]["ptEarthDistanceMiles"].(float64); !near(d, 3460, 30) {
			t.Errorf("New York-London = %v mi", nodes[1]["ptEarthDistanceMiles"])
		}

		res := post(t, url, `{ allSites(orderBy: [SPOT_EARTH_DISTANCE_ASC]) { nodes { id } } }`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "requires a nearPoint: reference") {
			t.Errorf("ordering without a reference: %+v", res.Errors)
		}
		if nodes := query(t, `(orderBy: [ID_ASC], first: 1) { nodes { spotEarthDistance posL2Distance } }`); nodes[0]["spotEarthDistance"] != nil || nodes[0]["posL2Distance"] != nil {
			t.Errorf("scores without references = %v", nodes[0])
		}
	})

	t.Run("keyset pages over earth distance", func(t *testing.T) {
		var ids []int
		after := ""
		for range 5 {
			args := `first: 2, nearPoint: {spot: {lat: 52.52, lon: 13.405}}, orderBy: [SPOT_EARTH_DISTANCE_ASC]`
			if after != "" {
				args += `, after: "` + after + `"`
			}
			res := post(t, url, `{ allSites(`+args+`) { edges { cursor node { id } } pageInfo { hasNextPage endCursor } } }`, nil)
			requireNoErrors(t, res)
			var page struct {
				Edges []struct {
					Node struct{ ID int } `json:"node"`
				} `json:"edges"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			}
			mustUnmarshal(t, res.Data["allSites"], &page)
			for _, e := range page.Edges {
				ids = append(ids, e.Node.ID)
			}
			if !page.PageInfo.HasNextPage {
				break
			}
			after = page.PageInfo.EndCursor
		}
		if want := []int{3, 2, 1, 4, 5, 6}; !slices.Equal(ids, want) {
			t.Errorf("pages from Berlin = %v, want %v", ids, want)
		}
	})

	t.Run("indexed_only policy", func(t *testing.T) {
		res := post(t, url, `{ f: __type(name: "SiteFilter") { inputFields { name type { name } } }
			s: __type(name: "Site") { fields { name } } o: __type(name: "SitesOrderBy") { enumValues { name } } }`, nil)
		requireNoErrors(t, res)
		raw := string(res.Data["f"]) + string(res.Data["s"]) + string(res.Data["o"])
		for _, want := range []string{
			`"name":"pos","type":{"name":"CubeDistanceFilterOps"}`, `"name":"spot","type":{"name":"LatLonDistanceFilterOps"}`,
			`"name":"pt","type":{"name":"PointDistanceFilterOps"}`, `"homeEarthDistance"`, `"SPOT_EARTH_DISTANCE_ASC"`,
		} {
			if !strings.Contains(raw, want) {
				t.Errorf("schema lacks %s", want)
			}
		}
		for _, reject := range []string{`"name":"home","type"`, `"HOME_EARTH_DISTANCE_ASC"`, `"POS_ASC"`, `"SPOT_ASC"`} {
			if strings.Contains(raw, reject) {
				t.Errorf("schema has %s", reject)
			}
		}
	})
}

// TestCubeEarthKNNIndex checks that nearest-first earth and cube orderings
// compile to a KNN scan of the column's GiST index. The ordering carries the
// primary key as a tiebreaker, which PostgreSQL before 17 cannot add on top
// of an index ordering operator (no incremental sort), so it sorts there.
func TestCubeEarthKNNIndex(t *testing.T) {
	setupCube(t)
	conn := testConn(t)
	var version int
	if err := conn.QueryRow(context.Background(), `SELECT current_setting('server_version_num')::int`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 170000 {
		t.Skip("PostgreSQL before 17 sorts a KNN ordering with a tiebreaker instead of scanning the index")
	}
	execAll(t, conn,
		`INSERT INTO `+cubeSchema+`.sites SELECT 100 + i, 'g' || i, cube(ARRAY[i % 97, i % 89]),
			ll_to_earth((i % 170) - 85, (i % 350) - 175), NULL, NULL FROM generate_series(1, 5000) AS i`,
		`ANALYZE `+cubeSchema+`.sites`,
		`SET enable_sort = off`,
	)
	cfg := config.DefaultConfig()
	cfg.Database.URL = os.Getenv("PDBQ_TEST_DATABASE_URL")
	cfg.Schema.Schemas = []string{cubeSchema}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	for q, index := range map[string]string{
		`{ allSites(first: 5, nearPoint: {spot: {lat: 51.5, lon: -0.12}}, orderBy: [SPOT_EARTH_DISTANCE_ASC]) { nodes { id spotEarthDistance } } }`: "sites_spot_idx",
		`{ allSites(first: 5, nearVector: {pos: [3, 4]}, orderBy: [POS_L2_DISTANCE_ASC]) { nodes { id } } }`:                                        "sites_pos_idx",
	} {
		stmts, err := app.CompileQuery(context.Background(), q, nil, "")
		if err != nil || len(stmts) != 1 {
			t.Fatalf("compile: %v %v", stmts, err)
		}
		rows, err := conn.Query(context.Background(), "EXPLAIN (COSTS OFF) "+stmts[0].SQL, stmts[0].Args...)
		if err != nil {
			t.Fatal(err)
		}
		var lines []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		plan := strings.Join(lines, "\n")
		if !strings.Contains(plan, "Index Scan using "+index) || !strings.Contains(plan, "Order By:") {
			t.Errorf("%s: no KNN scan of %s:\n%s", q, index, plan)
		}
	}
}

const rasterSchema = "raster_e2e"

// setupRaster creates raster_e2e.tiles: tile 1 a 4x4 WGS 84 raster over
// (0, 0)-(4, 4) with band 1 at 2 except 7 in its upper-left pixel and band 2
// at 3, tile 2 a 2x2 web-mercator raster at 9, tile 3 without a raster, and
// a GiST index on the convex hull serving intersects.
func setupRaster(t *testing.T) {
	t.Helper()
	requireExtensions(t, "postgis_raster")
	conn := testConn(t)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP SCHEMA IF EXISTS `+rasterSchema+` CASCADE`) })
	execAll(t, conn,
		`DROP SCHEMA IF EXISTS `+rasterSchema+` CASCADE`,
		`CREATE SCHEMA `+rasterSchema,
		`CREATE TABLE `+rasterSchema+`.tiles (id int PRIMARY KEY, rast raster, secret raster)`,
		`COMMENT ON COLUMN `+rasterSchema+`.tiles.secret IS '@omit read'`,
		`CREATE INDEX tiles_hull_idx ON `+rasterSchema+`.tiles USING gist (ST_ConvexHull(rast))`,
		`INSERT INTO `+rasterSchema+`.tiles VALUES
			(1, ST_AddBand(ST_SetValue(ST_AddBand(ST_MakeEmptyRaster(4, 4, 0, 4, 1, -1, 0, 0, 4326), '32BF'::text, 2, NULL), 1, 1, 1, 7), '8BUI'::text, 3, NULL), NULL),
			(2, ST_AddBand(ST_MakeEmptyRaster(2, 2, 1000000, 1000000, 100, -100, 0, 0, 3857), '8BUI'::text, 9, NULL), NULL),
			(3, NULL, NULL)`,
	)
}

func TestRaster(t *testing.T) {
	setupRaster(t)
	url := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{rasterSchema}
		cfg.Filters.IndexedOnly = true
		cfg.Filters.AllowColumns = map[string][]string{rasterSchema + ".tiles": {"rast"}}
	}).URL
	tiles := func(t *testing.T, args, sel string) string {
		t.Helper()
		res := post(t, url, `{ allTiles(orderBy: [ID_ASC]`+args+`) { nodes { `+sel+` } } }`, nil)
		requireNoErrors(t, res)
		return normalize(t, res.Data["allTiles"])
	}

	t.Run("accessors", func(t *testing.T) {
		got := tiles(t, `, filter: {id: {in: [1, 3]}}`, `id rastWidth rastHeight rastNumBands rastSrid rastPixelWidth rastPixelHeight rastEnvelope`)
		want := `{"nodes":[{"id":1,"rastEnvelope":{"coordinates":[[[0,4],[4,4],[4,0],[0,0],[0,4]]],"type":"Polygon"},"rastHeight":4,"rastNumBands":2,` +
			`"rastPixelHeight":1,"rastPixelWidth":1,"rastSrid":4326,"rastWidth":4},` +
			`{"id":3,"rastEnvelope":null,"rastHeight":null,"rastNumBands":null,"rastPixelHeight":null,"rastPixelWidth":null,"rastSrid":null,"rastWidth":null}]}`
		if got != normalizeStr(t, want) {
			t.Errorf("accessors = %s", got)
		}
	})

	t.Run("pixel values", func(t *testing.T) {
		got := tiles(t, ``, `id
			corner: rastValue(point: {type: "Point", coordinates: [0.5, 3.5]})
			inner: rastValue(point: "POINT(2.5 1.5)")
			band2: rastValue(point: "POINT(2.5 1.5)", band: 2)
			missingBand: rastValue(point: "POINT(2.5 1.5)", band: 5)
			outside: rastValue(point: "POINT(50 50)")
			mercator: rastValue(point: "SRID=3857;POINT(1000050 999950)")`)
		want := `{"nodes":[{"id":1,"corner":7,"inner":2,"band2":3,"missingBand":null,"outside":null,"mercator":null},` +
			`{"id":2,"corner":null,"inner":null,"band2":null,"missingBand":null,"outside":null,"mercator":9},` +
			`{"id":3,"corner":null,"inner":null,"band2":null,"missingBand":null,"outside":null,"mercator":null}]}`
		if got != normalizeStr(t, want) {
			t.Errorf("values = %s", got)
		}
	})

	t.Run("summary stats", func(t *testing.T) {
		got := tiles(t, `, filter: {id: {in: [1, 3]}}`, `id rastSummaryStats { __typename count sum mean min max } b2: rastSummaryStats(band: 2) { stddev } none: rastSummaryStats(band: 3) { count }`)
		want := `{"nodes":[{"id":1,"rastSummaryStats":{"__typename":"RasterSummaryStats","count":16,"sum":37,"mean":2.3125,"min":2,"max":7},"b2":{"stddev":0},"none":null},` +
			`{"id":3,"rastSummaryStats":null,"b2":null,"none":null}]}`
		if got != normalizeStr(t, want) {
			t.Errorf("summary stats = %s", got)
		}
	})

	t.Run("intersects", func(t *testing.T) {
		got := tiles(t, `, filter: {rast: {intersects: "SRID=4326;POLYGON((0 0, 1 0, 1 1, 0 1, 0 0))"}}`, `id`)
		if got != `{"nodes":[{"id":1}]}` {
			t.Errorf("intersects = %s", got)
		}
		got = tiles(t, `, filter: {rast: {intersects: {type: "Point", coordinates: [100, 100]}}}`, `id`)
		if got != `{"nodes":[]}` {
			t.Errorf("intersects elsewhere = %s", got)
		}
		got = tiles(t, `, filter: {rast: {isNull: true}}`, `id`)
		if got != `{"nodes":[{"id":3}]}` {
			t.Errorf("isNull = %s", got)
		}
		res := post(t, url, `{ allTiles(filter: {rast: {intersects: "SRID=3857;POINT(1 1)"}}) { nodes { id } } }`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "SRID") {
			t.Errorf("SRID mismatch: %+v", res.Errors)
		}
	})

	t.Run("raw value is not readable", func(t *testing.T) {
		for _, q := range []string{
			`{ allTiles { nodes { rast } } }`,
			`{ allTiles { nodes { secretWidth } } }`,
			`{ allTiles(filter: {secret: {isNull: true}}) { nodes { id } } }`,
			`{ allTiles(orderBy: [RAST_ASC]) { nodes { id } } }`,
			`{ allTiles(condition: {rast: "x"}) { nodes { id } } }`,
		} {
			if res := post(t, url, q, nil); len(res.Errors) == 0 {
				t.Errorf("accepted %s", q)
			}
		}
	})

	t.Run("write hex WKB", func(t *testing.T) {
		var hex string
		if err := testConn(t).QueryRow(context.Background(), `SELECT rast::text FROM `+rasterSchema+`.tiles WHERE id = 2`).Scan(&hex); err != nil {
			t.Fatal(err)
		}
		res := post(t, url, `mutation ($r: String) { createTile(input: {tile: {id: 4, rast: $r}}) { tile { id rastSrid rastWidth } } }`, map[string]any{"r": hex})
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["createTile"]); got != `{"tile":{"id":4,"rastSrid":3857,"rastWidth":2}}` {
			t.Errorf("created = %s", got)
		}
	})

	t.Run("indexed_only without allowlist", func(t *testing.T) {
		strict := testServer(t, func(cfg *config.Config) {
			cfg.Schema.Schemas = []string{rasterSchema}
			cfg.Filters.IndexedOnly = true
		}).URL
		res := post(t, strict, `{ allTiles(filter: {rast: {isNull: true}}) { nodes { id } } }`, nil)
		if len(res.Errors) == 0 {
			t.Error("raster filter offered on an unallowlisted column under indexed_only")
		}
		res = post(t, strict, `{ allTiles(orderBy: [ID_ASC]) { nodes { id rastWidth } } }`, nil)
		requireNoErrors(t, res)
	})
}

// TestCubeRasterContract checks that columns a schema contract hides get
// none of the cube, earthdistance and raster surface.
func TestCubeRasterContract(t *testing.T) {
	requireExtensions(t, "cube", "earthdistance", "postgis_raster")
	_, base := contractsServerWith(t, false, nil,
		`ALTER TABLE `+contractsSchema+`.customers ADD COLUMN pos cube, ADD COLUMN spot earth, ADD COLUMN rast raster`,
		`CREATE INDEX ON `+contractsSchema+`.customers USING gist (pos)`,
		`CREATE INDEX ON `+contractsSchema+`.customers USING gist (spot)`,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.pos IS '@behavior -contract:public'`,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.spot IS '@behavior -contract:public'`,
		`COMMENT ON COLUMN `+contractsSchema+`.customers.rast IS '@behavior -contract:public'`,
	)
	surface := []string{"posL2Distance", "spotEarthDistance", "rastWidth", "rastSummaryStats", "CustomerNearPointInput",
		"CustomerNearVectorInput", "POS_L2_DISTANCE_ASC", "SPOT_EARTH_DISTANCE_ASC", "spot: LatLon", "pos: Cube", "rast: RasterFilterOps"}
	internal := getSDL(t, base)
	for _, s := range surface {
		if !strings.Contains(internal, s) {
			t.Errorf("internal schema lacks %s", s)
		}
	}
	public := getSDL(t, base+"/public")
	for _, s := range surface {
		if strings.Contains(public, s) {
			t.Errorf("public schema keeps %s", s)
		}
	}
}
