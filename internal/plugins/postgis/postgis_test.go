package postgis

import (
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// buildWith applies the plugin (configured by settings) to the fixture catalog
// and returns the builder for inspection.
func buildWith(t *testing.T, settings map[string]any, mutate func(*introspect.Catalog)) *schema.Builder {
	t.Helper()
	cat := testutil.FixtureCatalog()
	if mutate != nil {
		mutate(cat)
	}
	b := schema.New(cat, inflect.Next(inflect.Default), schema.Options{
		Logger:    slog.Default(),
		Functions: true,
	})
	if err := New(settings).TransformSchema(context.Background(), b); err != nil {
		t.Fatalf("TransformSchema: %v", err)
	}
	return b
}

func TestSpatialFilterFields(t *testing.T) {
	b := buildWith(t, nil, nil)
	in, ok := b.Inputs["PlaceFilter"]
	if !ok {
		t.Fatal("PlaceFilter not generated")
	}
	for _, name := range []string{"location", "area"} {
		f := in.Field(name)
		if f == nil {
			t.Fatalf("filter field %q missing", name)
		}
		if f.Type != "GeometryFilterOps" {
			t.Errorf("%s type = %q, want GeometryFilterOps", name, f.Type)
		}
		if f.Column != name {
			t.Errorf("%s column binding = %q, want %q", name, f.Column, name)
		}
	}
	ops, ok := b.Inputs["GeometryFilterOps"]
	if !ok {
		t.Fatal("GeometryFilterOps not generated")
	}
	for _, name := range []string{
		"bboxIntersects", "intersects", "within", "dwithin", "beyond", "isNull",
		"bboxLeftOf", "bboxRightOf", "bboxBelow", "bboxAbove",
		"bboxOverlapsOrLeftOf", "bboxOverlapsOrRightOf", "bboxOverlapsOrBelow", "bboxOverlapsOrAbove",
		"bboxEquals", "exactlyEquals", "orderingEquals", "intersects3D", "bboxIntersectsND",
	} {
		if ops.Field(name) == nil {
			t.Errorf("operator %q missing from GeometryFilterOps", name)
		}
	}
	if got := ops.Field("bboxIntersects").Type; got != "BBoxInput" {
		t.Errorf("bboxIntersects takes %q, want BBoxInput", got)
	}
	if got := ops.Field("dwithin").Type; got != "DWithinInput" {
		t.Errorf("dwithin takes %q, want DWithinInput", got)
	}
	for _, name := range []string{"bboxLeftOf", "bboxEquals", "exactlyEquals", "intersects3D", "bboxIntersectsND"} {
		if got := ops.Field(name).Type; got != "GeoJSON" {
			t.Errorf("%s takes %q, want GeoJSON", name, got)
		}
	}
}

func TestNoScalarOpsOnSpatialColumns(t *testing.T) {
	b := buildWith(t, nil, nil)
	ops := b.Inputs["GeometryFilterOps"]
	// Scalar comparisons are meaningless for PostGIS types and mostly rejected
	// by Postgres; they must not be offered.
	for _, name := range []string{"equalTo", "lessThan", "greaterThan", "in", "like"} {
		if ops.Field(name) != nil {
			t.Errorf("scalar operator %q must not appear on spatial columns", name)
		}
	}
	// Condition and distinctOn need btree equality, which PostGIS lacks.
	if cond, ok := b.Inputs["PlaceCondition"]; ok {
		for _, name := range []string{"location", "area"} {
			if cond.Field(name) != nil {
				t.Errorf("spatial column %q must not appear in PlaceCondition", name)
			}
		}
	}
	if en, ok := b.Enums["PlacesDistinctOn"]; ok {
		for _, v := range en.Values {
			if v.Column == "location" || v.Column == "area" {
				t.Errorf("spatial column must not appear in distinctOn: %s", v.Name)
			}
		}
	}
}

func TestDerivedFields(t *testing.T) {
	b := buildWith(t, nil, nil)
	obj, ok := b.Objects["Place"]
	if !ok {
		t.Fatal("Place type missing")
	}
	want := map[string]string{
		"locationArea":         "Float",
		"locationCentroid":     "GeoJSON",
		"locationEnvelope":     "GeoJSON",
		"locationSrid":         "Int",
		"locationGeometryType": "String",
		"locationIsValid":      "Boolean",
		"locationX":            "Float",
		"locationY":            "Float",
		"locationNumPoints":    "Int",
		"locationWkt":          "String",
		"locationEwkt":         "String",
		"locationXmin":         "Float",
		"areaArea":             "Float",
		"areaPointOnSurface":   "GeoJSON",
		"areaIsEmpty":          "Boolean",
	}
	for name, typ := range want {
		f := obj.Field(name)
		if f == nil {
			t.Fatalf("derived field %q missing", name)
		}
		if f.Type != typ {
			t.Errorf("%s type = %q, want %q", name, f.Type, typ)
		}
		if f.Meta == nil || f.Meta.Kind != schema.KindSpatialDerived {
			t.Errorf("%s has wrong meta kind", name)
		}
		if f.Meta.Column == nil {
			t.Errorf("%s is not bound to a column", name)
		}
	}
}

// Accessors that are always null for a column's typmod are not generated;
// unconstrained columns keep every accessor.
func TestDerivedFieldsGatedByTypmod(t *testing.T) {
	b := buildWith(t, nil, func(cat *introspect.Catalog) {
		places := cat.Table("public", "places")
		places.Columns = append(places.Columns,
			&introspect.Column{Name: "route", Position: 6, PGType: "geometry", TypeSchema: "public",
				Spatial: &introspect.SpatialType{Subtype: introspect.SpatialLineString, SRID: 4326}},
			&introspect.Column{Name: "peak", Position: 7, PGType: "geography", TypeSchema: "public",
				Spatial: &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326, HasZ: true}},
			&introspect.Column{Name: "legacy", Position: 8, PGType: "geometry", TypeSchema: "public"},
		)
	})
	obj := b.Objects["Place"]
	present := []string{
		"locationX", "locationY", "locationArea",
		"areaX", "areaY", "areaZ", "areaM", "areaStartPoint", "areaEndPoint",
		"routeStartPoint", "routeEndPoint", "routeLength",
		"peakX", "peakY", "peakZ",
		"legacyX", "legacyM", "legacyStartPoint",
	}
	absent := []string{
		"locationZ", "locationM", "locationStartPoint", "locationEndPoint",
		"routeX", "routeY", "routeZ", "routeM",
		"peakM", "peakStartPoint",
	}
	for _, name := range present {
		if obj.Field(name) == nil {
			t.Errorf("accessor %q missing", name)
		}
	}
	for _, name := range absent {
		if obj.Field(name) != nil {
			t.Errorf("accessor %q generated although the typmod rules it out", name)
		}
	}
}

// A real column must keep its name rather than being shadowed by an accessor.
func TestDerivedFieldDoesNotShadowRealColumn(t *testing.T) {
	b := buildWith(t, nil, func(cat *introspect.Catalog) {
		places := cat.Table("public", "places")
		places.Columns = append(places.Columns, &introspect.Column{
			Name: "location_srid", Position: 5, PGType: "text", TypeSchema: "pg_catalog",
		})
	})
	f := b.Objects["Place"].Field("locationSrid")
	if f == nil {
		t.Fatal("locationSrid missing")
	}
	if f.Meta == nil || f.Meta.Kind != schema.KindColumn {
		t.Errorf("real column was shadowed by the derived accessor: kind=%v", f.Meta.Kind)
	}
}

func TestProjectionArgs(t *testing.T) {
	b := buildWith(t, nil, nil)
	f := b.Objects["Place"].Field("location")
	if f == nil {
		t.Fatal("location field missing")
	}
	args := map[string]string{}
	for _, a := range f.Args {
		args[a.Name] = a.Type
	}
	if args["transform"] != "Int" {
		t.Errorf("transform arg = %q, want Int", args["transform"])
	}
	if args["simplify"] != "Float" {
		t.Errorf("simplify arg = %q, want Float", args["simplify"])
	}
}

func TestDistanceOrdering(t *testing.T) {
	b := buildWith(t, nil, nil)
	en, ok := b.Enums["PlacesOrderBy"]
	if !ok {
		t.Fatal("PlacesOrderBy missing")
	}
	found := map[string]schema.EnumValue{}
	for _, v := range en.Values {
		found[v.Name] = v
	}
	asc, ok := found["LOCATION_DISTANCE_ASC"]
	if !ok {
		t.Fatal("LOCATION_DISTANCE_ASC missing")
	}
	if !asc.Distance || asc.Column != "location" || asc.Desc {
		t.Errorf("bad ASC binding: %+v", asc)
	}
	desc, ok := found["LOCATION_DISTANCE_DESC"]
	if !ok {
		t.Fatal("LOCATION_DISTANCE_DESC missing")
	}
	if !desc.Distance || !desc.Desc {
		t.Errorf("bad DESC binding: %+v", desc)
	}
	// Plain btree ordering of a geometry is meaningless and must be absent.
	for _, name := range []string{"LOCATION_ASC", "LOCATION_DESC", "AREA_ASC"} {
		if _, ok := found[name]; ok {
			t.Errorf("non-spatial order value %q should not exist for a spatial column", name)
		}
	}
	// The reference geometry argument must exist wherever orderBy does.
	f := b.Query.Field("allPlaces")
	if f == nil {
		t.Fatal("allPlaces missing")
	}
	var hasNear bool
	for _, a := range f.Args {
		if a.Name == "near" && a.Type == "NearInput" {
			hasNear = true
		}
	}
	if !hasNear {
		t.Error("near: NearInput argument missing from allPlaces")
	}
}

func TestSettingsToggleEachHalf(t *testing.T) {
	tests := []struct {
		setting string
		check   func(*testing.T, *schema.Builder)
	}{
		{"filters", func(t *testing.T, b *schema.Builder) {
			if _, ok := b.Inputs["GeometryFilterOps"]; ok {
				t.Error("GeometryFilterOps generated with filters disabled")
			}
		}},
		{"derived", func(t *testing.T, b *schema.Builder) {
			if b.Objects["Place"].Field("locationArea") != nil {
				t.Error("derived accessors generated with derived disabled")
			}
		}},
		{"ordering", func(t *testing.T, b *schema.Builder) {
			for _, v := range b.Enums["PlacesOrderBy"].Values {
				if v.Distance {
					t.Error("distance ordering generated with ordering disabled")
				}
			}
		}},
		{"projection", func(t *testing.T, b *schema.Builder) {
			for _, a := range b.Objects["Place"].Field("location").Args {
				if a.Name == "transform" {
					t.Error("projection args generated with projection disabled")
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.setting, func(t *testing.T) {
			tc.check(t, buildWith(t, map[string]any{tc.setting: false}, nil))
		})
	}
}

func TestSmartCommentOmitsSurface(t *testing.T) {
	b := buildWith(t, nil, func(cat *introspect.Catalog) {
		cat.Table("public", "places").Column("location").Comment = "@omit filter,order"
	})
	if f := b.Inputs["PlaceFilter"].Field("location"); f != nil {
		t.Error("@omit filter did not remove the spatial filter field")
	}
	for _, v := range b.Enums["PlacesOrderBy"].Values {
		if v.Distance && v.Column == "location" {
			t.Error("@omit order did not remove the distance ordering values")
		}
	}
}

// A catalog with no spatial column must be left completely untouched, so
// non-PostGIS databases keep their existing schema.
func TestInertWithoutSpatialColumns(t *testing.T) {
	cat := testutil.FixtureCatalog()
	cat.Tables = removeTable(cat.Tables, "places")

	plain := schema.New(cat, inflect.Next(inflect.Default), schema.Options{Logger: slog.Default(), Functions: true})
	before, err := plain.Build()
	if err != nil {
		t.Fatal(err)
	}

	// The catalog must be trimmed before New: the builder generates types
	// eagerly from the catalog it is constructed with.
	spatialFree := testutil.FixtureCatalog()
	spatialFree.Tables = removeTable(spatialFree.Tables, "places")
	withPlugin := schema.New(spatialFree, inflect.Next(inflect.Default), schema.Options{Logger: slog.Default(), Functions: true})
	if err := New(nil).TransformSchema(context.Background(), withPlugin); err != nil {
		t.Fatal(err)
	}
	after, err := withPlugin.Build()
	if err != nil {
		t.Fatal(err)
	}
	if before.SDL != after.SDL {
		t.Error("the plugin changed the schema of a database with no spatial columns")
	}
	if strings.Contains(after.SDL, "GeoJSON") {
		t.Error("GeoJSON scalar declared without any spatial column")
	}
}

func removeTable(tables []*introspect.Table, name string) []*introspect.Table {
	out := make([]*introspect.Table, 0, len(tables))
	for _, t := range tables {
		if t.Name != name {
			out = append(out, t)
		}
	}
	return out
}

func TestTypedGeometryFieldTypes(t *testing.T) {
	b := buildWith(t, map[string]any{"typed_geometry": true}, func(cat *introspect.Catalog) {
		places := cat.Table("public", "places")
		places.Columns = append(places.Columns,
			&introspect.Column{Name: "route", Position: 6, PGType: "geometry", TypeSchema: "public",
				Spatial: &introspect.SpatialType{Subtype: introspect.SpatialMultiLineString, SRID: 4326, HasZ: true, HasM: true}},
			&introspect.Column{Name: "shape", Position: 7, PGType: "geometry", TypeSchema: "public",
				Spatial: &introspect.SpatialType{Subtype: introspect.SpatialGeometry, SRID: 4326, HasM: true}},
			&introspect.Column{Name: "legacy", Position: 8, PGType: "geometry", TypeSchema: "public"},
			&introspect.Column{Name: "history", Position: 9, PGType: "geometry", TypeSchema: "public", IsArray: true},
		)
	})
	obj := b.Objects["Place"]
	for field, want := range map[string]string{
		"location": "GeometryPoint",
		"area":     "GeographyInterface",
		"route":    "GeometryMultiLineStringZM",
		"shape":    "GeometryGeometryM",
		"legacy":   "GeometryInterface",
		"history":  "[GeoJSON!]",
	} {
		if got := obj.Field(field).Type; got != want {
			t.Errorf("%s type = %q, want %q", field, got, want)
		}
	}
	if got := obj.Field("locationCentroid").Type; got != "GeoJSON" {
		t.Errorf("derived accessors stay GeoJSON, got %q", got)
	}
	if got := b.Inputs["PlaceCreateInput"].Field("location").Type; got != "GeoJSON" {
		t.Errorf("input stays GeoJSON, got %q", got)
	}
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"interface GeometryInterface {",
		"interface GeographyGeometryZ {",
		"type GeometryPoint implements GeometryGeometry & GeometryInterface {",
		"type GeographyPointZ implements GeographyGeometryZ & GeographyInterface {",
		"  longitude: Float!\n  latitude: Float!\n  height: Float!\n",
		"  exterior: GeometryLineStringZ\n  interiors: [GeometryLineStringZ]\n",
		"  geometries: [GeometryGeometryM]\n",
		"  polygons: [GeographyPolygon]\n",
		"  lines: [GeometryLineStringZM]\n",
	} {
		if !strings.Contains(built.SDL, want) {
			t.Errorf("SDL missing %q", want)
		}
	}
}

// typed_geometry defaults to off and must leave the schema untouched.
func TestTypedGeometryOffIsIdentical(t *testing.T) {
	sdl := func(settings map[string]any) string {
		t.Helper()
		built, err := buildWith(t, settings, nil).Build()
		if err != nil {
			t.Fatal(err)
		}
		return built.SDL
	}
	def := sdl(nil)
	if off := sdl(map[string]any{"typed_geometry": false}); off != def {
		t.Error("typed_geometry: false changed the schema")
	}
	if strings.Contains(def, "GeometryInterface") || strings.Contains(def, "GeometryPoint") {
		t.Error("typed geometry types generated without typed_geometry")
	}
}

// A user type already owning a geometry type name keeps it; that flavour
// falls back to the GeoJSON scalar.
func TestTypedGeometryNameClash(t *testing.T) {
	b := buildWith(t, nil, nil)
	b.Objects["GeometryPoint"] = &schema.Object{Name: "GeometryPoint"}
	if err := New(map[string]any{"typed_geometry": true}).TransformSchema(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if got := b.Objects["Place"].Field("location").Type; got != "GeoJSON" {
		t.Errorf("clashing flavour retyped location to %q", got)
	}
	if got := b.Objects["Place"].Field("area").Type; got != "GeographyInterface" {
		t.Errorf("geography flavour should still be typed, got %q", got)
	}
}
