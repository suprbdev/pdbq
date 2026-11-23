package aggregates_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// build builds cat with advanced-filters, postgis and, when withPlugin is
// set, the aggregates plugin configured by settings.
func build(t *testing.T, cat *introspect.Catalog, withPlugin bool, disabled map[string]bool, settings map[string]any) *schema.Built {
	t.Helper()
	b := schema.New(cat, inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	plugins := []plugin.Plugin{advancedfilters.New(nil), postgisplugin.New(nil)}
	if withPlugin {
		plugins = append(plugins, aggregates.New(settings))
	}
	if err := plugin.NewRegistry(plugins...).TransformSchema(context.Background(), disabled, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func enabled(t *testing.T, cat *introspect.Catalog, settings map[string]any) string {
	t.Helper()
	s := map[string]any{"enabled": true}
	for k, v := range settings {
		s[k] = v
	}
	return build(t, cat, true, nil, s).SDL
}

func requireSDL(t *testing.T, sdl string, want, reject []string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(sdl, w) {
			t.Errorf("SDL missing %q", w)
		}
	}
	for _, r := range reject {
		if strings.Contains(sdl, r) {
			t.Errorf("SDL must not contain %q", r)
		}
	}
}

// TestDisabledByteIdentical asserts the opt-in contract: without
// enabled: true (and without @aggregates tags) the plugin leaves the schema
// byte-identical, as does disabling it by name.
func TestDisabledByteIdentical(t *testing.T) {
	base := build(t, testutil.FixtureCatalog(), false, nil, nil).SDL
	for name, sdl := range map[string]string{
		"default settings": build(t, testutil.FixtureCatalog(), true, nil, nil).SDL,
		"enabled: false":   build(t, testutil.FixtureCatalog(), true, nil, map[string]any{"enabled": false}).SDL,
		"plugins.disabled": build(t, testutil.FixtureCatalog(), true, map[string]bool{"aggregates": true}, map[string]any{"enabled": true}).SDL,
	} {
		if sdl != base {
			t.Errorf("%s: schema differs from the plugin-less schema", name)
		}
	}
	tagged := func() *introspect.Catalog { return tagTable(testutil.FixtureCatalog(), "posts", "@aggregates") }
	if build(t, tagged(), true, map[string]bool{"aggregates": true}, nil).SDL != build(t, tagged(), false, nil, nil).SDL {
		t.Error("plugins.disabled must win over an @aggregates tag")
	}
}

func TestSchemaSurface(t *testing.T) {
	sdl := enabled(t, testutil.FixtureCatalog(), nil)
	requireSDL(t, sdl, []string{
		"aggregates: PostAggregates",
		"groupedAggregates(\"\"\"The method to use when grouping `Post` for these aggregates.\"\"\" groupBy: [PostGroupBy!]!, \"\"\"Conditions on the grouped aggregates.\"\"\" having: PostHavingInput): [PostAggregates!]",
		"keys: [String]",
		"sum: PostSumAggregates",
		"distinctCount: PostDistinctCountAggregates",
		"stddevSample: PostStddevSampleAggregates",
		"variancePopulation: PostVariancePopulationAggregates",
		"type PostSumAggregates {\n  \"\"\"Sum of id across the matching connection.\"\"\"\n  id: BigInt!",
		"type UserSumAggregates {\n  \"\"\"Sum of id across the matching connection.\"\"\"\n  id: BigInt!\n  \"\"\"Sum of balance across the matching connection.\"\"\"\n  balance: BigFloat!",
		"type PostAverageAggregates {\n  \"\"\"Mean average of id across the matching connection.\"\"\"\n  id: BigFloat",
		"createdAt: Datetime",
		"enum PostGroupBy {\n  AUTHOR_ID\n  TITLE\n}",
		"input PostHavingInput {\n  AND: [PostHavingInput!]\n  OR: [PostHavingInput!]\n  sum: PostHavingSumInput",
		"input HavingBigintFilter {\n  equalTo: BigInt",
		"input HavingIntFilter {\n  equalTo: Int",
		"POSTS_BY_AUTHOR_ID_COUNT_ASC",
		"POSTS_BY_AUTHOR_ID_SUM_ID_DESC",
		"POSTS_BY_AUTHOR_ID_DISTINCT_COUNT_TITLE_ASC",
		"POSTS_BY_AUTHOR_ID_STDDEV_SAMPLE_AUTHOR_ID_DESC",
		"aggregates: PostAggregatesFilter",
		"input PostAggregatesFilter {",
		"filter: PostFilter",
		"input PostSumAggregateFilter {\n  id: BigIntFilterOps",
		"stExtent: PlaceStExtentAggregates",
		"stCentroid: PlaceStCentroidAggregates",
		"type PlaceStUnionAggregates {",
		"location: GeoJSON",
	}, []string{
		"PostSumAggregates {\n  \"\"\"Sum of title",
		"_ST_EXTENT_", "PlaceStExtentAggregateFilter", "PlaceHavingStExtentInput",
		"POSTS_BY_AUTHOR_ID_DISTINCT_COUNT_BODY_ASC",
	})
	requireSDL(t, sdl, []string{"enum UserGroupBy {\n  MOOD\n  SETTINGS\n  TAGS\n  MOODS\n}"}, nil)
}

func TestTimestampDerivatives(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		AllowColumns:      map[string][]string{"public.users": {"created_at"}},
	})
	if err := aggregates.New(map[string]any{"enabled": true}).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	requireSDL(t, built.SDL, []string{"CREATED_AT\n  CREATED_AT_TRUNCATED_TO_HOUR\n  CREATED_AT_TRUNCATED_TO_DAY"}, nil)
	if gb := built.GroupBy["UserGroupBy"]["CREATED_AT_TRUNCATED_TO_DAY"]; gb == nil || gb.SQL == nil || gb.SQL("x") != "date_trunc('day', x)" {
		t.Errorf("missing day-truncation binding: %+v", gb)
	}
}

func TestSettings(t *testing.T) {
	sdl := enabled(t, testutil.FixtureCatalog(), map[string]any{"grouped": false, "order": false, "filters": false, "spatial": false})
	requireSDL(t, sdl, []string{"aggregates: PostAggregates"}, []string{
		"groupedAggregates(", "PostGroupBy", "PostHavingInput", "POSTS_BY_AUTHOR_ID_COUNT_ASC",
		"PostAggregatesFilter", "stExtent",
	})
	sdl = enabled(t, testutil.FixtureCatalog(), map[string]any{"grouped": "false"})
	requireSDL(t, sdl, nil, []string{"groupedAggregates("})
}

// TestSmartTags covers per-table opt-in/out and per-column exclusion.
func TestSmartTags(t *testing.T) {
	cat := tagTable(testutil.FixtureCatalog(), "posts", "@aggregates")
	sdl := build(t, cat, true, nil, nil).SDL
	requireSDL(t, sdl, []string{"aggregates: PostAggregates", "POSTS_BY_AUTHOR_ID_COUNT_ASC"},
		[]string{"UserAggregates", "PlaceAggregates"})

	cat = tagTable(testutil.FixtureCatalog(), "posts", "@aggregates off")
	cat = tagTable(cat, "places", "@omit aggregates")
	cat = tagTable(cat, "users", "@behavior -aggregates:orderBy -groupedAggregates")
	sdl = enabled(t, cat, nil)
	requireSDL(t, sdl, []string{"aggregates: UserAggregates", "aggregates: CommentAggregates"},
		[]string{"PostAggregates", "PlaceAggregates", "UserGroupBy", "USERS_BY"})
	if strings.Contains(sdl, "POSTS_BY_AUTHOR_ID_COUNT") {
		t.Error("an opted-out child table must not drive relational ordering")
	}

	cat = testutil.FixtureCatalog()
	posts := cat.Table("public", "posts")
	posts.Column("author_id").Comment = "@omit aggregates"
	posts.Column("title").Comment = "@behavior -groupBy"
	sdl = enabled(t, cat, nil)
	requireSDL(t, sdl, []string{"POSTS_BY_AUTHOR_ID_DISTINCT_COUNT_TITLE_ASC", "Distinct count of title"},
		[]string{"POSTS_BY_AUTHOR_ID_SUM_AUTHOR_ID", "Sum of authorId", "PostGroupBy"})
}

// TestBindings checks the metadata the compiler reads.
func TestBindings(t *testing.T) {
	built := build(t, testutil.FixtureCatalog(), true, nil, map[string]any{"enabled": true, "scan_cost": 7})
	if m := built.Meta["PostConnection"]["aggregates"]; m == nil || m.Kind != schema.KindAggregates || m.ScanCost != 7 {
		t.Errorf("aggregates meta: %+v", m)
	}
	if m := built.Meta["PostConnection"]["groupedAggregates"]; m == nil || m.Kind != schema.KindGroupedAggregates {
		t.Errorf("groupedAggregates meta: %+v", m)
	}
	v := built.Meta["PostSumAggregates"]["id"]
	if v == nil || v.Kind != schema.KindAggregateValue || v.Aggregate.SQL(`t."id"`) != `coalesce(sum(t."id"), '0')` || v.Aggregate.ResultType != "int8" {
		t.Errorf("sum binding: %+v", v)
	}
	if a := built.InputAggregates["PostHavingSumInput"]["id"]; a == nil || a.Spec != "sum" {
		t.Errorf("having binding: %+v", a)
	}
	if a := built.InputAggregates["PostSumAggregateFilter"]["id"]; a == nil || a.Spec != "sum" {
		t.Errorf("relation filter binding: %+v", a)
	}
	spec := built.OrderBy["UsersOrderBy"]["POSTS_BY_AUTHOR_ID_COUNT_DESC"]
	if spec.Relation == nil || spec.Relation.Aggregate == nil || spec.Relation.Aggregate.SQL("") != "count(*)" || !spec.Desc {
		t.Errorf("count order binding: %+v", spec)
	}
	extent := built.Meta["PlaceStExtentAggregates"]["area"]
	if extent == nil || extent.Aggregate.SQL(`t."area"`) != `ST_Extent(t."area"::geometry)::geometry` {
		t.Errorf("geography extent must cast to geometry: %+v", extent)
	}
}

func tagTable(cat *introspect.Catalog, table, comment string) *introspect.Catalog {
	cat.Table("public", table).Comment = comment
	return cat
}

// TestTypedGeometry checks that spatial aggregates keep the GeoJSON scalar
// when spatial columns read as typed geometry objects.
func TestTypedGeometry(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true})
	reg := plugin.NewRegistry(postgisplugin.New(map[string]any{"typed_geometry": true}), aggregates.New(map[string]any{"enabled": true}))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	requireSDL(t, built.SDL, []string{"type PlaceStExtentAggregates {\n  \"\"\"Bounding box (ST_Extent) of location across the matching connection.\"\"\"\n  location: GeoJSON"}, nil)
}
