package compile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	contribplugin "github.com/suprbdev/pdbq/internal/plugins/contrib"
	"github.com/suprbdev/pdbq/internal/schema"
)

// buildCustomRanges is the contrib fixture plus public.readings, whose
// columns use range types created with CREATE TYPE ... AS RANGE (recorded in
// Catalog.Ranges): floatrange over float8 with its floatmultirange,
// slotrange over int4, and moodrange over the enum public.mood. Its
// shadow column is a public.hstore user type that shares the extension
// type's name.
func buildCustomRanges(t *testing.T) *schema.Built {
	t.Helper()
	cat := contribfixture.Catalog()
	cat.Ranges = []*introspect.RangeType{
		{Schema: "public", Name: "floatrange", Subtype: "float8", SubtypeSchema: "pg_catalog", Multirange: "floatmultirange", MultirangeSchema: "public"},
		{Schema: "public", Name: "moodrange", Subtype: "mood", SubtypeSchema: "public", Multirange: "moodmultirange", MultirangeSchema: "public"},
		{Schema: "public", Name: "slotrange", Subtype: "int4", SubtypeSchema: "pg_catalog", Multirange: "slotmultirange", MultirangeSchema: "public"},
	}
	col := func(pos int, name, typ, schema string) *introspect.Column {
		return &introspect.Column{Name: name, Position: pos, PGType: typ, TypeSchema: schema, BaseTypeSchema: schema}
	}
	id := col(1, "id", "int4", "pg_catalog")
	id.NotNull, id.GeneratedKind = true, introspect.GeneratedIdentityAlways
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "readings", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			id,
			col(2, "temp", "floatrange", "public"),
			col(3, "temps", "floatmultirange", "public"),
			col(4, "slot", "slotrange", "public"),
			col(5, "moods", "moodrange", "public"),
			col(6, "attrs", "hstore", contribfixture.ExtSchema),
			col(7, "shadow", "hstore", "public"),
		},
		PrimaryKey: &introspect.Constraint{Name: "readings_pkey", Columns: []string{"id"}},
		Indexes: []*introspect.Index{
			{Name: "readings_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"},
			{Name: "readings_temp_idx", Columns: []string{"temp"}, Method: "gist"},
			{Name: "readings_temps_idx", Columns: []string{"temps"}, Method: "gist"},
			{Name: "readings_slot_idx", Columns: []string{"slot"}, Method: "gist"},
		},
	})
	return buildCatalog(t, cat, nil, contribplugin.New(nil))
}

func TestCustomRangeTypes(t *testing.T) {
	built := buildCustomRanges(t)
	reading := built.Schema.Types["Reading"]
	if reading == nil {
		t.Fatal("Reading type missing")
	}
	for field, want := range map[string]string{
		"temp": "FloatRange", "temps": "[FloatRange!]", "slot": "IntRange",
		"moods": "String", "attrs": "KeyValueHash", "shadow": "String",
	} {
		if f := reading.Fields.ForName(field); f == nil || f.Type.String() != want {
			t.Errorf("Reading.%s = %v, want %s", field, f, want)
		}
	}
	bound := built.Schema.Types["FloatRangeBound"]
	if bound == nil || bound.Fields.ForName("value").Type.String() != "Float!" {
		t.Errorf("FloatRangeBound = %+v, want value: Float!", bound)
	}
	filter := built.Schema.Types["ReadingFilter"]
	for field, want := range map[string]string{
		"temp": "FloatRangeFilterOps", "temps": "FloatMultirangeFilterOps", "slot": "IntRangeFilterOps",
	} {
		if f := filter.Fields.ForName(field); f == nil || f.Type.String() != want {
			t.Errorf("ReadingFilter.%s = %v, want %s", field, f, want)
		}
	}
	if ops := built.Schema.Types["FloatRangeFilterOps"]; ops == nil || ops.Fields.ForName("containsElement").Type.String() != "Float" {
		t.Errorf("FloatRangeFilterOps.containsElement missing or not Float: %+v", ops)
	}

	for name, tc := range map[string]struct {
		query   string
		want    []string
		hstores int
	}{
		"filter": {
			`{ allReadings(filter: {temp: {overlaps: {start: {value: 1.5, inclusive: true}}}, slot: {containsElement: 3}, temps: {containsElement: 2.5}}) { nodes { id } } }`,
			[]string{
				`(__t_1."temp") && "public"."floatrange"($2::float8, $3::float8, $4::text)`,
				`(__t_1."slot") @> $1::int4`,
				`(__t_1."temps") @> $5::float8`,
			},
			0,
		},
		"output": {
			`{ allReadings { nodes { temp { start { value inclusive } end { value } } temps { start { value } } slot { start { value } } attrs shadow } } }`,
			[]string{"lower(", "upper(", `'shadow', __t_1."shadow"`},
			1,
		},
		"create": {
			`mutation { createReading(input: {temp: {start: {value: 1, inclusive: true}, end: {value: 2.5, inclusive: false}}, slot: {start: {value: 1, inclusive: true}}}) { reading { id } } }`,
			[]string{`"public"."floatrange"(`, `"public"."slotrange"(`},
			0,
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc, perr := parser.ParseQuery(&ast.Source{Input: tc.query})
			if perr != nil {
				t.Fatal(perr)
			}
			if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
				t.Fatal(errs)
			}
			stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{
				Field:    doc.Operations[0].SelectionSet[0].(*ast.Field),
				Built:    built,
				MaxDepth: 15,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tc.want {
				if !strings.Contains(stmt.SQL, w) {
					t.Errorf("SQL lacks %q:\n%s", w, stmt.SQL)
				}
			}
			if n := strings.Count(stmt.SQL, "hstore_to_json("); n != tc.hstores {
				t.Errorf("SQL has %d hstore_to_jsonb calls, want %d (only the extension-typed column):\n%s", n, tc.hstores, stmt.SQL)
			}
		})
	}
}
