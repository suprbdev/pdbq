package compile_test

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
	"github.com/suprbdev/pdbq/internal/schema"
)

// intervalCatalog is the shared fixture plus public.durations (interval and
// interval[] columns), a composite with an interval attribute and functions
// taking and returning intervals. test/interval_e2e_test.go creates the same
// objects in a real database.
func intervalCatalog() *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	col := func(pos int, name, typ string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: "pg_catalog", BaseTypeSchema: "pg_catalog"}
	}
	id := col(1, "id", "int4")
	id.NotNull, id.GeneratedKind = true, introspect.GeneratedIdentityAlways
	spans := col(4, "spans", "_interval")
	spans.IsArray = true
	timing := col(5, "timing", "timing")
	timing.TypeSchema, timing.BaseTypeSchema = "public", "public"
	cat.Composites = append(cat.Composites, &introspect.Composite{
		Schema: "public", Name: "timing",
		Fields: []*introspect.Column{col(1, "label", "text"), col(2, "span", "interval"), func() *introspect.Column {
			c := col(3, "spans", "_interval")
			c.IsArray = true
			return c
		}()},
	})
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "durations", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns:    []*introspect.Column{id, col(2, "label", "text"), col(3, "span", "interval"), spans, timing},
		PrimaryKey: &introspect.Constraint{Name: "durations_pkey", Columns: []string{"id"}},
		Indexes: []*introspect.Index{
			{Name: "durations_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "durations_span_idx", Columns: []string{"span"}, Method: "btree"},
			{Name: "durations_spans_idx", Columns: []string{"spans"}, Method: "gin"},
		},
	})
	in := func(name, typ string) introspect.FuncArg {
		return introspect.FuncArg{Name: name, PGType: typ, TypeSchema: "pg_catalog", Mode: introspect.ArgModeIn}
	}
	stable := introspect.VolatilityStable
	cat.Functions = append(cat.Functions, &introspect.Function{
		Schema: "public", Name: "double_span", Args: []introspect.FuncArg{in("span", "interval")},
		ReturnType: "interval", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "span_list", Args: []introspect.FuncArg{in("spans", "_interval")},
		ReturnType: "_interval", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, &introspect.Function{
		Schema: "public", Name: "durations_twice", Args: []introspect.FuncArg{{Name: "d", PGType: "durations", TypeSchema: "public", Mode: introspect.ArgModeIn}},
		ReturnType: "interval", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	})
	return cat
}

func buildInterval(t testing.TB) *schema.Built {
	t.Helper()
	b := schema.New(intervalCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true})
	reg := plugin.NewRegistry(advancedfilters.New(nil), aggregates.New(map[string]any{"enabled": true}, nil))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatal(err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	return built
}

func TestIntervalSchema(t *testing.T) {
	sdl := buildInterval(t).SDL
	for _, want := range []string{
		"type Interval {", "input IntervalInput {",
		"  seconds: Float\n", "  minutes: Int\n", "  years: Int\n", "  twice: Interval\n", "  timing: Timing\n",
		"  span: Interval\n", "  spans: [Interval!]\n",
		"  span: IntervalFilterOps\n", "  spans: IntervalListFilterOps\n",
		"  equalTo: IntervalInput\n", "  in: [IntervalInput!]\n", "  contains: [IntervalInput!]\n",
		"doubleSpan(span: IntervalInput): Interval", "spanList(spans: [IntervalInput]): [Interval!]",
	} {
		if !strings.Contains(sdl, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
}
