package exec

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// cost parses+validates query against the fixture schema and returns the
// measured cost with a page cap of 100.
func cost(t *testing.T, query string, rawVars map[string]any) int {
	t.Helper()
	return costWith(t, testutil.FixtureCatalog(), query, rawVars)
}

// costWith is cost against a caller-supplied catalog, so tests can set
// database COMMENTs (e.g. @costMultiplier) before the schema is built.
func costWith(t *testing.T, cat *introspect.Catalog, query string, rawVars map[string]any) int {
	t.Helper()
	return costFor(t, cat, nil, query, rawVars)
}

func defaultPostgis() *postgisplugin.Settings {
	s := postgisplugin.DefaultSettings()
	return &s
}

func typedPostgis() *postgisplugin.Settings {
	s := postgisplugin.DefaultSettings()
	s.TypedGeometry = true
	return &s
}

// defaultMaxCost mirrors config.DefaultConfig().Limits.MaxCost.
const defaultMaxCost = 100000

// costFor is costWith with the postgis plugin applied first when
// postgisSettings is non-nil.
func costFor(t *testing.T, cat *introspect.Catalog, postgisSettings *postgisplugin.Settings, query string, rawVars map[string]any) int {
	t.Helper()
	b := schema.New(cat, inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	if postgisSettings != nil {
		if err := postgisplugin.New(*postgisSettings).TransformSchema(context.Background(), b); err != nil {
			t.Fatal(err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	op := doc.Operations[0]
	vars, verr := validator.VariableValues(built.Schema, op, rawVars)
	if verr != nil {
		t.Fatalf("variables: %v", verr)
	}
	_, c := measure(op.SelectionSet, doc.Fragments, vars, 100, 1, built)
	return c
}

func TestCostScalesWithFirst(t *testing.T) {
	small := cost(t, `{ allUsers(first: 1) { nodes { id } } }`, nil)
	big := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	if big <= small {
		t.Fatalf("cost(first:100)=%d not > cost(first:1)=%d", big, small)
	}
}

func TestCostClampedToPageCap(t *testing.T) {
	capped := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	huge := cost(t, `{ allUsers(first: 100000) { nodes { id } } }`, nil)
	if huge != capped {
		t.Fatalf("cost(first:100000)=%d != cost(first:100)=%d; page cap not applied", huge, capped)
	}
}

func TestCostResolvesVariableFirst(t *testing.T) {
	literal := cost(t, `{ allUsers(first: 50) { nodes { id } } }`, nil)
	variable := cost(t, `query($n: Int) { allUsers(first: $n) { nodes { id } } }`,
		map[string]any{"n": 50})
	if variable != literal {
		t.Fatalf("variable first cost %d != literal cost %d", variable, literal)
	}
}

func TestCostFirstOneCheaperThanUnpaginated(t *testing.T) {
	def := cost(t, `{ allUsers(first: 10) { nodes { postsByAuthorId { nodes { id } } } } }`, nil)
	one := cost(t, `{ allUsers(first: 10) { nodes { postsByAuthorId(first: 1) { nodes { id } } } } }`, nil)
	if one >= def {
		t.Fatalf("cost(first:1)=%d not < cost(no first)=%d; explicit small page must not cost more", one, def)
	}
}

func TestCostPaginationNotChargedTwice(t *testing.T) {
	// The page multiplier applies once at the connection field; nodes under
	// it must not add the ×10 list default on top. A connection without
	// first/last fetches a full page, so it costs the same as first equal
	// to the page-size cap.
	def := cost(t, `{ allUsers { nodes { id } } }`, nil)
	full := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	if full != def {
		t.Fatalf("cost(first:100)=%d != cost(no first)=%d; pagination charged twice", full, def)
	}
}

func TestCostUnboundedConnectionPricedAtPageSize(t *testing.T) {
	for _, q := range []string{
		`{ allUsers { nodes { id email } } }`,
		`{ allUsers { edges { cursor node { id } } } }`,
		`{ userById(id: 1) { postsByAuthorId { nodes { id title } } } }`,
	} {
		unbounded := cost(t, q, nil)
		half := cost(t, strings.Replace(strings.Replace(q, "allUsers", "allUsers(first: 50)", 1), "postsByAuthorId", "postsByAuthorId(first: 50)", 1), nil)
		if unbounded <= half {
			t.Errorf("%s: cost without first %d is not above cost with first: 50 (%d)", q, unbounded, half)
		}
	}
}

func TestCostDefaultMaxCostAdmitsTwoLevels(t *testing.T) {
	two := cost(t, `{ allUsers { totalCount nodes { id email fullName createdAt
		postsByAuthorId { totalCount nodes { id title body published } } } } }`, nil)
	if two > defaultMaxCost {
		t.Errorf("two-level query without first costs %d, over the default max_cost %d", two, defaultMaxCost)
	}
	three := cost(t, `{ allUsers { nodes { id postsByAuthorId { nodes { id commentsByPostId { nodes { id } } } } } } }`, nil)
	if three <= defaultMaxCost {
		t.Errorf("three-level query without first costs %d, within the default max_cost %d", three, defaultMaxCost)
	}
}

// TestCostSetReturningListsPricedAtPageSize charges a SETOF root function
// and a SETOF computed column like a connection without first: the page-size
// cap rows the compiled LIMIT fetches, each with its nested connection (the
// connection also charges each row's nodes field).
func TestCostSetReturningListsPricedAtPageSize(t *testing.T) {
	for _, tc := range []struct{ name, list, conn string }{
		{"root function",
			`{ searchPosts(term: "") { id commentsByPostId { nodes { id body } } } }`,
			`{ allPosts { nodes { id commentsByPostId { nodes { id body } } } } }`},
		{"computed column",
			`{ userById(id: 1) { recentPosts(n: 5) { id commentsByPostId { nodes { id body } } } } }`,
			`{ userById(id: 1) { postsByAuthorId { nodes { id commentsByPostId { nodes { id body } } } } } }`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, conn := cost(t, tc.list, nil), cost(t, tc.conn, nil)
			if want := conn - 100; list != want {
				t.Errorf("set-returning list costs %d, want %d like the same rows through a connection (less its nodes fields)", list, want)
			}
			if list <= defaultMaxCost/100 {
				t.Errorf("set-returning list costs %d, priced below a page of rows", list)
			}
		})
	}
	deeper := cost(t, `{ searchPosts(term: "") { id userByAuthorId { postsByAuthorId { nodes { id commentsByPostId { nodes { id } } } } } } }`, nil)
	if deeper <= defaultMaxCost {
		t.Errorf("two nested connections below a set-returning list cost %d, within the default max_cost %d", deeper, defaultMaxCost)
	}
}

func TestCostNestedPaginationCompounds(t *testing.T) {
	flat := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	nested := cost(t, `{ allUsers(first: 100) { nodes { postsByAuthorId(first: 100) { nodes { id } } } } }`, nil)
	if nested < flat*50 {
		t.Fatalf("nested pagination cost %d does not compound over flat %d", nested, flat)
	}
}

// withReplyCostMultiplier returns the fixture catalog with the comment on the
// self-referential comments.parent_id FK replaced by tag (the shared fixture
// ships it as "@costMultiplier 3"; "" gives the untagged baseline).
func withReplyCostMultiplier(tag string) *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	for _, fk := range cat.Table("public", "comments").ForeignKeys {
		if fk.Name == "comments_parent_id_fkey" {
			fk.Comment = tag
		}
	}
	return cat
}

// threadQuery is a 4-level comment thread, the shape that makes the default
// page^depth estimate explode.
const threadQuery = `{
  allComments(first: 20) { nodes { id
    commentsByParentId(first: 20) { nodes { id
      commentsByParentId(first: 20) { nodes { id
        commentsByParentId(first: 20) { nodes { id } } } } } } } }
}`

func TestCostMultiplierLowersRecursiveEstimate(t *testing.T) {
	plain := costWith(t, withReplyCostMultiplier(""), threadQuery, nil)
	tagged := costWith(t, withReplyCostMultiplier("@costMultiplier 3"), threadQuery, nil)
	if tagged >= plain {
		t.Fatalf("@costMultiplier did not lower cost: tagged=%d plain=%d", tagged, plain)
	}
	// The point of the tag is that a deep thread fits under a sane max_cost.
	if tagged > 10000 {
		t.Errorf("tagged cost %d still exceeds the default max_cost 10000", tagged)
	}
	if plain <= 10000 {
		t.Errorf("untagged cost %d unexpectedly fits the default limit; fixture no longer exercises the blowup", plain)
	}
}

func TestCostMultiplierNeverRaisesCost(t *testing.T) {
	// A query asking for fewer rows than the declared average fan-out must be
	// charged the smaller requested page, not the average.
	const small = `{ allComments(first: 20) { nodes { id
	    commentsByParentId(first: 2) { nodes { id } } } } }`
	plain := costWith(t, withReplyCostMultiplier(""), small, nil)
	tagged := costWith(t, withReplyCostMultiplier("@costMultiplier 10"), small, nil)
	if tagged != plain {
		t.Fatalf("@costMultiplier raised cost above the requested page: tagged=%d plain=%d", tagged, plain)
	}
}

func TestCostMultiplierIgnoresInvalid(t *testing.T) {
	plain := costWith(t, withReplyCostMultiplier(""), threadQuery, nil)
	for _, tag := range []string{"@costMultiplier 0", "@costMultiplier -5", "@costMultiplier abc", "@costMultiplier"} {
		if got := costWith(t, withReplyCostMultiplier(tag), threadQuery, nil); got != plain {
			t.Errorf("tag %q changed cost to %d, want unchanged %d", tag, got, plain)
		}
	}
}

// Typed geometry list fields (points, interiors, ...) read from one
// in-statement GeoJSON value, so they must not be charged the ×10 list
// fan-out: a full page of polygons stays under the default limit.
func TestCostTypedGeometryLists(t *testing.T) {
	const query = `{ allPlaces(first: 100) { nodes { area { ... on GeographyPolygon {
		exterior { points { longitude latitude } }
		interiors { points { longitude latitude } }
	} } } } }`
	got := costFor(t, testutil.FixtureCatalog(), typedPostgis(), query, nil)
	if limit := defaultMaxCost; got > limit {
		t.Errorf("typed polygon page costs %d, over the default max_cost %d", got, limit)
	}
}

func enabledAggregates() aggregates.Settings {
	s := aggregates.DefaultSettings()
	s.Enabled = true
	return s
}

// aggregateCost is cost against the fixture schema with the aggregates
// plugin enabled.
func aggregateCost(t *testing.T, query string) int {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	if err := advancedfilters.New(advancedfilters.DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if err := aggregates.New(enabledAggregates(), false).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	_, c := measure(doc.Operations[0].SelectionSet, doc.Fragments, nil, 100, 1, built)
	return c
}

// TestCostAggregates pins the aggregate cost model: an aggregates field adds
// the scan cost (10) to its selection; groupedAggregates, a list of groups,
// also multiplies its selection by the page-size cap (100); every aggregate
// orderBy value (tripled under a cursor) and relation-aggregate comparison
// adds the scan cost to its field.
func TestCostAggregates(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
	}{
		{`{ allPosts(first: 1) { totalCount } }`, 2},
		{`{ allPosts(first: 1) { aggregates { sum { id } } } }`, 14},
		{`{ allPosts(first: 1) { groupedAggregates(groupBy: [AUTHOR_ID]) { keys } } }`, 112},
		{`{ allUsers(first: 1, orderBy: [POSTS_BY_AUTHOR_ID__COUNT_DESC]) { totalCount } }`, 12},
		{`{ allUsers(first: 1, after: "x", orderBy: [POSTS_BY_AUTHOR_ID__COUNT_DESC]) { totalCount } }`, 32},
		{`{ allUsers(first: 1, filter: {not: {postsByAuthorId: {aggregates: {
			filter: {title: {startsWith: "a"}}
			sum: {id: {greaterThan: "1"}}
			max: {id: {lessThan: 2}}
		}}}}) { totalCount } }`, 22},
	} {
		if got := aggregateCost(t, tc.query); got != tc.want {
			t.Errorf("cost(%s) = %d, want %d", tc.query, got, tc.want)
		}
	}
}

// TestCostScoreFields pins score fields at the cost of a column: one per
// row, multiplied by the page like any scalar, whatever the reference.
func TestCostScoreFields(t *testing.T) {
	cols := costFor(t, testutil.FixtureCatalog(), defaultPostgis(), `{ allPlaces(first: 20) { nodes { id name } } }`, nil)
	scores := costFor(t, testutil.FixtureCatalog(), defaultPostgis(),
		`{ allPlaces(first: 20, near: {location: "POINT(0 0)", area: "POINT(0 0)"}) { nodes { locationDistance(useSpheroid: true) areaDistance } } }`, nil)
	if cols != scores {
		t.Errorf("score fields cost %d, columns %d", scores, cols)
	}
}

// TestCostRasterFields pins the raster surface: metadata accessors cost a
// column, a pixel value postgis.RasterValueCost and summary statistics
// postgis.RasterSummaryStatsCost per row (plus their selected members), all
// multiplied by the page.
func TestCostRasterFields(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "tiles", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "rast", Position: 2, PGType: "raster", TypeSchema: "public"},
		},
		PrimaryKey: &introspect.Constraint{Name: "tiles_pkey", Columns: []string{"id"}},
	})
	page := func(sel string) int {
		return costFor(t, cat, defaultPostgis(), `{ allTiles(first: 20) { nodes { `+sel+` } } }`, nil)
	}
	base := page("id")
	for sel, perRow := range map[string]int{
		"id rastWidth rastEnvelope":                     2,
		`id rastValue(point: "POINT(0 0)")`:             postgis.RasterValueCost,
		"id rastSummaryStats { count mean }":            postgis.RasterSummaryStatsCost + 2,
		`id v: rastValue(point: "POINT(0 0)", band: 2)`: postgis.RasterValueCost,
	} {
		if got := page(sel) - base; got != 20*perRow {
			t.Errorf("cost of %q = base + %d, want base + 20×%d", sel, got, perRow)
		}
	}
}
