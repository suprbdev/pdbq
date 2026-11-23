package postgis

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// withTiles adds public.tiles (id, rast raster, mask raster) to cat.
func withTiles(cat *introspect.Catalog) {
	cat.Tables = append(cat.Tables, &introspect.Table{
		Schema: "public", Name: "tiles", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true, Insert: true, Update: true, Delete: true},
		Columns: []*introspect.Column{
			{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog", NotNull: true},
			{Name: "rast", Position: 2, PGType: "raster", TypeSchema: "public"},
			{Name: "mask", Position: 3, PGType: "raster", TypeSchema: "public"},
		},
		PrimaryKey: &introspect.Constraint{Name: "tiles_pkey", Columns: []string{"id"}},
		Indexes:    []*introspect.Index{{Name: "tiles_pkey", Columns: []string{"id"}, Unique: true, Method: "btree"}},
	})
}

// buildTiles is buildWith over the fixture plus tiles under the given
// indexed-only policy, built into a valid schema.
func buildTiles(t *testing.T, settings func(*Settings), indexedOnly bool, allow map[string][]string, mutate func(*introspect.Catalog)) *schema.Builder {
	t.Helper()
	cat := testutil.FixtureCatalog()
	withTiles(cat)
	if mutate != nil {
		mutate(cat)
	}
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{
		Logger: slog.Default(), Functions: true, FilterIndexedOnly: indexedOnly, AllowColumns: allow,
	})
	s := DefaultSettings()
	if settings != nil {
		settings(&s)
	}
	if err := New(s).TransformSchema(context.Background(), b); err != nil {
		t.Fatalf("TransformSchema: %v", err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return b
}

func TestRasterFields(t *testing.T) {
	b := buildTiles(t, nil, false, nil, nil)
	obj := b.Objects["Tile"]
	if obj.Field("rast") != nil || obj.Field("mask") != nil {
		t.Error("raster columns have an output field")
	}
	for _, a := range postgis.RasterAccessors {
		f := obj.Field("rast" + a.Suffix)
		if f == nil || f.Type != a.Type || f.Meta.Kind != schema.KindRasterAccessor || f.Meta.Nested["suffix"] != a.Suffix {
			t.Errorf("rast%s: %+v", a.Suffix, f)
		}
	}
	if got := obj.Field("rastEnvelope").Type; got != "GeoJSON" {
		t.Errorf("rastEnvelope = %s", got)
	}
	v := obj.Field("rastValue")
	if v == nil || v.Type != "Float" || v.Meta.Kind != schema.KindRasterValue || v.Meta.RowCost != postgis.RasterValueCost-1 {
		t.Fatalf("rastValue: %+v", v)
	}
	if len(v.Args) != 2 || v.Args[0].Name != "point" || v.Args[0].Type != "GeoJSON!" || v.Args[1].Name != "band" || v.Args[1].Default != "1" {
		t.Errorf("rastValue args: %+v", v.Args)
	}
	st := obj.Field("rastSummaryStats")
	if st == nil || st.Type != "RasterSummaryStats" || st.Meta.Kind != schema.KindRasterSummaryStats || st.Meta.RowCost != postgis.RasterSummaryStatsCost-1 {
		t.Fatalf("rastSummaryStats: %+v", st)
	}
	stats := b.Objects["RasterSummaryStats"]
	for _, f := range postgis.RasterSummaryStatsFields {
		if sf := stats.Field(f.Name); sf == nil || sf.Type != f.Type {
			t.Errorf("RasterSummaryStats.%s: %+v", f.Name, sf)
		}
	}
	if got := fieldType(b, "TileCreateInput", "rast"); got != "String" {
		t.Errorf("TileCreateInput.rast = %q, want String (hex WKB input)", got)
	}
}

func fieldType(b *schema.Builder, input, field string) string {
	in := b.Inputs[input]
	if in == nil {
		return ""
	}
	if f := in.Field(field); f != nil {
		return f.Type
	}
	return ""
}

func TestRasterFilterAndNoGenericSurface(t *testing.T) {
	b := buildTiles(t, nil, false, nil, nil)
	for _, c := range []string{"rast", "mask"} {
		if got := fieldType(b, "TileFilter", c); got != "RasterFilterOps" {
			t.Errorf("TileFilter.%s = %q", c, got)
		}
		if fieldType(b, "TileCondition", c) != "" {
			t.Errorf("%s in the condition input", c)
		}
		for _, v := range b.Enums["TilesOrderBy"].Values {
			if v.Column == c {
				t.Errorf("orderBy value %s on a raster column", v.Name)
			}
		}
		if en := b.Enums["TilesDistinctOn"]; en != nil && en.HasValue(strings.ToUpper(c)) {
			t.Errorf("%s in distinctOn", c)
		}
	}
	ops := b.Inputs["RasterFilterOps"]
	if len(ops.Fields) != 2 || ops.Field("isNull") == nil || ops.Field("intersects").Type != "GeoJSON" {
		t.Errorf("RasterFilterOps fields: %+v", ops.Fields)
	}

	policy := buildTiles(t, nil, true, map[string][]string{"public.tiles": {"rast"}}, nil)
	if got := fieldType(policy, "TileFilter", "rast"); got != "RasterFilterOps" {
		t.Errorf("allowlisted rast = %q", got)
	}
	if got := fieldType(policy, "TileFilter", "mask"); got != "" {
		t.Errorf("indexed_only kept the mask filter: %q", got)
	}
}

func TestRasterSettingsAndOmit(t *testing.T) {
	noDerived := buildTiles(t, func(s *Settings) { s.Derived = false }, false, nil, nil)
	if noDerived.Objects["Tile"].Field("rastWidth") != nil || noDerived.Objects["RasterSummaryStats"] != nil {
		t.Error("derived: false kept the raster fields")
	}
	if fieldType(noDerived, "TileFilter", "rast") == "" {
		t.Error("derived: false removed the raster filter")
	}
	noFilters := buildTiles(t, func(s *Settings) { s.Filters = false }, false, nil, nil)
	if fieldType(noFilters, "TileFilter", "rast") != "" || noFilters.Inputs["RasterFilterOps"] != nil {
		t.Error("filters: false kept the raster filter")
	}

	omitted := buildTiles(t, nil, false, nil, func(cat *introspect.Catalog) {
		tiles := cat.Table("public", "tiles")
		tiles.Column("rast").Comment = "@omit read"
		tiles.Column("mask").Comment = "@omit filter"
	})
	obj := omitted.Objects["Tile"]
	for _, f := range []string{"rastWidth", "rastValue", "rastSummaryStats", "rastEnvelope"} {
		if obj.Field(f) != nil {
			t.Errorf("@omit read kept %s", f)
		}
	}
	if fieldType(omitted, "TileFilter", "rast") != "" || fieldType(omitted, "TileFilter", "mask") != "" {
		t.Error("@omit read / @omit filter kept the raster filter")
	}
	if obj.Field("maskSrid") == nil {
		t.Error("@omit filter removed the raster fields")
	}
}

func TestRasterOnlyCatalog(t *testing.T) {
	b := buildTiles(t, nil, false, nil, func(cat *introspect.Catalog) {
		cat.Tables = removeTable(cat.Tables, "places")
	})
	sdl := b.SDL()
	if !strings.Contains(sdl, "scalar GeoJSON") {
		t.Error("raster surface without the GeoJSON scalar")
	}
	for _, typ := range []string{"BBoxInput", "GeometryFilterOps", "NearInput"} {
		if strings.Contains(sdl, typ) {
			t.Errorf("raster-only catalog declares %s", typ)
		}
	}

	none := buildTiles(t, func(s *Settings) { s.Filters, s.Derived = false, false }, false, nil, func(cat *introspect.Catalog) {
		cat.Tables = removeTable(cat.Tables, "places")
	})
	if strings.Contains(none.SDL(), "GeoJSON") {
		t.Error("GeoJSON declared without any raster surface")
	}
}

func TestRasterNamesTaken(t *testing.T) {
	cat := testutil.FixtureCatalog()
	withTiles(cat)
	var logs bytes.Buffer
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{Logger: slog.New(slog.NewTextHandler(&logs, nil)), Functions: true})
	b.Objects["RasterSummaryStats"] = &schema.Object{Name: "RasterSummaryStats"}
	b.Inputs["RasterFilterOps"] = &schema.Input{Name: "RasterFilterOps"}
	if err := New(DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if b.Objects["Tile"].Field("rastSummaryStats") != nil || b.Objects["Tile"].Field("rastValue") == nil {
		t.Error("summary statistics added although RasterSummaryStats is taken")
	}
	if fieldType(b, "TileFilter", "rast") != "" {
		t.Error("raster filter added although RasterFilterOps is taken")
	}
	for _, n := range []string{"RasterSummaryStats", "RasterFilterOps"} {
		if !strings.Contains(logs.String(), n) {
			t.Errorf("no warning for %s: %s", n, logs.String())
		}
	}
}
