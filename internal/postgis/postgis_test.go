package postgis

import (
	"strings"
	"testing"
)

func TestFlavourOf(t *testing.T) {
	tests := []struct {
		pgType string
		want   Flavour
	}{
		{"geometry", Geometry},
		{"geography", Geography},
		{"_geometry", Geometry}, // array element type
		{"_geography", Geography},
		{"text", NotSpatial},
		{"jsonb", NotSpatial},
		{"", NotSpatial},
		{"geometry_columns", NotSpatial}, // the PostGIS metadata view, not a type
	}
	for _, tc := range tests {
		if got := FlavourOf(tc.pgType); got != tc.want {
			t.Errorf("FlavourOf(%q) = %v, want %v", tc.pgType, got, tc.want)
		}
	}
}

func TestParseGeoJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      any
		flavour Flavour
		wantSQL string
		wantArg string
		wantErr string
	}{
		{
			name:    "point",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}},
			flavour: Geometry,
			wantSQL: "ST_SetSRID(ST_GeomFromGeoJSON($1), 4326)",
			wantArg: `{"coordinates":[1,2],"type":"Point"}`,
		},
		{
			name:    "geography casts",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}},
			flavour: Geography,
			wantSQL: "(ST_SetSRID(ST_GeomFromGeoJSON($1), 4326))::geography",
		},
		{
			name: "feature unwrapped to its geometry",
			in: map[string]any{
				"type":     "Feature",
				"geometry": map[string]any{"type": "Point", "coordinates": []any{3.0, 4.0}},
			},
			flavour: Geometry,
			wantArg: `{"coordinates":[3,4],"type":"Point"}`,
		},
		{
			name: "feature collection becomes a geometry collection",
			in: map[string]any{
				"type": "FeatureCollection",
				"features": []any{
					map[string]any{"type": "Feature", "geometry": map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}},
				},
			},
			flavour: Geometry,
			wantArg: `{"geometries":[{"coordinates":[1,2],"type":"Point"}],"type":"GeometryCollection"}`,
		},
		{
			name:    "explicit EPSG CRS is honoured",
			in:      map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}, "crs": map[string]any{"properties": map[string]any{"name": "EPSG:3857"}}},
			flavour: Geometry,
			wantSQL: "ST_SetSRID(ST_GeomFromGeoJSON($1), 3857)",
		},
		{
			name:    "missing type",
			in:      map[string]any{"coordinates": []any{1.0, 2.0}},
			flavour: Geometry,
			wantErr: "type",
		},
		{
			name:    "missing coordinates",
			in:      map[string]any{"type": "Point"},
			flavour: Geometry,
			wantErr: "coordinates",
		},
		{
			name:    "unknown geojson type",
			in:      map[string]any{"type": "Wormhole", "coordinates": []any{1.0}},
			flavour: Geometry,
			wantErr: "unknown GeoJSON type",
		},
		{
			name:    "wrong Go type",
			in:      42,
			flavour: Geometry,
			wantErr: "GeoJSON object or a WKT string",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in, tc.flavour)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantArg != "" && got.Param != tc.wantArg {
				t.Errorf("param = %q, want %q", got.Param, tc.wantArg)
			}
			if tc.wantSQL != "" {
				if sql := got.SQL("$1"); sql != tc.wantSQL {
					t.Errorf("SQL = %q, want %q", sql, tc.wantSQL)
				}
			}
		})
	}
}

func TestParseWKT(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		flavour Flavour
		wantSQL string
		wantErr bool
	}{
		{name: "plain point", in: "POINT(1 2)", flavour: Geometry, wantSQL: "ST_GeomFromEWKT($1)"},
		{name: "ewkt keeps its own srid", in: "SRID=4326;POINT(1 2)", flavour: Geometry, wantSQL: "ST_GeomFromEWKT($1)"},
		{name: "lowercase accepted", in: "polygon((0 0,1 0,1 1,0 0))", flavour: Geometry, wantSQL: "ST_GeomFromEWKT($1)"},
		{name: "3d point", in: "POINT Z (1 2 3)", flavour: Geometry, wantSQL: "ST_GeomFromEWKT($1)"},
		{name: "empty geometry", in: "POINT EMPTY", flavour: Geometry, wantSQL: "ST_GeomFromEWKT($1)"},
		{
			// Bare WKT is SRID 0; geography needs a real CRS, so 4326 is forced.
			name: "bare wkt on geography gets a srid", in: "POINT(1 2)", flavour: Geography,
			wantSQL: "(ST_SetSRID(ST_GeomFromEWKT($1), 4326))::geography",
		},
		{name: "empty string", in: "", flavour: Geometry, wantErr: true},
		{name: "not wkt at all", in: "DROP TABLE users", flavour: Geometry, wantErr: true},
		{name: "sql fragment", in: "1=1); DROP TABLE users --", flavour: Geometry, wantErr: true},
		{name: "bare number", in: "42", flavour: Geometry, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.in, tc.flavour)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if sql := got.SQL("$1"); sql != tc.wantSQL {
				t.Errorf("SQL = %q, want %q", sql, tc.wantSQL)
			}
			// The value itself must never appear in the SQL text.
			if strings.Contains(got.SQL("$1"), strings.TrimSpace(tc.in)) {
				t.Errorf("SQL %q leaked the input value", got.SQL("$1"))
			}
		})
	}
}

// TestSQLTextIsValueIndependent is the package-level mirror of the compiler's
// FuzzFilter invariant: two different values of the same shape must produce
// identical SQL text.
func TestSQLTextIsValueIndependent(t *testing.T) {
	a, err := Parse(map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}, Geometry)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(map[string]any{"type": "Point", "coordinates": []any{99.0, -3.5}}, Geometry)
	if err != nil {
		t.Fatal(err)
	}
	if a.SQL("$1") != b.SQL("$1") {
		t.Errorf("SQL text varies with value: %q vs %q", a.SQL("$1"), b.SQL("$1"))
	}
	if a.Param == b.Param {
		t.Errorf("distinct values collapsed to the same parameter")
	}
}

func TestUnwrapDepthIsBounded(t *testing.T) {
	// A Feature chain deeper than maxUnwrapDepth must be rejected rather than
	// recursing without bound.
	doc := map[string]any{"type": "Point", "coordinates": []any{1.0, 2.0}}
	for range maxUnwrapDepth + 2 {
		doc = map[string]any{"type": "Feature", "geometry": doc}
	}
	if _, err := Parse(doc, Geometry); err == nil {
		t.Fatal("expected an error for deeply nested GeoJSON")
	}
}

func TestOpsRender(t *testing.T) {
	tests := []struct {
		op   string
		want string
	}{
		{"bboxIntersects", `"t"."geom" && $1`},
		{"bboxContains", `"t"."geom" ~ $1`},
		{"bboxContainedBy", `"t"."geom" @ $1`},
		{"intersects", `ST_Intersects("t"."geom", $1)`},
		{"within", `ST_Within("t"."geom", $1)`},
		{"contains", `ST_Contains("t"."geom", $1)`},
		{"equals", `ST_Equals("t"."geom", $1)`},
	}
	for _, tc := range tests {
		op, ok := OpByName(tc.op)
		if !ok {
			t.Fatalf("operator %q not found", tc.op)
		}
		if got := op.Render(`"t"."geom"`, "$1"); got != tc.want {
			t.Errorf("%s rendered %q, want %q", tc.op, got, tc.want)
		}
	}
}

func TestDWithinRender(t *testing.T) {
	op, ok := OpByName("dwithin")
	if !ok {
		t.Fatal("dwithin not found")
	}
	want := `ST_DWithin("t"."geom", $1, $2)`
	if got := op.Render(`"t"."geom"`, "$1", "$2"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	beyond, _ := OpByName("beyond")
	wantBeyond := `NOT ST_DWithin("t"."geom", $1, $2)`
	if got := beyond.Render(`"t"."geom"`, "$1", "$2"); got != wantBeyond {
		t.Errorf("got %q, want %q", got, wantBeyond)
	}
}

func TestOpKinds(t *testing.T) {
	tests := map[string]ArgKind{
		"bboxIntersects": ArgBBox,
		"dwithin":        ArgDWithin,
		"beyond":         ArgDWithin,
		"isNull":         ArgBoolean,
		"intersects":     ArgGeometry,
	}
	for name, want := range tests {
		op, ok := OpByName(name)
		if !ok {
			t.Fatalf("operator %q not found", name)
		}
		if got := op.Kind(); got != want {
			t.Errorf("%s kind = %v, want %v", name, got, want)
		}
	}
}

func TestBBoxSQL(t *testing.T) {
	got := BBoxSQL("$1", "$2", "$3", "$4", 0, Geometry)
	want := "ST_MakeEnvelope($1, $2, $3, $4, 4326)" // 0 defaults to WGS84
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := BBoxSQL("$1", "$2", "$3", "$4", 3857, Geography); !strings.HasSuffix(got, "::geography") {
		t.Errorf("geography box not cast: %q", got)
	}
}

func TestOutputSQLEvaluatesRefOnce(t *testing.T) {
	// ST_AsGeoJSON is STRICT and NULL::jsonb is NULL, so no CASE guard is
	// needed — and ref may be a computed-column function call, so it must
	// appear exactly once to avoid double evaluation.
	ref := `"t"."geom"`
	got := OutputSQL(ref)
	for _, want := range []string{"ST_AsGeoJSON", "::jsonb"} {
		if !strings.Contains(got, want) {
			t.Errorf("OutputSQL missing %q: %s", want, got)
		}
	}
	if n := strings.Count(got, ref); n != 1 {
		t.Errorf("OutputSQL references ref %d times, want 1: %s", n, got)
	}
}

func TestDerivedGeographyCasts(t *testing.T) {
	// Accessors without a geography overload must cast to geometry so they do
	// not fail at execution time.
	for _, suffix := range []string{"Centroid", "Envelope", "GeometryType", "IsValid", "Perimeter"} {
		d, ok := DerivedBySuffix(suffix)
		if !ok {
			t.Fatalf("derived %q not found", suffix)
		}
		if got := d.SQL(`"t"."g"`, Geography); !strings.Contains(got, "::geometry") {
			t.Errorf("%s on geography is not cast: %s", suffix, got)
		}
	}
	// Area and Length do have geography overloads (and mean metres there), so
	// they must NOT be cast away.
	for _, suffix := range []string{"Area", "Length"} {
		d, _ := DerivedBySuffix(suffix)
		if got := d.SQL(`"t"."g"`, Geography); strings.Contains(got, "::geometry") {
			t.Errorf("%s on geography should keep spheroidal units: %s", suffix, got)
		}
	}
}

func TestTransformAndSimplify(t *testing.T) {
	if got := TransformSQL(`"t"."g"`, 0, Geometry); got != `"t"."g"` {
		t.Errorf("a zero SRID must be a no-op, got %q", got)
	}
	if got := TransformSQL(`"t"."g"`, 3857, Geometry); got != `ST_Transform("t"."g", 3857)` {
		t.Errorf("got %q", got)
	}
	if got := SimplifySQL(`"t"."g"`, "$1", Geometry); got != `ST_SimplifyPreserveTopology("t"."g", $1)` {
		t.Errorf("got %q", got)
	}
}
