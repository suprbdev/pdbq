package compile_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/plugin"
	"github.com/suprbdev/pdbq/internal/plugins/advancedfilters"
	"github.com/suprbdev/pdbq/internal/plugins/aggregates"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	postgisplugin "github.com/suprbdev/pdbq/internal/plugins/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

var update = flag.Bool("update", false, "rewrite golden files")

// build compiles the fixture catalog into a Built schema with default naming.
//
// The postgis plugin is applied because the spatial surface (filters, derived
// accessors, distance ordering) only exists once it has run, and the golden
// files cover that SQL.
func build(t testing.TB) *schema.Built {
	t.Helper()
	return buildWith(t, nil)
}

// buildWith is build with the postgis plugin configured by settings.
func buildWith(t testing.TB, settings map[string]any) *schema.Built {
	t.Helper()
	cat := testutil.FixtureCatalog()
	cat.Functions = append(cat.Functions, functionFixtures()...)
	return buildCatalog(t, cat, settings)
}

// buildContrib compiles the contrib fixture catalog (the shared fixture plus
// a table of contrib-typed columns and the extensions) with the postgis and
// contrib plugins applied.
func buildContrib(t testing.TB) *schema.Built {
	t.Helper()
	cat := contribfixture.Catalog()
	cat.Functions = append(cat.Functions, functionFixtures()...)
	return buildCatalog(t, cat, nil, contribplugin.New(nil))
}

// buildCatalog builds cat with default naming, the postgis plugin
// configured by settings and the extra schema hooks.
func buildCatalog(t testing.TB, cat *introspect.Catalog, settings map[string]any, hooks ...*contribplugin.Plugin) *schema.Built {
	t.Helper()
	b := schema.New(cat, inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	if err := postgisplugin.New(settings).TransformSchema(context.Background(), b); err != nil {
		t.Fatalf("postgis plugin: %v", err)
	}
	for _, h := range hooks {
		if err := h.TransformSchema(context.Background(), b); err != nil {
			t.Fatalf("%s plugin: %v", h.Name(), err)
		}
	}
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	return built
}

// buildAggregates compiles the fixture catalog with the advanced-filters,
// postgis and aggregates (enabled) plugins applied; users.created_at is
// allowed as an orderable column so its timestamp group-by derivatives exist.
func buildAggregates(t testing.TB) *schema.Built {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		AllowColumns:      map[string][]string{"public.users": {"created_at"}},
	})
	reg := plugin.NewRegistry(advancedfilters.New(nil), postgisplugin.New(nil),
		aggregates.New(map[string]any{"enabled": true, "spatial_union_collect": true}, nil))
	if err := reg.TransformSchema(context.Background(), nil, b); err != nil {
		t.Fatalf("plugins: %v", err)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	return built
}

// functionFixtures are functions the shared pdbcore fixture lacks, added to
// the golden catalog for the function-call surface: DEFAULT and VARIADIC
// parameters, OUT/TABLE records, composite returns and arguments, and PostGIS
// arguments. test/e2e_test.go creates the same functions in a real database.
func functionFixtures() []*introspect.Function {
	in := func(name, typ, typeSchema string) introspect.FuncArg {
		return introspect.FuncArg{Name: name, PGType: typ, TypeSchema: typeSchema, Mode: introspect.ArgModeIn}
	}
	withDefault := func(a introspect.FuncArg, def string) introspect.FuncArg {
		a.HasDefault, a.Default = true, def
		return a
	}
	out := func(name, typ, typeSchema string, mode introspect.ArgMode) introspect.FuncArg {
		return introspect.FuncArg{Name: name, PGType: typ, TypeSchema: typeSchema, Mode: mode}
	}
	stable := introspect.VolatilityStable
	return []*introspect.Function{{
		Schema: "public", Name: "greet",
		Args: []introspect.FuncArg{
			in("name", "text", "pg_catalog"),
			withDefault(in("greeting", "text", "pg_catalog"), "'Hello'::text"),
		},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, {
		Schema: "public", Name: "sum_all",
		Args: []introspect.FuncArg{
			withDefault(in("scale", "int4", "pg_catalog"), "1"),
			withDefault(introspect.FuncArg{Name: "nums", PGType: "_int4", TypeSchema: "pg_catalog", Mode: introspect.ArgModeVariadic}, "'{}'::integer[]"),
		},
		ReturnType: "int8", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, {
		Schema: "public", Name: "post_stats",
		Args:          []introspect.FuncArg{withDefault(in("min_id", "int4", "pg_catalog"), "0")},
		ReturnColumns: []introspect.FuncArg{out("total", "int8", "pg_catalog", introspect.ArgModeOut), out("max_title", "text", "pg_catalog", introspect.ArgModeOut)},
		ReturnType:    "record", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, {
		Schema: "public", Name: "post_titles",
		ReturnColumns: []introspect.FuncArg{out("id", "int4", "pg_catalog", introspect.ArgModeTable), out("title", "text", "pg_catalog", introspect.ArgModeTable)},
		ReturnType:    "record", ReturnTypeSchema: "pg_catalog", ReturnsSet: true, Volatility: stable,
	}, {
		Schema: "public", Name: "make_address",
		Args:       []introspect.FuncArg{in("street", "text", "pg_catalog"), in("city", "text", "pg_catalog")},
		ReturnType: "address", ReturnTypeSchema: "public", Volatility: stable,
	}, {
		Schema: "public", Name: "format_address",
		Args:       []introspect.FuncArg{in("a", "address", "public")},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, {
		Schema: "public", Name: "places_near",
		Args: []introspect.FuncArg{
			in("pt", "geometry", "public"),
			withDefault(in("max_dist", "float8", "pg_catalog"), "1000"),
		},
		ReturnType: "places", ReturnTypeSchema: "public", ReturnsSet: true, Volatility: stable,
	}, {
		Schema: "public", Name: "record_visit",
		Args:          []introspect.FuncArg{out("spot", "geometry", "public", introspect.ArgModeInOut), withDefault(in("note", "jsonb", "pg_catalog"), "NULL::jsonb")},
		ReturnColumns: []introspect.FuncArg{out("visit_id", "int4", "pg_catalog", introspect.ArgModeOut), out("spot", "geometry", "public", introspect.ArgModeInOut)},
		ReturnType:    "record", ReturnTypeSchema: "pg_catalog", Volatility: introspect.VolatilityVolatile,
	}, {
		Schema: "public", Name: "users_greeting",
		Args: []introspect.FuncArg{
			in("u", "users", "public"),
			withDefault(in("prefix", "text", "pg_catalog"), "'Hi'::text"),
		},
		ReturnType: "text", ReturnTypeSchema: "pg_catalog", Volatility: stable,
	}, {
		Schema: "public", Name: "users_home",
		Args:       []introspect.FuncArg{in("u", "users", "public")},
		ReturnType: "address", ReturnTypeSchema: "public", Volatility: stable,
	}}
}

// TestGolden compiles each testdata/*.graphql document and compares the SQL
// (plus parameters) against the .sql golden next to it. Run with -update to
// regenerate. Documents named spatial_typed_* compile against the schema
// built with plugins.settings.postgis.typed_geometry on, temporal_* ones
// against the catalog extended with temporalTables, through the
// advanced-filters and nested-mutations plugins (nested_* ones too), contrib_*
// against the contrib fixture catalog with the contrib plugin, aggregates_*
// against buildAggregates.
func TestGolden(t *testing.T) {
	plain := build(t)
	aggBuilt := buildAggregates(t)
	typed := buildWith(t, map[string]any{"typed_geometry": true})
	temporal, temporalReg := buildTemporal(t)
	contribBuilt := buildContrib(t)
	intervalBuilt := buildInterval(t)

	files, err := filepath.Glob(filepath.Join("testdata", "*.graphql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden inputs found: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".graphql")
		built := plain
		if strings.HasPrefix(name, "spatial_typed_") {
			built = typed
		}
		if strings.HasPrefix(name, "contrib_") {
			built = contribBuilt
		}
		if strings.HasPrefix(name, "interval_") {
			built = intervalBuilt
		}
		if strings.HasPrefix(name, "aggregates") {
			built = aggBuilt
		}
		compileFn := compile.New(built).Compile
		if strings.HasPrefix(name, "temporal") || strings.HasPrefix(name, "nested_") {
			built = temporal
			compileFn = temporalReg.CompileChain(nil, compile.New(built).Compile)
		}
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			query, vars := splitVariables(t, string(src))

			doc, perr := parser.ParseQuery(&ast.Source{Name: name, Input: query})
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
				t.Fatalf("validate: %v", errs)
			}
			op := doc.Operations[0]
			coerced, verr := validator.VariableValues(built.Schema, op, vars)
			if verr != nil {
				t.Fatalf("variables: %v", verr)
			}

			var out strings.Builder
			for _, sel := range op.SelectionSet {
				f, ok := sel.(*ast.Field)
				if !ok {
					continue
				}
				stmt, err := compileFn(context.Background(), &compile.Request{
					Field:     f,
					Fragments: doc.Fragments,
					Vars:      coerced,
					Built:     built,
					MaxDepth:  15,
				})
				if err != nil {
					fmt.Fprintf(&out, "-- field: %s\n-- error: %v\n\n", f.Name, err)
					continue
				}
				argsJSON, _ := json.Marshal(stmt.Args)
				fmt.Fprintf(&out, "-- field: %s\n%s\n-- args: %s\n\n", f.Name, stmt.SQL, argsJSON)
			}

			goldenPath := filepath.Join("testdata", name+".sql")
			if *update {
				if err := os.WriteFile(goldenPath, []byte(out.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden file %s (run with -update): %v", goldenPath, err)
			}
			if string(want) != out.String() {
				t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, out.String())
			}
		})
	}
}

// splitVariables extracts an optional leading `# variables: {...}` comment.
func splitVariables(t *testing.T, src string) (string, map[string]any) {
	t.Helper()
	vars := map[string]any{}
	for _, line := range strings.Split(src, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# variables:"); ok {
			if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &vars); err != nil {
				t.Fatalf("bad variables comment: %v", err)
			}
		}
	}
	return src, vars
}
