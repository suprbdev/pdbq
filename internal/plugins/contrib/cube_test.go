package contrib

import (
	"bytes"
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribfixture"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func TestCubeEarthTypeMapping(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	obj := b.Objects["Location"]
	for field, want := range map[string]string{"pos": "Cube", "box": "Cube", "spot": "LatLon", "home": "LatLon", "here": "String"} {
		if got := obj.Field(field).Type; got != want {
			t.Errorf("Location.%s = %s, want %s", field, got, want)
		}
	}
	for field, want := range map[string]string{"pos": "Cube", "spot": "LatLonInput", "here": "String"} {
		if got := fieldType(t, b, "LocationInput", field); got != want {
			t.Errorf("LocationInput.%s = %s, want %s", field, got, want)
		}
	}
	sdl := b.SDL()
	if !strings.Contains(sdl, "scalar Cube") || b.Objects["LatLon"] == nil || b.Inputs["LatLonInput"] == nil {
		t.Error("Cube, LatLon or LatLonInput not declared")
	}
	if got := b.Inputs["LatLonFilterOps"].Fields; len(got) != 1 || got[0].Name != "isNull" {
		t.Errorf("LatLonFilterOps fields: %+v", got)
	}
	for _, op := range []string{"equalTo", "notEqualTo", "isNull", "contains", "containedBy", "overlaps"} {
		if fieldType(t, b, "CubeFilterOps", op) == "" {
			t.Errorf("CubeFilterOps.%s missing", op)
		}
	}
	if got := fieldType(t, b, "CubeFilterOps", "contains"); got != "Cube" {
		t.Errorf("CubeFilterOps.contains operand = %s", got)
	}
	for _, f := range []string{"pos", "spot"} {
		if c := b.Inputs["LocationCondition"]; c != nil && c.Field(f) != nil {
			t.Errorf("%s in the condition input", f)
		}
		if en := b.Enums["LocationsDistinctOn"]; en != nil && en.HasValue(strings.ToUpper(f)) {
			t.Errorf("%s in distinctOn", f)
		}
		if hasEnumValue(b, "LocationsOrderBy", strings.ToUpper(f)+"_ASC") {
			t.Errorf("plain ordering on %s", f)
		}
	}

	for ext, want := range map[string]string{"cube": "String", "earthdistance": "Cube"} {
		noExt := buildWith(t, withoutExtension(contribfixture.Catalog(), ext), nil, true, true)
		if got := noExt.Objects["Location"].Field("spot").Type; got != want {
			t.Errorf("spot without %s = %s, want %s", ext, got, want)
		}
		if strings.Contains(noExt.SDL(), "LatLon") || strings.Contains(noExt.SDL(), "nearPoint") {
			t.Errorf("earthdistance surface without %s", ext)
		}
	}
	noCube := buildWith(t, withoutExtension(contribfixture.Catalog(), "cube"), nil, true, true)
	if got := noCube.Objects["Location"].Field("pos").Type; got != "String" {
		t.Errorf("pos without cube = %s", got)
	}
}

func TestCubeEarthFilterPolicy(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	for field, want := range map[string]string{"pos": "CubeDistanceFilterOps", "box": "CubeFilterOps", "spot": "LatLonDistanceFilterOps"} {
		if got := fieldType(t, b, "LocationFilter", field); got != want {
			t.Errorf("LocationFilter.%s = %s, want %s", field, got, want)
		}
	}
	for _, field := range []string{"home", "here", "there"} {
		if got := fieldType(t, b, "LocationFilter", field); got != "" {
			t.Errorf("unindexed %s filterable as %s", field, got)
		}
	}
	for _, op := range []string{"l2DistanceLessThan", "l1DistanceLessThan", "chebyshevDistanceLessThan", "contains", "equalTo"} {
		if fieldType(t, b, "CubeDistanceFilterOps", op) == "" {
			t.Errorf("CubeDistanceFilterOps.%s missing", op)
		}
	}
	if got := fieldType(t, b, "CubeDistanceInput", "cube"); got != "Cube!" {
		t.Errorf("CubeDistanceInput.cube = %s", got)
	}
	if got := fieldType(t, b, "LatLonDistanceFilterOps", "earthDistanceLessThan"); got != "LatLonDistanceInput" {
		t.Errorf("LatLonDistanceFilterOps.earthDistanceLessThan = %s", got)
	}
	if b.Inputs["PointDistanceFilterOps"] != nil {
		t.Error("PointDistanceFilterOps declared without an admitted point column")
	}

	allowed, _ := buildAllowing(t, contribfixture.Catalog(), map[string][]string{"public.locations": {"home", "here"}})
	if got := fieldType(t, allowed, "LocationFilter", "home"); got != "LatLonDistanceFilterOps" {
		t.Errorf("allowlisted home = %s", got)
	}
	if got := fieldType(t, allowed, "LocationFilter", "here"); got != "PointDistanceFilterOps" {
		t.Errorf("allowlisted here = %s", got)
	}
	for _, op := range []string{"equalTo", "isNull", "earthDistanceMilesLessThan"} {
		if fieldType(t, allowed, "PointDistanceFilterOps", op) == "" {
			t.Errorf("PointDistanceFilterOps.%s missing", op)
		}
	}

	off := buildWith(t, contribfixture.Catalog(), nil, false, true)
	for field, want := range map[string]string{"box": "CubeDistanceFilterOps", "home": "LatLonDistanceFilterOps", "there": "PointDistanceFilterOps"} {
		if got := fieldType(t, off, "LocationFilter", field); got != want {
			t.Errorf("indexed_only off: LocationFilter.%s = %s, want %s", field, got, want)
		}
	}

	noFilters := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Filters = false }, true, true)
	if got := fieldType(t, noFilters, "LocationFilter", "pos"); got != "CubeFilterOps" {
		t.Errorf("filters: false: pos = %s", got)
	}
	if fieldType(t, noFilters, "CubeFilterOps", "contains") != "" {
		t.Error("filters: false kept the cube operators")
	}
}

func TestCubeEarthOrdering(t *testing.T) {
	b := buildWith(t, contribfixture.Catalog(), nil, true, true)
	want := []string{
		"POS_L2_DISTANCE_ASC", "POS_L2_DISTANCE_DESC", "POS_L1_DISTANCE_ASC", "POS_L1_DISTANCE_DESC",
		"POS_CHEBYSHEV_DISTANCE_ASC", "POS_CHEBYSHEV_DISTANCE_DESC",
	}
	if got := enumValues(b, "LocationsOrderBy", "POS_"); !slices.Equal(got, want) {
		t.Errorf("pos (GiST) orderings = %v", got)
	}
	if got := enumValues(b, "LocationsOrderBy", "SPOT_"); !slices.Equal(got, []string{"SPOT_EARTH_DISTANCE_ASC", "SPOT_EARTH_DISTANCE_DESC"}) {
		t.Errorf("spot (GiST) orderings = %v", got)
	}
	for _, prefix := range []string{"BOX_", "HOME_", "HERE_", "THERE_"} {
		if got := enumValues(b, "LocationsOrderBy", prefix); len(got) != 0 {
			t.Errorf("orderings without a GiST index: %v", got)
		}
	}
	for _, v := range b.Enums["LocationsOrderBy"].Values {
		if v.Name == "SPOT_EARTH_DISTANCE_ASC" && (v.Vector != "earthDistance" || v.Reference != "nearPoint") {
			t.Errorf("SPOT_EARTH_DISTANCE_ASC binding: %+v", v)
		}
		if v.Name == "POS_CHEBYSHEV_DISTANCE_ASC" && (v.Vector != "chebyshevDistance" || v.Reference != "nearVector") {
			t.Errorf("POS_CHEBYSHEV_DISTANCE_ASC binding: %+v", v)
		}
	}
	for field, want := range map[string]string{"pos": "Cube", "box": "Cube"} {
		if got := fieldType(t, b, "LocationNearVectorInput", field); got != want {
			t.Errorf("LocationNearVectorInput.%s = %q", field, got)
		}
	}
	for field, want := range map[string]string{"spot": "LatLonInput", "home": "LatLonInput", "here": ""} {
		if got := fieldType(t, b, "LocationNearPointInput", field); got != want {
			t.Errorf("LocationNearPointInput.%s = %q, want %q", field, got, want)
		}
	}
	all := b.Query.Field("allLocations")
	if !hasArg(all, "nearVector") || !hasArg(all, "nearPoint") {
		t.Error("allLocations lacks nearVector: or nearPoint:")
	}
	if hasArg(b.Query.Field("allEmbeddings"), "nearPoint") {
		t.Error("nearPoint: on a table without earth or point columns")
	}

	scores := scoreFields(b, "Location")
	for name, want := range map[string]schema.Score{
		"posL2Distance":        {Arg: "nearVector", Expr: "l2Distance"},
		"boxChebyshevDistance": {Arg: "nearVector", Expr: "chebyshevDistance"},
		"spotEarthDistance":    {Arg: "nearPoint", Expr: "earthDistance"},
		"homeEarthDistance":    {Arg: "nearPoint", Expr: "earthDistance"},
	} {
		if got, ok := scores[name]; !ok || got != want {
			t.Errorf("score %s = %+v (present %v), want %+v", name, got, ok, want)
		}
	}
	if _, ok := scores["hereEarthDistanceMiles"]; ok {
		t.Error("score on an unadmitted point column")
	}

	allowed, _ := buildAllowing(t, contribfixture.Catalog(), map[string][]string{"public.locations": {"here"}})
	if got := enumValues(allowed, "LocationsOrderBy", "HERE_EARTH"); len(got) != 2 {
		t.Errorf("allowlisted here orderings = %v", got)
	}
	if got := fieldType(t, allowed, "LocationNearPointInput", "here"); got != "LatLonInput" {
		t.Errorf("LocationNearPointInput.here = %q", got)
	}
	if _, ok := scoreFields(allowed, "Location")["hereEarthDistanceMiles"]; !ok {
		t.Error("allowlisted point column lacks its score")
	}

	noOrder := buildWith(t, contribfixture.Catalog(), func(s *Settings) { s.Ordering = false }, true, true)
	if len(enumValues(noOrder, "LocationsOrderBy", "POS_")) != 0 || hasArg(noOrder.Query.Field("allLocations"), "nearPoint") {
		t.Error("ordering: false kept the distance ordering")
	}
}

func TestPointColumnsUnchangedWithoutPolicy(t *testing.T) {
	cat := func(withEarth bool) *introspect.Catalog {
		c := contribfixture.Catalog()
		c.Tables = []*introspect.Table{contribfixture.Locations()}
		c.Tables[0].Columns = slices.DeleteFunc(c.Tables[0].Columns, func(col *introspect.Column) bool {
			return col.Domain != "" || col.PGType == "cube"
		})
		if !withEarth {
			withoutExtension(c, "earthdistance")
		}
		return c
	}
	with := buildWith(t, cat(true), nil, true, true).SDL()
	without := buildWith(t, cat(false), nil, true, true).SDL()
	if with != without {
		t.Error("earthdistance changed the schema of unadmitted point columns")
	}
	allowed, _ := buildAllowing(t, cat(true), map[string][]string{"public.locations": {"here"}})
	if allowed.Objects["LatLon"] == nil || allowed.Inputs["LatLonInput"] == nil {
		t.Error("an admitted point column without earth columns lacks LatLonInput")
	}
}

func TestCubeEarthOmit(t *testing.T) {
	for _, tag := range []string{"@omit read", "@omit filter", "@omit order"} {
		cat := contribfixture.Catalog()
		for _, c := range []string{"pos", "spot"} {
			cat.Table("public", "locations").Column(c).Comment = tag
		}
		b := buildWith(t, cat, nil, true, true)
		read, filter, order := tag == "@omit read", tag == "@omit filter", tag == "@omit order"
		for _, prefix := range []string{"POS_", "SPOT_"} {
			if got := len(enumValues(b, "LocationsOrderBy", prefix)) > 0; got == (read || order) {
				t.Errorf("%s: %s orderings present = %v", tag, prefix, got)
			}
		}
		if got := fieldType(t, b, "LocationFilter", "pos") == "CubeDistanceFilterOps"; got == (read || filter) {
			t.Errorf("%s: pos thresholds present = %v", tag, got)
		}
		if got := fieldType(t, b, "LocationFilter", "spot") == "LatLonDistanceFilterOps"; got == (read || filter) {
			t.Errorf("%s: spot radius filter present = %v", tag, got)
		}
		obj := b.Objects["Location"]
		if got := obj.Field("posL2Distance") != nil || obj.Field("spotEarthDistance") != nil; got == read {
			t.Errorf("%s: distance fields present = %v", tag, got)
		}
		if in := b.Inputs["LocationNearPointInput"]; (in != nil && in.Field("spot") != nil) == read {
			t.Errorf("%s: nearPoint field for spot present = %v", tag, !read)
		}
	}
}

func TestCubeEarthNamesTaken(t *testing.T) {
	for _, tc := range []struct {
		typ   string
		check func(*schema.Builder) bool
	}{
		{"LocationNearPointInput", func(b *schema.Builder) bool {
			return len(enumValues(b, "LocationsOrderBy", "SPOT_")) == 0 && !hasArg(b.Query.Field("allLocations"), "nearPoint")
		}},
		{"CubeDistanceFilterOps", func(b *schema.Builder) bool {
			return fieldType(t, b, "LocationFilter", "pos") == "CubeFilterOps"
		}},
		{"LatLonDistanceInput", func(b *schema.Builder) bool {
			return fieldType(t, b, "LocationFilter", "spot") == "LatLonFilterOps"
		}},
	} {
		var logs bytes.Buffer
		b := schema.New(contribfixture.Catalog(), inflect.Next(inflect.Default), schema.Options{
			Logger: slog.New(slog.NewTextHandler(&logs, nil)), FilterIndexedOnly: true,
		})
		b.Inputs[tc.typ] = &schema.Input{Name: tc.typ}
		if err := New(DefaultSettings()).TransformSchema(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		if !tc.check(b) {
			t.Errorf("%s taken: surface still added", tc.typ)
		}
		if !strings.Contains(logs.String(), tc.typ) {
			t.Errorf("%s taken: no warning: %s", tc.typ, logs.String())
		}
	}

	cat := contribfixture.Catalog()
	cat.Tables = append([]*introspect.Table{{
		Schema: "public", Name: "lat_lon", Kind: introspect.RelTable,
		Privileges: introspect.Privileges{Select: true},
		Columns:    []*introspect.Column{{Name: "id", Position: 1, PGType: "int4", TypeSchema: "pg_catalog"}},
	}}, cat.Tables...)
	b, logs := buildAllowing(t, cat, map[string][]string{"public.locations": {"here"}})
	if got := b.Objects["Location"].Field("spot").Type; got != "String" {
		t.Errorf("spot with LatLon taken = %s, want String", got)
	}
	if b.Inputs["LocationNearPointInput"] != nil || hasArg(b.Query.Field("allLocations"), "nearPoint") {
		t.Error("nearPoint: added although LatLonInput cannot be declared")
	}
	if !strings.Contains(logs, "LatLon") {
		t.Errorf("no warning: %s", logs)
	}
}
