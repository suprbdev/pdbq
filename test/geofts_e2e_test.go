package e2e

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestSpatialPrecisionAndBuffer selects the fixture's places through the
// precision: and buffer: projection arguments and compares them with
// PostGIS.
func TestSpatialPrecisionAndBuffer(t *testing.T) {
	hs := testServer(t, nil)
	res := post(t, hs.URL, `query ($d: Int) {
		allPlaces(condition: {id: 1}) {
			nodes {
				compact: location(precision: 2)
				fromVar: location(precision: $d)
				full: location
				buffered: location(buffer: 0.001, precision: 4)
				geoBuffered: area(buffer: 100)
				mercator: area(buffer: 100, simplify: 1, transform: 3857, precision: 0)
			}
		}
	}`, map[string]any{"d": 0})
	requireNoErrors(t, res)
	var conn struct {
		Nodes []map[string]json.RawMessage `json:"nodes"`
	}
	mustUnmarshal(t, res.Data["allPlaces"], &conn)
	if len(conn.Nodes) != 1 {
		t.Fatalf("nodes: %s", res.Data["allPlaces"])
	}
	n := conn.Nodes[0]
	want := map[string]string{}
	ctx := context.Background()
	db, err := pgx.Connect(ctx, os.Getenv("PDBQ_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close(ctx) }()
	var compact, fromVar, buffered, geoBuffered, mercator string
	if err := db.QueryRow(ctx, `SELECT ST_AsGeoJSON(location, 2), ST_AsGeoJSON(location, 0),
		ST_AsGeoJSON(ST_Buffer(location, 0.001), 4), ST_AsGeoJSON(ST_Buffer(area, 100)),
		ST_AsGeoJSON(ST_Transform(ST_SimplifyPreserveTopology(ST_Buffer(area, 100)::geometry, 1), 3857), 0)
		FROM places WHERE id = 1`).Scan(&compact, &fromVar, &buffered, &geoBuffered, &mercator); err != nil {
		t.Fatal(err)
	}
	want["compact"], want["fromVar"], want["buffered"], want["geoBuffered"], want["mercator"] = compact, fromVar, buffered, geoBuffered, mercator
	for k, w := range want {
		var g, e any
		mustUnmarshal(t, n[k], &g)
		mustUnmarshal(t, []byte(w), &e)
		gj, _ := json.Marshal(g)
		ej, _ := json.Marshal(e)
		if string(gj) != string(ej) {
			t.Errorf("%s = %s, want %s", k, gj, ej)
		}
	}
	if !strings.Contains(string(n["compact"]), "[-0.13,51.51]") {
		t.Errorf("compact = %s", n["compact"])
	}
	if strings.Contains(string(n["mercator"]), ".") {
		t.Errorf("precision 0 kept decimals: %s", n["mercator"])
	}
	if len(n["full"]) <= len(n["compact"]) {
		t.Errorf("full %s not longer than compact %s", n["full"], n["compact"])
	}

	for _, q := range []string{
		`{allPlaces { nodes { location(precision: 16) } }}`,
		`{allPlaces { nodes { location(precision: -1) } }}`,
	} {
		if res := post(t, hs.URL, q, nil); len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "precision") {
			t.Errorf("%s: %+v", q, res.Errors)
		}
	}
}

// TestTypedGeometryPrecision rounds every coordinate field of a typed
// geometry object.
func TestTypedGeometryPrecision(t *testing.T) {
	hs := testServer(t, func(c *config.Config) {
		c.Plugins.Settings.Postgis.TypedGeometry = true
	})
	res := post(t, hs.URL, `{allPlaces(condition: {id: 1}) { nodes { location(precision: 1) { x y } } }}`, nil)
	requireNoErrors(t, res)
	var conn struct {
		Nodes []struct {
			Location struct{ X, Y float64 } `json:"location"`
		} `json:"nodes"`
	}
	mustUnmarshal(t, res.Data["allPlaces"], &conn)
	if len(conn.Nodes) != 1 || conn.Nodes[0].Location.X != -0.1 || conn.Nodes[0].Location.Y != 51.5 {
		t.Errorf("rounded point = %s", res.Data["allPlaces"])
	}
}

const headlineSchema = "headline_e2e"

// TestFullTextHeadline highlights the document columns of a table with a
// generated tsvector column against the list's matches* filter and compares
// the snippets with ts_headline. json/jsonb columns take no full-text
// operators in pdbq.
func TestFullTextHeadline(t *testing.T) {
	setupExtSchema(t, headlineSchema, "headline e2e ext", nil, func(map[string]string) string {
		return `
CREATE TABLE ` + headlineSchema + `.articles (
	id int PRIMARY KEY,
	title text NOT NULL,
	body text,
	meta jsonb,
	search tsvector GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || coalesce(body, ''))) STORED
);
CREATE INDEX ON ` + headlineSchema + `.articles USING gin (search);
CREATE INDEX ON ` + headlineSchema + `.articles USING gin (meta);
INSERT INTO ` + headlineSchema + `.articles VALUES
	(1, 'Foxes', 'The quick brown fox jumps over the lazy dog. Foxes are clever animals that live in many places.', '{"tag": "fox"}'),
	(2, 'Dogs', 'A dog is a loyal companion.', NULL),
	(3, 'Fox news', NULL, NULL);
`
	})
	hs := testServer(t, func(cfg *config.Config) { cfg.Schema.Schemas = []string{headlineSchema} })
	headlines := func(q string, vars map[string]any) map[int]*string {
		t.Helper()
		res := post(t, hs.URL, q, vars)
		requireNoErrors(t, res)
		var conn struct {
			Nodes []struct {
				ID int     `json:"id"`
				H  *string `json:"h"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allArticles"], &conn)
		out := map[int]*string{}
		for _, n := range conn.Nodes {
			out[n.ID] = n.H
		}
		return out
	}
	sqlHeadlines := func(q string, args ...any) map[int]*string {
		t.Helper()
		ctx := context.Background()
		db, err := pgx.Connect(ctx, os.Getenv("PDBQ_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close(ctx) }()
		rows, err := db.Query(ctx, q, args...)
		if err != nil {
			t.Fatal(err)
		}
		out := map[int]*string{}
		for rows.Next() {
			var id int
			var h *string
			if err := rows.Scan(&id, &h); err != nil {
				t.Fatal(err)
			}
			out[id] = h
		}
		return out
	}
	same := func(name string, got, want map[int]*string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: got %d rows, want %d", name, len(got), len(want))
		}
		for id, w := range want {
			g := got[id]
			if (g == nil) != (w == nil) || g != nil && *g != *w {
				t.Errorf("%s row %d: got %v, want %v", name, id, deref(g), deref(w))
			}
		}
	}

	t.Run("against the filter", func(t *testing.T) {
		got := headlines(`query ($q: String!) {
			allArticles(filter: {search: {matchesWebsearch: $q, config: "english"}}, orderBy: [SEARCH_RANK_DESC]) {
				nodes { id h: searchHeadline(document: BODY, options: {maxWords: 8, minWords: 3, startSel: "<mark>", stopSel: "</mark>"}) }
			}
		}`, map[string]any{"q": "fox"})
		want := sqlHeadlines(`SELECT id, ts_headline('english', body, websearch_to_tsquery('english', 'fox'), 'MaxWords=8, MinWords=3, StartSel="<mark>", StopSel="</mark>"')
			FROM ` + headlineSchema + `.articles WHERE search @@ websearch_to_tsquery('english', 'fox')`)
		same("body", got, want)
		if h := got[1]; h == nil || !strings.Contains(*h, "<mark>fox</mark>") {
			t.Errorf("article 1 headline = %v", deref(h))
		}
		if got[3] != nil {
			t.Errorf("a NULL document has headline %v", deref(got[3]))
		}
		got = headlines(`{allArticles(filter: {search: {matches: "fox"}}) { nodes { id h: searchHeadline(document: TITLE, options: {maxFragments: 2, fragmentDelimiter: " | "}) } }}`, nil)
		want = sqlHeadlines(`SELECT id, ts_headline(title, to_tsquery('fox'), 'MaxFragments=2, FragmentDelimiter=" | "') FROM ` + headlineSchema + `.articles WHERE search @@ to_tsquery('fox')`)
		same("title", got, want)
	})

	t.Run("null without a match", func(t *testing.T) {
		got := headlines(`{allArticles(filter: {or: [{search: {matches: "fox"}}, {id: {equalTo: 2}}]}) { nodes { id h: searchHeadline(document: BODY) } }}`, nil)
		for id, h := range got {
			if h != nil {
				t.Errorf("row %d has headline %q outside a conjunctive match", id, *h)
			}
		}
	})

	t.Run("invalid options", func(t *testing.T) {
		for _, opts := range []string{`{maxWords: 500}`, `{maxWords: 10}`, `{startSel: "a\"b"}`, `{maxFragments: 11}`} {
			res := post(t, hs.URL, `{allArticles(filter: {search: {matches: "fox"}}) { nodes { searchHeadline(document: BODY, options: `+opts+`) } }}`, nil)
			if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "headline option") {
				t.Errorf("%s: %+v", opts, res.Errors)
			}
		}
	})

	t.Run("no json search", func(t *testing.T) {
		res := post(t, hs.URL, `{allArticles(filter: {meta: {matches: "fox"}}) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("a matches filter on a jsonb column was accepted")
		}
		res = post(t, hs.URL, `{allArticles(filter: {search: {matches: "fox"}}) { nodes { searchHeadline(document: META) } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("a jsonb column is a headline document")
		}
	})

	t.Run("evaluated for the page rows only", func(t *testing.T) {
		ctx := context.Background()
		db, err := pgx.Connect(ctx, os.Getenv("PDBQ_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = db.Close(ctx) }()
		if _, err := db.Exec(ctx, `INSERT INTO `+headlineSchema+`.articles (id, title, body)
			SELECT i, 'fox ' || i, repeat('the quick brown fox ', 50) FROM generate_series(10, 2000) AS i`); err != nil {
			t.Fatal(err)
		}
		res := post(t, hs.URL, `{allArticles(filter: {search: {matches: "fox"}}, orderBy: [SEARCH_RANK_DESC], first: 3) { nodes { id h: searchHeadline(document: BODY) } }}`, nil)
		requireNoErrors(t, res)
		var plan string
		if err := db.QueryRow(ctx, `EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON)
			SELECT json_build_object('nodes', coalesce(json_agg(s.ndata ORDER BY s.rn) FILTER (WHERE s.rn <= 3), '[]'::json)) FROM (
				SELECT json_build_object('h', ts_headline(t.body, to_tsquery($1::text), $2::text)) AS ndata,
					row_number() OVER (ORDER BY ts_rank(t.search, to_tsquery($1::text)) DESC NULLS LAST, t.id) AS rn
				FROM `+headlineSchema+`.articles AS t WHERE t.search @@ to_tsquery($1::text)
				ORDER BY ts_rank(t.search, to_tsquery($1::text)) DESC NULLS LAST, t.id LIMIT 4) AS s GROUP BY ()`, "fox", "").Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if rows := headlineRows(t, plan); rows < 0 || rows > 4 {
			t.Errorf("ts_headline evaluated on %v rows, want at most the page + 1:\n%s", rows, plan)
		}
	})
}

// headlineRows returns the actual row count of the plan node whose output
// computes ts_headline, or -1 when none does.
func headlineRows(t *testing.T, plan string) float64 {
	t.Helper()
	var root []struct {
		Plan map[string]any `json:"Plan"`
	}
	mustUnmarshal(t, []byte(plan), &root)
	var walk func(n map[string]any) float64
	walk = func(n map[string]any) float64 {
		for _, c := range asNodes(n["Plans"]) {
			if r := walk(c); r >= 0 {
				return r
			}
		}
		out, _ := n["Output"].([]any)
		for _, o := range out {
			if s, _ := o.(string); strings.HasPrefix(s, "json_build_object('h', ts_headline(") {
				r, _ := n["Actual Rows"].(float64)
				return math.Round(r)
			}
		}
		return -1
	}
	return walk(root[0].Plan)
}

func asNodes(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}
