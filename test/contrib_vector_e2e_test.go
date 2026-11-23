package e2e

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

// setupExtSchema connects to the e2e database, installs exts (those not
// installed yet) into extSchema, a schema that needs quoting and is not on
// the search_path, and runs ddl with schema, extSchema and public on the
// search_path. Everything it creates is dropped when the test ends. It
// skips the test when one of exts is not available on the server.
func setupExtSchema(t *testing.T, schema, extSchema string, exts []string, ddl func(ext map[string]string) string) {
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
	t.Cleanup(func() { _ = conn.Close(ctx) })
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
			installed = append(installed, ext)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
		for _, ext := range installed {
			_, _ = conn.Exec(ctx, `DROP EXTENSION IF EXISTS `+ext+` CASCADE`)
		}
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS "`+extSchema+`" CASCADE`)
	})
	exec := func(sql string) {
		t.Helper()
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`CREATE SCHEMA IF NOT EXISTS "` + extSchema + `"`)
	for _, ext := range installed {
		exec(`CREATE EXTENSION ` + ext + ` SCHEMA "` + extSchema + `"`)
	}
	schemas := map[string]string{}
	for _, ext := range exts {
		var s string
		if err := conn.QueryRow(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE e.extname = $1`, ext).Scan(&s); err != nil {
			t.Fatal(err)
		}
		schemas[ext] = `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	exec(`CREATE SCHEMA ` + schema)
	exec(ddl(schemas))
}

const unaccentSchema = "unaccent_e2e"

// TestUnaccent runs the unaccent operators with the extension installed
// off the search_path, where the one-argument unaccent cannot resolve its
// dictionary, on an allowlisted column that also takes the pg_trgm
// operators.
func TestUnaccent(t *testing.T) {
	setupExtSchema(t, unaccentSchema, "unaccent e2e ext", []string{"unaccent", "pg_trgm"}, func(ext map[string]string) string {
		return `
CREATE TABLE ` + unaccentSchema + `.dishes (
	id int PRIMARY KEY,
	name text NOT NULL,
	note varchar
);
CREATE INDEX ON ` + unaccentSchema + `.dishes (note);
INSERT INTO ` + unaccentSchema + `.dishes VALUES
	(1, 'Crème Brûlée', 'Dessert'),
	(2, 'Café au lait', 'Boisson'),
	(3, 'Ångström 100%_', NULL),
	(4, 'Naïve Salade', 'Entrée'),
	(5, 'creme anglaise', NULL);
`
	})
	hs := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{unaccentSchema}
		cfg.Filters.AllowColumns = map[string][]string{unaccentSchema + ".dishes": {"name"}}
	})
	url := hs.URL

	for _, tc := range []struct {
		name, filter string
		want         []int
	}{
		{"equalTo", `{name: {unaccentEqualTo: "CREME BRULEE"}}`, []int{1}},
		{"like", `{name: {unaccentLike: "caf_ au%"}}`, []int{2}},
		{"like wildcards", `{name: {unaccentLike: "%e%"}}`, []int{1, 2, 4, 5}},
		{"includes", `{name: {unaccentIncludes: "CRÈME"}}`, []int{1, 5}},
		{"includes escapes wildcards", `{name: {unaccentIncludes: "0%_"}}`, []int{3}},
		{"includes literal underscore", `{name: {unaccentIncludes: "_"}}`, []int{3}},
		{"startsWith", `{name: {unaccentStartsWith: "angs"}}`, []int{3}},
		{"startsWith escapes", `{name: {unaccentStartsWith: "%"}}`, nil},
		{"composes with trgm", `{name: {unaccentIncludes: "naive", similarTo: "Naïve Salade"}}`, []int{4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := post(t, url, `{allDishes(orderBy: [ID_ASC], filter: `+tc.filter+`) { nodes { id } }}`, nil)
			requireNoErrors(t, res)
			if got := docIDs(t, res.Data["allDishes"]); !slices.Equal(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}

	t.Run("indexed_only leaves unallowlisted columns alone", func(t *testing.T) {
		res := post(t, url, `{__type(name: "DishFilter") { inputFields { name type { name } } }}`, nil)
		requireNoErrors(t, res)
		raw := string(res.Data["__type"])
		if !strings.Contains(raw, `"name":"name","type":{"name":"StringTrgmUnaccentFilter"}`) {
			t.Errorf("name filter: %s", raw)
		}
		if !strings.Contains(raw, `"name":"note","type":{"name":"StringFilter"}`) {
			t.Errorf("note filter: %s", raw)
		}
		res = post(t, url, `{allDishes(filter: {note: {unaccentEqualTo: "entree"}}) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("unaccent operator accepted on a column the policy does not admit")
		}
	})
}

const vectorSchema = "vector_e2e"

// setupVector creates the vector_e2e schema with pgvector installed off the
// search_path, skipping the test when the server lacks pgvector. Rows 1-4
// are ordered differently by each metric from [1, 1, 0]; row 5 has no
// embedding.
func setupVector(t *testing.T) {
	t.Helper()
	setupExtSchema(t, vectorSchema, "vector e2e ext", []string{"vector"}, func(ext map[string]string) string {
		v := ext["vector"]
		return `
CREATE TABLE ` + vectorSchema + `.items (
	id int PRIMARY KEY GENERATED BY DEFAULT AS IDENTITY,
	embedding ` + v + `.vector(3),
	half ` + v + `.halfvec(3),
	sparse ` + v + `.sparsevec(3)
);
CREATE INDEX ON ` + vectorSchema + `.items USING hnsw (embedding ` + v + `.vector_l2_ops);
CREATE INDEX ON ` + vectorSchema + `.items USING hnsw (embedding ` + v + `.vector_ip_ops);
CREATE INDEX ON ` + vectorSchema + `.items USING hnsw (embedding ` + v + `.vector_cosine_ops);
CREATE INDEX ON ` + vectorSchema + `.items USING hnsw (embedding ` + v + `.vector_l1_ops);
INSERT INTO ` + vectorSchema + `.items (id, embedding, half, sparse) VALUES
	(1, '[2.5,1,0]', '[0.5,1,2]', '{1:1}/3'),
	(2, '[2,2,0]', NULL, '{2:1}/3'),
	(3, '[0,0,3]', NULL, '{1:1,2:1}/3'),
	(4, '[6,5,0]', NULL, NULL),
	(5, NULL, NULL, NULL);
SELECT setval(pg_get_serial_sequence('` + vectorSchema + `.items', 'id'), 100);
CREATE FUNCTION ` + vectorSchema + `.scale_vector(v ` + v + `.vector, by float8) RETURNS ` + v + `.vector IMMUTABLE LANGUAGE sql
	AS $$ SELECT array_agg(x * by ORDER BY o)::real[]::` + v + `.vector FROM unnest(v::real[]) WITH ORDINALITY AS u(x, o) $$;
`
	})
}

// TestVector covers pgvector columns end to end: list input and output,
// nearest-first ordering per metric against near:, keyset paging, and
// the indexed-only policy for distance ordering.
func TestVector(t *testing.T) {
	setupVector(t)
	hs := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{vectorSchema}
		cfg.Filters.AllowColumns = map[string][]string{vectorSchema + ".items": {"sparse"}}
	})
	url := hs.URL

	t.Run("create and read back", func(t *testing.T) {
		res := post(t, url, `mutation {
			createItem(input: {item: {embedding: [1, 2.5, -3], half: [0.5, 1, 2], sparse: "{1:1.5,3:2}/3"}}) {
				item { id embedding half sparse }
			}
		}`, nil)
		requireNoErrors(t, res)
		var payload struct {
			Item struct {
				ID        int       `json:"id"`
				Embedding []float64 `json:"embedding"`
				Half      []float64 `json:"half"`
				Sparse    string    `json:"sparse"`
			} `json:"item"`
		}
		mustUnmarshal(t, res.Data["createItem"], &payload)
		if !slices.Equal(payload.Item.Embedding, []float64{1, 2.5, -3}) || !slices.Equal(payload.Item.Half, []float64{0.5, 1, 2}) || payload.Item.Sparse != "{1:1.5,3:2}/3" {
			t.Errorf("created item: %+v", payload.Item)
		}
		res = post(t, url, `mutation ($id: Int!) {
			updateItemById(input: {id: $id, itemPatch: {embedding: [0, 0, 1]}}) { item { embedding } }
			deleteItemById(input: {id: $id}) { item { id } }
		}`, map[string]any{"id": payload.Item.ID})
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["updateItemById"]); got != `{"item":{"embedding":[0,0,1]}}` {
			t.Errorf("update: %s", got)
		}

		res = post(t, url, `mutation { createItem(input: {item: {embedding: [1, 2]}}) { item { id } } }`, nil)
		if len(res.Errors) == 0 {
			t.Error("a vector of the wrong dimension was accepted")
		}
	})

	t.Run("nearest first per metric", func(t *testing.T) {
		// An HNSW or IVFFlat index scan skips rows whose vector is NULL, so
		// whether row 5 is returned depends on the plan; leave it out.
		res := post(t, url, `{
			l2: allItems(filter: {embedding: {isNull: false}}, near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_L2_DISTANCE_ASC]) { nodes { id } }
			ip: allItems(filter: {embedding: {isNull: false}}, near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_NEGATIVE_INNER_PRODUCT_ASC]) { nodes { id } }
			cosine: allItems(filter: {embedding: {isNull: false}}, near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_COSINE_DISTANCE_ASC]) { nodes { id } }
			l1: allItems(filter: {embedding: {isNull: false}}, near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_L1_DISTANCE_ASC]) { nodes { id } }
			sparse: allItems(filter: {sparse: {isNull: false}}, near: {sparse: "{1:1}/3"}, orderBy: [SPARSE_L2_DISTANCE_ASC]) { nodes { id } }
			l2Desc: allItems(near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_L2_DISTANCE_DESC]) { nodes { id } }
			unset: allItems(filter: {embedding: {isNull: true}}) { nodes { id } }
		}`, nil)
		requireNoErrors(t, res)
		for alias, want := range map[string][]int{
			"l2":     {2, 1, 3, 4},
			"ip":     {4, 2, 1, 3},
			"cosine": {2, 4, 1, 3},
			"l1":     {1, 2, 3, 4},
			"sparse": {1, 3, 2},
			"l2Desc": {4, 3, 1, 2, 5},
			"unset":  {5},
		} {
			if got := docIDs(t, res.Data[alias]); !slices.Equal(got, want) {
				t.Errorf("%s: got %v want %v", alias, got, want)
			}
		}
	})

	t.Run("the near: reference is required", func(t *testing.T) {
		res := post(t, url, `{allItems(orderBy: [EMBEDDING_L2_DISTANCE_ASC]) { nodes { id } }}`, nil)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "orderBy EMBEDDING_L2_DISTANCE_ASC requires near: {embedding: …}") {
			t.Errorf("ordering without a reference: %+v", res.Errors)
		}
	})

	t.Run("keyset paging", func(t *testing.T) {
		var seen []int
		after := any(nil)
		for range 4 {
			res := post(t, url, `query ($after: Cursor) {
				allItems(near: {embedding: [1, 1, 0]}, orderBy: [EMBEDDING_COSINE_DISTANCE_DESC], first: 2, after: $after) {
					edges { cursor node { id } }
					pageInfo { hasNextPage }
				}
			}`, map[string]any{"after": after})
			requireNoErrors(t, res)
			var conn struct {
				Edges []struct {
					Cursor string `json:"cursor"`
					Node   struct {
						ID int `json:"id"`
					} `json:"node"`
				} `json:"edges"`
				PageInfo struct {
					HasNextPage bool `json:"hasNextPage"`
				} `json:"pageInfo"`
			}
			mustUnmarshal(t, res.Data["allItems"], &conn)
			for _, e := range conn.Edges {
				seen = append(seen, e.Node.ID)
				after = e.Cursor
			}
			if !conn.PageInfo.HasNextPage {
				break
			}
		}
		if want := []int{3, 1, 4, 2, 5}; !slices.Equal(seen, want) {
			t.Errorf("pages: got %v want %v", seen, want)
		}
	})

	t.Run("function arguments and results", func(t *testing.T) {
		res := post(t, url, `{scaleVector(v: [1, 2, 3], by: 2)}`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["scaleVector"]); got != `[2,4,6]` {
			t.Errorf("scaleVector: %s", got)
		}
	})

	t.Run("indexed_only offers indexed and allowlisted columns", func(t *testing.T) {
		res := post(t, url, `{__type(name: "ItemsOrderBy") { enumValues { name } }}`, nil)
		requireNoErrors(t, res)
		raw := string(res.Data["__type"])
		for _, want := range []string{"EMBEDDING_L1_DISTANCE_ASC", "EMBEDDING_COSINE_DISTANCE_DESC", "SPARSE_NEGATIVE_INNER_PRODUCT_ASC"} {
			if !strings.Contains(raw, want) {
				t.Errorf("ItemsOrderBy lacks %s", want)
			}
		}
		if strings.Contains(raw, "HALF_") || strings.Contains(raw, `"EMBEDDING_ASC"`) {
			t.Errorf("unexpected orderings: %s", raw)
		}
	})
}

// TestVectorPluginDisabled checks that the pgvector type mapping, reads and
// writes are core: disabling the contrib plugin removes only the ordering.
func TestVectorPluginDisabled(t *testing.T) {
	setupVector(t)
	hs := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{vectorSchema}
		cfg.Plugins.Disabled = append(cfg.Plugins.Disabled, "contrib")
	})
	res := post(t, hs.URL, `{itemById(id: 1) { embedding half sparse }}`, nil)
	requireNoErrors(t, res)
	if got, want := normalize(t, res.Data["itemById"]), normalizeStr(t, `{"embedding":[2.5,1,0],"half":[0.5,1,2],"sparse":"{1:1}/3"}`); got != want {
		t.Errorf("got %s want %s", got, want)
	}
	res = post(t, hs.URL, `{allItems(near: {embedding: [1, 1, 0]}) { nodes { id } }}`, nil)
	if len(res.Errors) == 0 {
		t.Error("near: accepted with the contrib plugin disabled")
	}
}
