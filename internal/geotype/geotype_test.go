package geotype

import (
	"testing"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

func TestAllRoundTrips(t *testing.T) {
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		all := All(f)
		if len(all) != 1+4+7*4 {
			t.Fatalf("%s: %d types", f, len(all))
		}
		for _, gt := range all {
			back, ok := Parse(gt.Name())
			if !ok || back != gt {
				t.Errorf("Parse(%q) = %+v, %v", gt.Name(), back, ok)
			}
		}
	}
	if _, ok := Parse("GeoJSON"); ok {
		t.Error("the scalar is not a geometry type")
	}
}

func TestForColumn(t *testing.T) {
	col := func(pg string, st *introspect.SpatialType) *introspect.Column {
		return &introspect.Column{PGType: pg, Spatial: st}
	}
	tests := []struct {
		col  *introspect.Column
		want string
	}{
		{col("geometry", nil), "GeometryInterface"},
		{col("geography", &introspect.SpatialType{Subtype: introspect.SpatialGeometry}), "GeographyInterface"},
		{col("geometry", &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326}), "GeometryPoint"},
		{col("geometry", &introspect.SpatialType{Subtype: introspect.SpatialPolygon, HasZ: true}), "GeometryPolygonZ"},
		{col("geography", &introspect.SpatialType{Subtype: introspect.SpatialPoint, SRID: 4326, HasM: true}), "GeographyPointM"},
		{col("geometry", &introspect.SpatialType{Subtype: introspect.SpatialGeometry, SRID: 4326}), "GeometryGeometry"},
		{col("geometry", &introspect.SpatialType{Subtype: introspect.SpatialGeometry, SRID: 4326, HasZ: true, HasM: true}), "GeometryGeometryZM"},
		{col("geometry", &introspect.SpatialType{Subtype: "CircularString", SRID: 4326, HasZ: true}), "GeometryGeometryZ"},
	}
	for _, tc := range tests {
		if got := ForColumn(tc.col).Name(); got != tc.want {
			t.Errorf("%s %+v: got %s, want %s", tc.col.PGType, tc.col.Spatial, got, tc.want)
		}
	}
}

func TestPointCoordinates(t *testing.T) {
	pz := Type{Flavour: postgis.Geometry, Kind: Point, Dims: XYZ}
	if i, ok := pz.CoordIndex("z"); !ok || i != 2 {
		t.Errorf("z on PointZ = %d, %v", i, ok)
	}
	pm := Type{Flavour: postgis.Geography, Kind: Point, Dims: XYM}
	if _, ok := pm.CoordIndex("height"); ok {
		t.Error("PointM has no height")
	}
	if i, ok := pm.CoordIndex("latitude"); !ok || i != 1 {
		t.Errorf("latitude = %d, %v", i, ok)
	}
}
