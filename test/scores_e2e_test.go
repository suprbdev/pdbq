package e2e

import (
	"context"
	"math"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

// scoreRows decodes a connection's nodes into id -> field -> value, null
// values as NaN.
func scoreRows(t *testing.T, raw []byte) map[int]map[string]float64 {
	t.Helper()
	var conn struct {
		Nodes []map[string]*float64 `json:"nodes"`
	}
	mustUnmarshal(t, raw, &conn)
	out := map[int]map[string]float64{}
	for _, n := range conn.Nodes {
		row := map[string]float64{}
		for k, v := range n {
			row[k] = math.NaN()
			if v != nil {
				row[k] = *v
			}
		}
		out[int(row["id"])] = row
	}
	return out
}

// sqlScores runs q (id, value...) and returns id -> values.
func sqlScores(t *testing.T, q string, args ...any) map[int][]float64 {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PDBQ_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	out := map[int][]float64{}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		id := int(vals[0].(int32))
		for _, v := range vals[1:] {
			f := math.NaN()
			switch x := v.(type) {
			case float64:
				f = x
			case float32:
				f = float64(x)
			}
			out[id] = append(out[id], f)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func approxEqual(a, b float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.IsNaN(a) && math.IsNaN(b)
	}
	return math.Abs(a-b) <= 1e-6*math.Max(1, math.Abs(b))
}

// TestDistanceScore selects <field>Distance against near: on the fixture's
// places and compares it with PostGIS: SRID units on the geometry column,
// metres on the geography column and with useSpheroid.
func TestDistanceScore(t *testing.T) {
	hs := testServer(t, nil)
	const ref = "SRID=4326;POINT(-0.1 51.5)"
	res := post(t, hs.URL, `query ($ref: GeoJSON!) {
		allPlaces(near: {geometry: $ref}, orderBy: [LOCATION_DISTANCE_ASC]) {
			nodes { id locationDistance metres: locationDistance(useSpheroid: true) areaDistance }
		}
		none: allPlaces(first: 1) { nodes { id locationDistance } }
	}`, map[string]any{"ref": ref})
	requireNoErrors(t, res)
	got := scoreRows(t, res.Data["allPlaces"])
	want := sqlScores(t, `SELECT id, ST_Distance(location, ST_GeomFromEWKT($1)),
		ST_Distance(location::geography, ST_GeomFromEWKT($1)::geography),
		ST_Distance(area, ST_GeomFromEWKT($1)::geography) FROM places`, ref)
	if len(got) == 0 || len(got) != len(want) {
		t.Fatalf("rows: got %d want %d", len(got), len(want))
	}
	for id, w := range want {
		g := got[id]
		for i, f := range []string{"locationDistance", "metres", "areaDistance"} {
			if !approxEqual(g[f], w[i]) {
				t.Errorf("place %d %s = %v, want %v", id, f, g[f], w[i])
			}
		}
	}
	for _, row := range scoreRows(t, res.Data["none"]) {
		if !math.IsNaN(row["locationDistance"]) {
			t.Errorf("distance without near: = %v", row["locationDistance"])
		}
	}
}

const scoresSchema = "scores_e2e"

// TestTrigramAndVectorScores selects the pg_trgm similarity and pgvector
// distance fields and applies the distance-threshold filters, comparing
// every value with the extension functions. It skips without pgvector.
func TestTrigramAndVectorScores(t *testing.T) {
	setupExtSchema(t, scoresSchema, "scores e2e ext", []string{"pg_trgm", "vector"}, func(ext map[string]string) string {
		v, g := ext["vector"], ext["pg_trgm"]
		return `
CREATE TABLE ` + scoresSchema + `.items (
	id int PRIMARY KEY,
	name text NOT NULL,
	embedding ` + v + `.vector(3),
	code bit(3)
);
CREATE INDEX ON ` + scoresSchema + `.items USING gist (name ` + g + `.gist_trgm_ops);
CREATE INDEX ON ` + scoresSchema + `.items USING hnsw (embedding ` + v + `.vector_cosine_ops);
CREATE INDEX ON ` + scoresSchema + `.items USING hnsw (code ` + v + `.bit_hamming_ops);
INSERT INTO ` + scoresSchema + `.items VALUES
	(1, 'postgres database', '[1,0,0]', '110'),
	(2, 'postgis spatial', '[0,1,0]', '011'),
	(3, 'graph queries', '[1,1,0]', '000'),
	(4, 'unrelated', NULL, NULL);
`
	})
	hs := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{scoresSchema}
		cfg.Filters.AllowColumns = map[string][]string{scoresSchema + ".items": {"embedding", "code"}}
	})
	url := hs.URL
	ext := func(name string) string {
		var s string
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, os.Getenv("PDBQ_TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close(ctx) }()
		if err := conn.QueryRow(ctx, `SELECT quote_ident(n.nspname) FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1`, name).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	g, v := ext("pg_trgm"), ext("vector")

	t.Run("scores", func(t *testing.T) {
		res := post(t, url, `{
			allItems(
				similarTo: {name: "postgres"}
				nearVector: {embedding: [1, 0.5, 0], code: "100"}
				orderBy: [NAME_SIMILARITY_DESC]
			) {
				nodes {
					id nameSimilarity nameWordSimilarity nameStrictWordSimilarity
					embeddingCosineDistance embeddingL2Distance embeddingNegativeInnerProduct embeddingL1Distance
					codeHammingDistance codeJaccardDistance
				}
			}
		}`, nil)
		requireNoErrors(t, res)
		got := scoreRows(t, res.Data["allItems"])
		want := sqlScores(t, `SELECT id,
			`+g+`.similarity(name, 'postgres'), `+g+`.word_similarity('postgres', name), `+g+`.strict_word_similarity('postgres', name),
			embedding OPERATOR(`+v+`.<=>) '[1,0.5,0]'::`+v+`.vector, embedding OPERATOR(`+v+`.<->) '[1,0.5,0]'::`+v+`.vector,
			embedding OPERATOR(`+v+`.<#>) '[1,0.5,0]'::`+v+`.vector, embedding OPERATOR(`+v+`.<+>) '[1,0.5,0]'::`+v+`.vector,
			code OPERATOR(`+v+`.<~>) '100'::varbit, code OPERATOR(`+v+`.<%>) '100'::varbit
			FROM `+scoresSchema+`.items`)
		fields := []string{
			"nameSimilarity", "nameWordSimilarity", "nameStrictWordSimilarity",
			"embeddingCosineDistance", "embeddingL2Distance", "embeddingNegativeInnerProduct", "embeddingL1Distance",
			"codeHammingDistance", "codeJaccardDistance",
		}
		if len(got) != 4 {
			t.Fatalf("rows: %v", got)
		}
		for id, w := range want {
			for i, f := range fields {
				if !approxEqual(got[id][f], w[i]) {
					t.Errorf("item %d %s = %v, want %v", id, f, got[id][f], w[i])
				}
			}
		}
	})

	t.Run("null without the reference", func(t *testing.T) {
		res := post(t, url, `{allItems(similarTo: {name: "x"}) { nodes { id nameSimilarity embeddingL2Distance codeHammingDistance } }}`, nil)
		requireNoErrors(t, res)
		for id, row := range scoreRows(t, res.Data["allItems"]) {
			if math.IsNaN(row["nameSimilarity"]) || !math.IsNaN(row["embeddingL2Distance"]) || !math.IsNaN(row["codeHammingDistance"]) {
				t.Errorf("item %d: %v", id, row)
			}
		}
	})

	t.Run("threshold filters", func(t *testing.T) {
		res := post(t, url, `{
			cosine: allItems(
				filter: {embedding: {cosineDistanceLessThan: {vector: [1, 0.5, 0], distance: 0.1}}}
				nearVector: {embedding: [1, 0.5, 0]}
				orderBy: [EMBEDDING_COSINE_DISTANCE_ASC]
			) { nodes { id } }
			l2: allItems(filter: {embedding: {l2DistanceLessThan: {vector: [1, 0.5, 0], distance: 1}}}, orderBy: [ID_ASC]) { nodes { id } }
			hamming: allItems(filter: {code: {hammingDistanceLessThan: {vector: "100", distance: 2}}}, orderBy: [ID_ASC]) { nodes { id } }
			hammingOrder: allItems(filter: {code: {isNull: false}}, nearVector: {code: "001"}, orderBy: [CODE_HAMMING_DISTANCE_ASC, ID_ASC]) { nodes { id } }
		}`, nil)
		requireNoErrors(t, res)
		for alias, want := range map[string][]int{
			"cosine":       {3},
			"l2":           {1, 3},
			"hamming":      {1, 3},
			"hammingOrder": {2, 3, 1},
		} {
			if got := docIDs(t, res.Data[alias]); !slices.Equal(got, want) {
				t.Errorf("%s: got %v want %v", alias, got, want)
			}
		}
		res = post(t, url, `{allItems(filter: {embedding: {l2DistanceLessThan: {vector: [1, 0], distance: 1}}}) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "dimensions") {
			t.Errorf("wrong dimensions: %+v", res.Errors)
		}
		res = post(t, url, `{allItems(filter: {code: {hammingDistanceLessThan: {vector: "102", distance: 1}}}) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("a malformed bit string was accepted")
		}
	})

	t.Run("indexed_only keeps thresholds to allowlisted columns", func(t *testing.T) {
		strict := testServer(t, func(cfg *config.Config) { cfg.Schema.Schemas = []string{scoresSchema} })
		res := post(t, strict.URL, `{allItems(filter: {embedding: {l2DistanceLessThan: {vector: [1, 0, 0], distance: 1}}}) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("threshold filter accepted on a column the policy does not admit")
		}
		res = post(t, strict.URL, `{allItems(nearVector: {embedding: [1, 0, 0]}, orderBy: [ID_ASC]) { nodes { id embeddingL2Distance } }}`, nil)
		requireNoErrors(t, res)
	})
}
