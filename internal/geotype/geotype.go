// Package geotype models the typed geometry objects the postgis plugin
// generates when plugins.settings.postgis.typed_geometry is on, following the
// shape of `@graphile/postgis`:
//
//	interface GeometryInterface { geojson: GeoJSON, srid: Int! }
//	interface GeometryGeometry / GeometryGeometryZ / ...M / ...ZM (same fields)
//	type GeometryPoint implements GeometryGeometry & GeometryInterface { geojson srid x y }
//	type GeometryLineString ... { points: [GeometryPoint] }
//	type GeometryPolygon ... { exterior: GeometryLineString, interiors: [GeometryLineString] }
//	type GeometryMultiPoint / MultiLineString / MultiPolygon / GeometryCollection ...
//
// with Z, M and ZM variants of every concrete type, and the same family
// prefixed Geography for geography columns (whose points expose longitude,
// latitude and height instead of x, y and z).
//
// The schema plugin and the SQL compiler both derive names and fields from
// this package, so the generated SDL and the compiled JSON cannot drift.
package geotype

import (
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// Kind is the geometry type of a concrete object type, named as GeoJSON names
// it. The zero value marks an interface.
type Kind int

const (
	// Interface is not a concrete geometry type: the value's runtime type is
	// one of the concrete types implementing it.
	Interface Kind = iota
	Point
	LineString
	Polygon
	MultiPoint
	MultiLineString
	MultiPolygon
	GeometryCollection
)

var kindNames = [...]string{"", "Point", "LineString", "Polygon", "MultiPoint", "MultiLineString", "MultiPolygon", "GeometryCollection"}

// Kinds lists the concrete kinds in declaration order.
var Kinds = []Kind{Point, LineString, Polygon, MultiPoint, MultiLineString, MultiPolygon, GeometryCollection}

// GeoJSON returns the GeoJSON "type" member for the kind ("" for Interface).
func (k Kind) GeoJSON() string { return kindNames[k] }

// Dims is the coordinate dimension of a geometry.
type Dims int

const (
	XY Dims = iota
	XYZ
	XYM
	XYZM
)

// AllDims lists the dimensions in the order their types are generated.
var AllDims = []Dims{XY, XYZ, XYM, XYZM}

// Suffix is the type-name suffix for the dimension: "", "Z", "M" or "ZM".
func (d Dims) Suffix() string {
	switch d {
	case XYZ:
		return "Z"
	case XYM:
		return "M"
	case XYZM:
		return "ZM"
	}
	return ""
}

// HasZ reports whether the dimension carries a Z coordinate.
func (d Dims) HasZ() bool { return d == XYZ || d == XYZM }

// DimsOf combines Z and M flags into a Dims.
func DimsOf(hasZ, hasM bool) Dims {
	switch {
	case hasZ && hasM:
		return XYZM
	case hasZ:
		return XYZ
	case hasM:
		return XYM
	}
	return XY
}

// Type is one generated GraphQL type: a concrete geometry type, a
// per-dimension interface (Kind Interface), or the top-level interface every
// type of the flavour implements (Kind Interface with AnyDims).
type Type struct {
	Flavour postgis.Flavour
	Kind    Kind
	Dims    Dims
	// AnyDims marks the top-level interface (GeometryInterface,
	// GeographyInterface), whose implementations span every dimension.
	AnyDims bool
}

func prefix(f postgis.Flavour) string {
	if f == postgis.Geography {
		return "Geography"
	}
	return "Geometry"
}

// Prefix is the flavour's type-name prefix: "Geometry" or "Geography".
func (t Type) Prefix() string { return prefix(t.Flavour) }

// Name is the GraphQL type name, e.g. GeometryPointZ, GeographyGeometryM,
// GeometryInterface.
func (t Type) Name() string {
	p := prefix(t.Flavour)
	switch {
	case t.Kind != Interface:
		return p + t.Kind.GeoJSON() + t.Dims.Suffix()
	case t.AnyDims:
		return p + "Interface"
	}
	return p + "Geometry" + t.Dims.Suffix()
}

// Concrete reports whether the type is an object type rather than an
// interface.
func (t Type) Concrete() bool { return t.Kind != Interface }

// Possible lists the concrete types a value of type t can have at runtime.
func (t Type) Possible() []Type {
	if t.Concrete() {
		return []Type{t}
	}
	dims := []Dims{t.Dims}
	if t.AnyDims {
		dims = AllDims
	}
	var out []Type
	for _, d := range dims {
		for _, k := range Kinds {
			out = append(out, Type{Flavour: t.Flavour, Kind: k, Dims: d})
		}
	}
	return out
}

// Interfaces lists the interfaces a concrete type implements: its dimension
// interface and the flavour's top-level interface.
func (t Type) Interfaces() []Type {
	if !t.Concrete() {
		return nil
	}
	return []Type{
		{Flavour: t.Flavour, Dims: t.Dims},
		{Flavour: t.Flavour, AnyDims: true},
	}
}

// Member is the type of the elements a field of t holds (points, lines,
// polygons, geometries, exterior, interiors). ok is false for fields without
// a geometry-typed value.
func (t Type) Member(field string) (Type, bool) {
	elem := func(k Kind) (Type, bool) { return Type{Flavour: t.Flavour, Kind: k, Dims: t.Dims}, true }
	switch {
	case field == "points" && (t.Kind == LineString || t.Kind == MultiPoint):
		return elem(Point)
	case (field == "exterior" || field == "interiors") && t.Kind == Polygon:
		return elem(LineString)
	case field == "lines" && t.Kind == MultiLineString:
		return elem(LineString)
	case field == "polygons" && t.Kind == MultiPolygon:
		return elem(Polygon)
	case field == "geometries" && t.Kind == GeometryCollection:
		return elem(Interface)
	}
	return Type{}, false
}

// CoordIndex maps a point coordinate field to its position in a GeoJSON
// coordinate array: x/longitude 0, y/latitude 1, z/height 2.
func (t Type) CoordIndex(field string) (int, bool) {
	if t.Kind != Point {
		return 0, false
	}
	names := coordNames(t.Flavour)
	for i, n := range names {
		if i == 2 && !t.Dims.HasZ() {
			break
		}
		if n == field {
			return i, true
		}
	}
	return 0, false
}

func coordNames(f postgis.Flavour) [3]string {
	if f == postgis.Geography {
		return [3]string{"longitude", "latitude", "height"}
	}
	return [3]string{"x", "y", "z"}
}

// Field is one field of a generated type.
type Field struct {
	Name        string
	Type        string
	Description string
}

// Fields lists the fields of t in SDL order.
func (t Type) Fields() []Field {
	out := []Field{
		{Name: "geojson", Type: postgis.TypeName, Description: "Converts the object to GeoJSON"},
		{Name: "srid", Type: "Int!", Description: "Spatial reference identifier (SRID)"},
	}
	list := func(name string) Field {
		m, _ := t.Member(name)
		return Field{Name: name, Type: "[" + m.Name() + "]"}
	}
	switch t.Kind {
	case Point:
		names := coordNames(t.Flavour)
		out = append(out, Field{Name: names[0], Type: "Float!"}, Field{Name: names[1], Type: "Float!"})
		if t.Dims.HasZ() {
			out = append(out, Field{Name: names[2], Type: "Float!"})
		}
	case LineString, MultiPoint:
		out = append(out, list("points"))
	case Polygon:
		ext, _ := t.Member("exterior")
		out = append(out, Field{Name: "exterior", Type: ext.Name()}, list("interiors"))
	case MultiLineString:
		out = append(out, list("lines"))
	case MultiPolygon:
		out = append(out, list("polygons"))
	case GeometryCollection:
		out = append(out, list("geometries"))
	}
	return out
}

// Description documents t in the generated schema.
func (t Type) Description() string {
	word := "geometry"
	if t.Flavour == postgis.Geography {
		word = "geography"
	}
	switch {
	case t.AnyDims:
		return "All " + word + " types implement this interface"
	case t.Kind == Interface:
		return "All " + word + " XY" + t.Dims.Suffix() + " types implement this interface"
	}
	return ""
}

// All lists every type of a flavour: the top-level interface, the four
// dimension interfaces, then the concrete types.
func All(f postgis.Flavour) []Type {
	out := []Type{{Flavour: f, AnyDims: true}}
	for _, d := range AllDims {
		out = append(out, Type{Flavour: f, Dims: d})
	}
	return append(out, Type{Flavour: f, AnyDims: true}.Possible()...)
}

var byName = func() map[string]Type {
	m := map[string]Type{}
	for _, f := range []postgis.Flavour{postgis.Geometry, postgis.Geography} {
		for _, t := range All(f) {
			m[t.Name()] = t
		}
	}
	return m
}()

// Parse resolves a generated type name.
func Parse(name string) (Type, bool) {
	t, ok := byName[name]
	return t, ok
}

var kindBySubtype = map[introspect.SpatialSubtype]Kind{
	introspect.SpatialPoint:              Point,
	introspect.SpatialLineString:         LineString,
	introspect.SpatialPolygon:            Polygon,
	introspect.SpatialMultiPoint:         MultiPoint,
	introspect.SpatialMultiLineString:    MultiLineString,
	introspect.SpatialMultiPolygon:       MultiPolygon,
	introspect.SpatialGeometryCollection: GeometryCollection,
}

// ForColumn picks the field type for a spatial column from its typmod: the
// concrete type when the modifier fixes a simple-feature subtype
// (geometry(PointZ,4326) -> GeometryPointZ), the dimension interface when it
// fixes only the dimensions (geometry(Geometry,4326) -> GeometryGeometry, or
// a curve subtype), and the top-level interface for an unconstrained column
// or one without typmod information.
func ForColumn(c *introspect.Column) Type {
	f := postgis.FlavourOf(c.PGType)
	st := c.Spatial
	if st == nil || !st.Constrained() {
		return Type{Flavour: f, AnyDims: true}
	}
	d := DimsOf(st.HasZ, st.HasM)
	if k, ok := kindBySubtype[st.Subtype]; ok {
		return Type{Flavour: f, Kind: k, Dims: d}
	}
	return Type{Flavour: f, Dims: d}
}
