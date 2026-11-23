package e2e

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

const valuesSchema = "contrib_values_e2e"

// setupValues creates contrib_values_e2e.items with hstore, ltree,
// fuzzystrmatch and intarray installed off the search_path. Row 5's name is
// longer than fuzzystrmatch's 255-character limit; row 4 is all NULL.
func setupValues(t *testing.T) {
	t.Helper()
	setupExtSchema(t, valuesSchema, "contrib values e2e ext", []string{"hstore", "ltree", "fuzzystrmatch", "intarray"}, func(ext map[string]string) string {
		return `
CREATE TABLE ` + valuesSchema + `.items (
	id int PRIMARY KEY,
	name text,
	attrs ` + ext["hstore"] + `.hstore,
	path ` + ext["ltree"] + `.ltree,
	paths ` + ext["ltree"] + `.ltree[],
	tag_ids int4[]
);
CREATE INDEX ON ` + valuesSchema + `.items USING gin (tag_ids ` + ext["intarray"] + `.gin__int_ops);
INSERT INTO ` + valuesSchema + `.items VALUES
	(1, 'Robert', 'color=>red, size=>L', 'top.science.astronomy', '{top.a.b,top.a.c}', '{1,2,3}'),
	(2, 'Rupert', 'color=>blue, size=>NULL', 'top.science', '{top.x}', '{2,4}'),
	(3, 'Smyth', 'size=>S', 'top', '{}', '{}'),
	(4, NULL, NULL, NULL, NULL, NULL),
	(5, repeat('a', 300), 'color=>green', 'top.hobbies.amateurs_astronomy', NULL, '{5}');
`
	})
}

// itemIDs extracts the ids of an allItems connection's nodes.
func itemIDs(t *testing.T, raw json.RawMessage) []int { return docIDs(t, raw) }

// TestContribValues runs the ltree, hstore and intarray value fields,
// filters and orderings and the fuzzystrmatch operators, Levenshtein
// ordering and distance field against a live database, with the
// extensions' schema off the search_path.
func TestContribValues(t *testing.T) {
	setupValues(t)
	hs := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{valuesSchema}
		cfg.Filters.AllowColumns = map[string][]string{valuesSchema + ".items": {"name", "path", "attrs", "tag_ids"}}
	})
	url := hs.URL

	t.Run("fields", func(t *testing.T) {
		res := post(t, url, `{allItems(orderBy: [ID_ASC]) { nodes {
			id pathNlevel pathParent pathsLca tagIdsIcount
			color: attrsValue(key: "color")
			pathLca(path: "top.science.x")
			rest: pathSubpath(offset: 1)
			last: pathSubpath(offset: -1, length: 1)
		} }}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			Nodes []map[string]any `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allItems"], &conn)
		want := []map[string]any{
			{"id": 1.0, "pathNlevel": 3.0, "pathParent": "top.science", "pathsLca": "top.a", "tagIdsIcount": 3.0,
				"color": "red", "pathLca": "top.science", "rest": "science.astronomy", "last": "astronomy"},
			{"id": 2.0, "pathNlevel": 2.0, "pathParent": "top", "pathsLca": "top", "tagIdsIcount": 2.0,
				"color": "blue", "pathLca": "top", "rest": "science", "last": "science"},
			{"id": 3.0, "pathNlevel": 1.0, "pathParent": "", "pathsLca": nil, "tagIdsIcount": 0.0,
				"color": nil, "pathLca": "", "rest": nil, "last": "top"},
			{"id": 4.0, "pathNlevel": nil, "pathParent": nil, "pathsLca": nil, "tagIdsIcount": nil,
				"color": nil, "pathLca": nil, "rest": nil, "last": nil},
		}
		for i, w := range want {
			for k, v := range w {
				if got := conn.Nodes[i][k]; got != v {
					t.Errorf("row %d %s = %#v, want %#v", i+1, k, got, v)
				}
			}
		}
	})

	t.Run("subpath length is a parameter", func(t *testing.T) {
		q := `query ($len: Int) { allItems(orderBy: [ID_ASC], first: 1) { nodes { pathSubpath(offset: 0, length: $len) } } }`
		for _, tc := range []struct {
			len  any
			want string
		}{{nil, "top.science.astronomy"}, {2, "top.science"}, {0, ""}} {
			res := post(t, url, q, map[string]any{"len": tc.len})
			requireNoErrors(t, res)
			var conn struct {
				Nodes []struct {
					Subpath string `json:"pathSubpath"`
				} `json:"nodes"`
			}
			mustUnmarshal(t, res.Data["allItems"], &conn)
			if len(conn.Nodes) != 1 || conn.Nodes[0].Subpath != tc.want {
				t.Errorf("length %v: %s", tc.len, res.Data["allItems"])
			}
		}
		res := post(t, url, q, map[string]any{"len": -1})
		if len(res.Errors) == 0 {
			t.Error("negative length accepted")
		}
	})

	for _, tc := range []struct {
		name, args string
		want       []int
	}{
		{"nlevel", `filter: {pathNlevel: {greaterThanOrEqualTo: 2}}`, []int{1, 2, 5}},
		{"value in", `filter: {attrsValue: {key: "color", in: ["red", "blue"]}}`, []int{1, 2}},
		{"value null", `filter: {attrsValue: {key: "size", isNull: true}}`, []int{2, 4, 5}},
		{"value and", `filter: {and: [{attrsValue: {key: "color", likeInsensitive: "%e%"}}, {attrsValue: {key: "size", equalTo: "L"}}]}`, []int{1}},
		{"query_int", `filter: {tagIds: {matchesQueryInt: "2&!4"}}`, []int{1}},
		{"query_int negation", `filter: {tagIds: {matchesQueryInt: "!2"}}`, []int{3, 5}},
		{"icount", `filter: {tagIdsIcount: {greaterThan: 1}}`, []int{1, 2}},
		{"soundex", `filter: {name: {soundexEqualTo: "Rupert"}}`, []int{1, 2}},
		{"double metaphone", `filter: {name: {doubleMetaphoneEqualTo: "Smith"}}`, []int{3}},
		{"metaphone", `filter: {name: {metaphoneEqualTo: "Smithe"}}`, []int{3}},
		{"levenshtein", `filter: {name: {levenshteinDistanceLessThan: {text: "Robert", distance: 3}}}`, []int{1, 2}},
		{"levenshtein negated skips long values", `filter: {not: {name: {levenshteinDistanceLessThan: {text: "Robert", distance: 3}}}}`, []int{3}},
		{"nlevel order", `orderBy: [PATH_NLEVEL_DESC]`, []int{1, 5, 2, 3, 4}},
		{"value order", `hstoreKey: {attrs: "color"}, orderBy: [ATTRS_VALUE_ASC]`, []int{2, 5, 1, 3, 4}},
		{"value order desc", `hstoreKey: {attrs: "color"}, orderBy: [ATTRS_VALUE_DESC]`, []int{1, 5, 2, 3, 4}},
		{"levenshtein order", `similarTo: {name: "Robert"}, orderBy: [NAME_LEVENSHTEIN_DISTANCE_ASC]`, []int{1, 2, 3, 4, 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if !strings.Contains(args, "orderBy") {
				args += ", orderBy: [ID_ASC]"
			}
			res := post(t, url, `{allItems(`+args+`) { nodes { id } }}`, nil)
			requireNoErrors(t, res)
			if got := itemIDs(t, res.Data["allItems"]); !slices.Equal(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}

	t.Run("levenshtein distance field", func(t *testing.T) {
		res := post(t, url, `{allItems(similarTo: {name: "Robert"}, orderBy: [ID_ASC]) { nodes { id nameLevenshteinDistance } }}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			Nodes []struct {
				ID       int  `json:"id"`
				Distance *int `json:"nameLevenshteinDistance"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allItems"], &conn)
		if d := conn.Nodes[0].Distance; d == nil || *d != 0 {
			t.Errorf("Robert: %v", d)
		}
		if d := conn.Nodes[1].Distance; d == nil || *d != 2 {
			t.Errorf("Rupert: %v", d)
		}
		if conn.Nodes[4].Distance != nil {
			t.Errorf("a 300-character name has distance %v", *conn.Nodes[4].Distance)
		}
	})

	t.Run("keyset over value order", func(t *testing.T) {
		q := `query ($after: Cursor) { allItems(hstoreKey: {attrs: "color"}, orderBy: [ATTRS_VALUE_ASC], first: 2, after: $after) {
			nodes { id } pageInfo { endCursor hasNextPage } } }`
		var got []int
		var after any
		for range 5 {
			res := post(t, url, q, map[string]any{"after": after})
			requireNoErrors(t, res)
			var page struct {
				Nodes []struct {
					ID int `json:"id"`
				} `json:"nodes"`
				PageInfo struct {
					EndCursor   string `json:"endCursor"`
					HasNextPage bool   `json:"hasNextPage"`
				} `json:"pageInfo"`
			}
			mustUnmarshal(t, res.Data["allItems"], &page)
			for _, n := range page.Nodes {
				got = append(got, n.ID)
			}
			if !page.PageInfo.HasNextPage {
				break
			}
			after = page.PageInfo.EndCursor
		}
		if !slices.Equal(got, []int{2, 5, 1, 3, 4}) {
			t.Errorf("pages = %v", got)
		}
	})

	t.Run("errors", func(t *testing.T) {
		for _, q := range []string{
			`{allItems(orderBy: [ATTRS_VALUE_ASC]) { nodes { id } }}`,
			`{allItems(filter: {name: {levenshteinDistanceLessThan: {text: "` + strings.Repeat("x", 256) + `", distance: 1}}}) { nodes { id } }}`,
			`{allItems(filter: {tagIds: {matchesQueryInt: "1 &"}}) { nodes { id } }}`,
		} {
			if res := post(t, url, q, nil); len(res.Errors) == 0 {
				t.Errorf("accepted: %.80s", q)
			}
		}
	})
}

// TestContribValuesPolicy checks the filter policy: under indexed_only the
// unindexed value filters and orderings and the fuzzystrmatch operators
// need filters.allow_columns, while query_int follows the GIN index; with
// indexed_only off every column gets them, plus containsDefinedKey.
func TestContribValuesPolicy(t *testing.T) {
	setupValues(t)
	strict := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{valuesSchema}
	})
	res := post(t, strict.URL, `{__type(name: "ItemFilter") { inputFields { name type { name } } }}`, nil)
	requireNoErrors(t, res)
	raw := string(res.Data["__type"])
	for _, absent := range []string{"pathNlevel", "attrsValue", "tagIdsIcount", "Fuzzy"} {
		if strings.Contains(raw, absent) {
			t.Errorf("indexed_only without allowlist exposes %s: %s", absent, raw)
		}
	}
	if !strings.Contains(raw, `"name":"tagIds","type":{"name":"IntListIntarrayFilterOps"}`) {
		t.Errorf("GIN-indexed tag_ids lacks query_int: %s", raw)
	}

	open := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{valuesSchema}
		cfg.Filters.IndexedOnly = false
	})
	res = post(t, open.URL, `{allItems(filter: {attrs: {containsDefinedKey: "size"}, pathNlevel: {lessThan: 3}}, orderBy: [ID_ASC]) { nodes { id } }}`, nil)
	requireNoErrors(t, res)
	if got := itemIDs(t, res.Data["allItems"]); !slices.Equal(got, []int{3}) {
		t.Errorf("containsDefinedKey and nlevel = %v, want [3]", got)
	}

	disabled := testServer(t, func(cfg *config.Config) {
		cfg.Schema.Schemas = []string{valuesSchema}
		cfg.Filters.IndexedOnly = false
		cfg.Plugins.Disabled = append(cfg.Plugins.Disabled, "contrib")
	})
	res = post(t, disabled.URL, `{allItems(orderBy: [ID_ASC]) { nodes { id attrs path } }}`, nil)
	requireNoErrors(t, res)
	if res = post(t, disabled.URL, `{allItems { nodes { pathNlevel } }}`, nil); len(res.Errors) == 0 {
		t.Error("value fields with the contrib plugin disabled")
	}
}
