package postgis

import (
	"fmt"
	"strings"
)

// Op describes one spatial filter operator: its GraphQL field name, the input
// shape it takes, and how it renders as SQL.
type Op struct {
	// Name is the GraphQL field name inside GeometryFilterOps.
	Name string
	// Arg is the SDL type of the operand.
	Arg string
	// Description documents the operator in the generated schema.
	Description string
	// Index reports whether the operator can use a GiST index directly.
	// Index-usable operators are documented as such so users can tell the
	// cheap ones from the exact-but-expensive ones.
	Index bool
	// render builds the predicate. col is the column expression, geom the
	// already-parameterized geometry expression, and extra carries operator
	// specific placeholders (currently the distance parameter).
	render func(col, geom string, extra []string) string
}

// ArgKind classifies the operand shape so callers know what to parse.
type ArgKind int

const (
	// ArgGeometry: the operand is a bare GeoJSON/WKT value.
	ArgGeometry ArgKind = iota
	// ArgDWithin: the operand is {geometry, distance}.
	ArgDWithin
	// ArgBBox: the operand is {minX, minY, maxX, maxY, srid}.
	ArgBBox
	// ArgBoolean: the operand is a Boolean (isNull).
	ArgBoolean
)

// Kind reports the operand shape for an operator.
func (o Op) Kind() ArgKind {
	switch o.Name {
	case "dwithin", "beyond":
		return ArgDWithin
	case "bboxIntersects", "bboxContains", "bboxContainedBy":
		return ArgBBox
	case "isNull":
		return ArgBoolean
	}
	return ArgGeometry
}

// binary builds a render func for a plain two-argument predicate.
func binary(op string) func(string, string, []string) string {
	return func(col, geom string, _ []string) string {
		return col + " " + op + " " + geom
	}
}

// fn builds a render func for a two-argument PostGIS function.
func fn(name string) func(string, string, []string) string {
	return func(col, geom string, _ []string) string {
		return name + "(" + col + ", " + geom + ")"
	}
}

// Ops is the spatial operator vocabulary exposed on every geometry and
// geography column.
//
// Ordering matters only for schema readability: the index-usable bounding-box
// operators come first because they are the ones that stay fast on large
// tables, followed by the exact DE-9IM predicates.
var Ops = []Op{
	{
		Name: "bboxIntersects", Arg: "BBoxInput", Index: true,
		Description: "Bounding boxes overlap (&&). Index-usable; the cheap first pass for viewport queries.",
		render:      binary("&&"),
	},
	{
		Name: "bboxContains", Arg: "BBoxInput", Index: true,
		Description: "This column's bounding box contains the given box (~).",
		render:      binary("~"),
	},
	{
		Name: "bboxContainedBy", Arg: "BBoxInput", Index: true,
		Description: "This column's bounding box is contained by the given box (@).",
		render:      binary("@"),
	},
	{
		Name: "intersects", Arg: TypeName, Index: true,
		Description: "Geometries share any portion of space (ST_Intersects).",
		render:      fn("ST_Intersects"),
	},
	{
		Name: "disjoint", Arg: TypeName,
		Description: "Geometries share no space (ST_Disjoint). Cannot use an index.",
		render:      fn("ST_Disjoint"),
	},
	{
		Name: "contains", Arg: TypeName, Index: true,
		Description: "This column's geometry contains the given geometry (ST_Contains).",
		render:      fn("ST_Contains"),
	},
	{
		Name: "containsProperly", Arg: TypeName, Index: true,
		Description: "Contains with no boundary contact (ST_ContainsProperly).",
		render:      fn("ST_ContainsProperly"),
	},
	{
		Name: "within", Arg: TypeName, Index: true,
		Description: "This column's geometry lies within the given geometry (ST_Within).",
		render:      fn("ST_Within"),
	},
	{
		Name: "covers", Arg: TypeName, Index: true,
		Description: "No point of the given geometry lies outside this column (ST_Covers).",
		render:      fn("ST_Covers"),
	},
	{
		Name: "coveredBy", Arg: TypeName, Index: true,
		Description: "No point of this column lies outside the given geometry (ST_CoveredBy).",
		render:      fn("ST_CoveredBy"),
	},
	{
		Name: "crosses", Arg: TypeName, Index: true,
		Description: "Geometries cross (ST_Crosses).",
		render:      fn("ST_Crosses"),
	},
	{
		Name: "overlaps", Arg: TypeName, Index: true,
		Description: "Geometries overlap at the same dimension (ST_Overlaps).",
		render:      fn("ST_Overlaps"),
	},
	{
		Name: "touches", Arg: TypeName, Index: true,
		Description: "Geometries touch at a boundary but interiors do not intersect (ST_Touches).",
		render:      fn("ST_Touches"),
	},
	{
		Name: "equals", Arg: TypeName,
		Description: "Geometries are spatially equal, ignoring vertex order (ST_Equals).",
		render:      fn("ST_Equals"),
	},
	{
		Name: "dwithin", Arg: "DWithinInput", Index: true,
		Description: "Within the given distance of the geometry (ST_DWithin). Distance is metres for geography, SRID units for geometry.",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "beyond", Arg: "DWithinInput",
		Description: "Further than the given distance from the geometry (ST_DWithin negated).",
		render: func(col, geom string, extra []string) string {
			return fmt.Sprintf("NOT ST_DWithin(%s, %s, %s)", col, geom, extra[0])
		},
	},
	{
		Name: "isNull", Arg: "Boolean",
		Description: "Test whether the column is null.",
		render:      nil, // handled structurally by the compiler
	},
}

// OpByName looks up an operator by GraphQL field name.
func OpByName(name string) (Op, bool) {
	for _, op := range Ops {
		if op.Name == name {
			return op, true
		}
	}
	return Op{}, false
}

// Render builds the SQL predicate for an operator.
func (o Op) Render(col, geom string, extra ...string) string {
	if o.render == nil {
		return ""
	}
	return o.render(col, geom, extra)
}

// BBoxSQL renders a bounding box as ST_MakeEnvelope over four numeric
// parameters. The placeholders are supplied by the caller so the values stay
// in the parameter list; only the SRID (an integer read from the query shape)
// is inlined, matching how the rest of the compiler treats structural values.
func BBoxSQL(minX, minY, maxX, maxY string, srid int, f Flavour) string {
	if srid == 0 {
		srid = defaultGeoJSONSRID
	}
	expr := fmt.Sprintf("ST_MakeEnvelope(%s, %s, %s, %s, %d)", minX, minY, maxX, maxY, srid)
	if f == Geography {
		expr = "(" + expr + ")::geography"
	}
	return expr
}

// OutputSQL renders a spatial column as the GeoJSON text the response carries.
//
// ST_AsGeoJSON returns text, so the result is re-parsed into jsonb to embed it
// as a real JSON object rather than a JSON-encoded string. No NULL guard is
// needed: ST_AsGeoJSON is STRICT (NULL in, NULL out) and NULL::jsonb is NULL —
// and ref may be a computed-column function call, which a CASE guard would
// evaluate twice.
func OutputSQL(ref string) string {
	return "ST_AsGeoJSON(" + ref + ")::jsonb"
}

// Derived is one computed spatial accessor exposed as a sibling field of a
// spatial column (e.g. `location` -> `locationArea`).
type Derived struct {
	// Suffix is appended to the column's field name.
	Suffix string
	// Type is the SDL type of the generated field.
	Type string
	// Description documents the field.
	Description string
	// GeoJSON marks accessors returning a geometry (rendered as GeoJSON).
	GeoJSON bool
	// sql builds the expression from the column reference and flavour.
	sql func(ref string, f Flavour) string
}

// Deriveds are the derived accessors generated for every spatial column.
//
// Each is a pure function of the column, so they compile into the same single
// statement as an ordinary column read — no extra round trip.
var Deriveds = []Derived{
	{
		Suffix: "Area", Type: "Float",
		Description: "Area of the geometry (ST_Area): square metres for geography, SRID units for geometry.",
		sql:         func(ref string, _ Flavour) string { return "ST_Area(" + ref + ")" },
	},
	{
		Suffix: "Length", Type: "Float",
		Description: "Length/perimeter of the geometry (ST_Length): metres for geography, SRID units for geometry.",
		sql:         func(ref string, _ Flavour) string { return "ST_Length(" + ref + ")" },
	},
	{
		Suffix: "Perimeter", Type: "Float",
		Description: "Perimeter of a polygonal geometry (ST_Perimeter).",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				// ST_Perimeter has no geography overload in older PostGIS;
				// the geometry cast is exact for the planar perimeter.
				return "ST_Perimeter(" + ref + "::geometry)"
			}
			return "ST_Perimeter(" + ref + ")"
		},
	},
	{
		Suffix: "Centroid", Type: TypeName, GeoJSON: true,
		Description: "Centroid of the geometry (ST_Centroid), as GeoJSON.",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_Centroid(" + ref + "::geometry)"
			}
			return "ST_Centroid(" + ref + ")"
		},
	},
	{
		Suffix: "Envelope", Type: TypeName, GeoJSON: true,
		Description: "Bounding box of the geometry (ST_Envelope), as GeoJSON.",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_Envelope(" + ref + "::geometry)"
			}
			return "ST_Envelope(" + ref + ")"
		},
	},
	{
		Suffix: "Srid", Type: "Int",
		Description: "Spatial reference identifier of the value (ST_SRID).",
		sql:         func(ref string, _ Flavour) string { return "ST_SRID(" + ref + ")" },
	},
	{
		Suffix: "GeometryType", Type: "String",
		// ST_GeometryType is the SQL/MM-standard spelling and returns the
		// "ST_"-prefixed name ("ST_Point"); the bare GeometryType() function
		// returns the unprefixed legacy form ("POINT").
		Description: "Geometry type name, e.g. ST_Point (ST_GeometryType).",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_GeometryType(" + ref + "::geometry)"
			}
			return "ST_GeometryType(" + ref + ")"
		},
	},
	{
		Suffix: "IsValid", Type: "Boolean",
		Description: "Whether the geometry is topologically valid (ST_IsValid).",
		sql: func(ref string, f Flavour) string {
			if f == Geography {
				return "ST_IsValid(" + ref + "::geometry)"
			}
			return "ST_IsValid(" + ref + ")"
		},
	},
}

// DerivedBySuffix looks up a derived accessor.
func DerivedBySuffix(suffix string) (Derived, bool) {
	for _, d := range Deriveds {
		if d.Suffix == suffix {
			return d, true
		}
	}
	return Derived{}, false
}

// SQL renders the derived accessor over a column reference, wrapping geometry
// valued results as GeoJSON.
func (d Derived) SQL(ref string, f Flavour) string {
	expr := d.sql(ref, f)
	if d.GeoJSON {
		return OutputSQL(expr)
	}
	return expr
}

// TransformSQL renders the per-field `transform:` argument: reproject a value
// into another SRID. The SRID is an integer from the query shape, so inlining
// it keeps the value parameter list untouched.
func TransformSQL(ref string, srid int, f Flavour) string {
	if srid <= 0 {
		return ref
	}
	if f == Geography {
		return fmt.Sprintf("ST_Transform(%s::geometry, %d)", ref, srid)
	}
	return fmt.Sprintf("ST_Transform(%s, %d)", ref, srid)
}

// SimplifySQL renders the per-field `simplify:` argument: drop vertices under
// the given tolerance (ST_SimplifyPreserveTopology, which never produces an
// invalid geometry). The tolerance is a placeholder supplied by the caller.
func SimplifySQL(ref, tolerance string, f Flavour) string {
	if f == Geography {
		return fmt.Sprintf("ST_SimplifyPreserveTopology(%s::geometry, %s)", ref, tolerance)
	}
	return fmt.Sprintf("ST_SimplifyPreserveTopology(%s, %s)", ref, tolerance)
}

// DistanceSQL renders the ordering expression for distance-from-a-point sorts.
func DistanceSQL(col, geom string) string {
	return fmt.Sprintf("ST_Distance(%s, %s)", col, geom)
}

// KNNSQL renders the index-assisted nearest-neighbour operator (<->), which a
// GiST index can answer directly. Used for distance ordering because it is the
// operator PostGIS optimises for ORDER BY.
func KNNSQL(col, geom string) string {
	return fmt.Sprintf("%s <-> %s", col, geom)
}

// DerivedSuffixes returns the accessor suffixes in declaration order.
func DerivedSuffixes() []string {
	out := make([]string, len(Deriveds))
	for i, d := range Deriveds {
		out[i] = d.Suffix
	}
	return out
}

// OpNames returns the operator names in declaration order.
func OpNames() []string {
	out := make([]string, len(Ops))
	for i, op := range Ops {
		out[i] = op.Name
	}
	return out
}

// String renders the operator vocabulary for documentation/debugging.
func OpsDoc() string {
	var b strings.Builder
	for _, op := range Ops {
		fmt.Fprintf(&b, "%s(%s): %s\n", op.Name, op.Arg, op.Description)
	}
	return b.String()
}
