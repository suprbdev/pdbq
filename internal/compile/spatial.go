package compile

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
)

// spatialOp compiles one operator of a PostGIS filter (see postgis.Ops).
//
// Every operand — GeoJSON geometry, bounding-box corner, distance — travels as
// a bind parameter. The SQL text depends only on which operator was used and
// on the column's flavour, never on the values, which is what the FuzzFilter
// invariant checks.
//
// PostGIS defines only some predicates for geography; for the others a
// geography column and its operand are both cast to geometry (planar
// semantics).
func (s *stmt) spatialOp(f postgis.Flavour, ref, op string, val any) (string, error) {
	spec, ok := postgis.OpByName(op)
	if !ok {
		return "", errUser("unknown spatial filter operator %q", op)
	}
	render := f
	if f == postgis.Geography && !spec.Geography && spec.Kind() != postgis.ArgBoolean {
		render = postgis.Geometry
		ref += "::geometry"
	}
	switch spec.Kind() {
	case postgis.ArgBoolean:
		// isNull, like elsewhere, is part of the query shape.
		if b, _ := val.(bool); b {
			return ref + " IS NULL", nil
		}
		return ref + " IS NOT NULL", nil

	case postgis.ArgBBox:
		box, err := s.bboxExpr(render, val)
		if err != nil {
			return "", err
		}
		return spec.Render(ref, box), nil

	case postgis.ArgDWithin:
		m, ok := val.(map[string]any)
		if !ok {
			return "", errUser("%s filter must be an object with geometry and distance", op)
		}
		geomVal, ok := m["geometry"]
		if !ok || geomVal == nil {
			return "", errUser("%s filter requires a geometry", op)
		}
		geom, err := s.spatialOperand(f, geomVal)
		if err != nil {
			return "", err
		}
		distVal, ok := m["distance"]
		if !ok || distVal == nil {
			return "", errUser("%s filter requires a distance", op)
		}
		dist, err := toFloat(distVal)
		if err != nil {
			return "", errUser("%s distance must be a number", op)
		}
		if dist < 0 {
			return "", errUser("%s distance must be >= 0", op)
		}
		// Geometry columns measure in SRID units; `useSpheroid` lets callers
		// ask for metres on a geometry column by casting both sides to
		// geography, which is the standard PostGIS idiom for lat/lon data
		// stored as plain geometry.
		if b, _ := m["useSpheroid"].(bool); b && f == postgis.Geometry {
			return spec.Render(ref+"::geography", "("+geom+")::geography", s.param(dist)), nil
		}
		return spec.Render(ref, geom, s.param(dist)), nil

	default:
		geom, err := s.spatialOperandAs(f, render, val)
		if err != nil {
			return "", err
		}
		return spec.Render(ref, geom), nil
	}
}

// spatialOperandAs parses the operand for a column of flavour f, which fixes
// the SRID defaults (geography input is tagged 4326), but casts the expression
// to render so a geography column compared through a geometry-only predicate
// gets a geometry operand with the same SRID.
func (s *stmt) spatialOperandAs(f, render postgis.Flavour, v any) (string, error) {
	val, err := postgis.Parse(v, f)
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	val.Flavour = render
	return val.SQL(s.param(val.Param)), nil
}

// spatialOperand renders a GeoJSON/WKT operand as a parameterized PostGIS
// value expression.
func (s *stmt) spatialOperand(f postgis.Flavour, v any) (string, error) {
	val, err := postgis.Parse(v, f)
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	return val.SQL(s.param(val.Param)), nil
}

// bboxExpr renders a BBoxInput as ST_MakeEnvelope over four bind parameters.
// The SRID is structural (an integer literal in the compiled text) exactly
// like a page size or an enum label, so the four coordinates remain the only
// value-carrying parts.
func (s *stmt) bboxExpr(f postgis.Flavour, v any) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", errUser("bounding box filter must be an object")
	}
	coord := func(key string) (string, error) {
		raw, ok := m[key]
		if !ok || raw == nil {
			return "", errUser("bounding box requires %s", key)
		}
		n, err := toFloat(raw)
		if err != nil {
			return "", errUser("bounding box %s must be a number", key)
		}
		return s.param(n), nil
	}
	minX, err := coord("minX")
	if err != nil {
		return "", err
	}
	minY, err := coord("minY")
	if err != nil {
		return "", err
	}
	maxX, err := coord("maxX")
	if err != nil {
		return "", err
	}
	maxY, err := coord("maxY")
	if err != nil {
		return "", err
	}
	srid := 0
	if raw, ok := m["srid"]; ok && raw != nil {
		n, err := toFloat(raw)
		if err != nil {
			return "", errUser("bounding box srid must be an integer")
		}
		srid = int(n)
	}
	return postgis.BBoxSQL(minX, minY, maxX, maxY, srid, f), nil
}

// toFloat normalizes the numeric shapes a GraphQL value can arrive as.
func toFloat(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case json.Number:
		return n.Float64()
	case string:
		// gqlparser hands Int/Float literals through as strings in some
		// positions; accept them rather than failing on a valid query.
		f, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, fmt.Errorf("not a number")
		}
		return f, nil
	}
	return 0, fmt.Errorf("not a number")
}

// nearExpr resolves the `near:` argument into a parameterized geometry
// expression for distance ordering over col.
//
// Returns "" when the argument is absent: the distance orderBy value is then
// silently dropped, because sorting by distance from nothing is not a
// meaningful order and a fabricated one would be worse than none.
func (s *stmt) nearExpr(t *introspect.Table, col string, f *ast.Field) (string, error) {
	v, ok := s.argValue(f, "near")
	if !ok {
		return "", nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", errUser("near must be an object with a geometry")
	}
	geom, ok := m["geometry"]
	if !ok || geom == nil {
		return "", errUser("near requires a geometry")
	}
	c := t.Column(col)
	if c == nil {
		return "", fmt.Errorf("compile: spatial order column %q not found", col)
	}
	return s.spatialOperand(postgis.FlavourOf(c.PGType), geom)
}

// spatialFieldSQL renders a selected spatial column, applying the optional
// per-field `transform:` (reproject) and `simplify:` (vertex reduction)
// arguments before the value is encoded as GeoJSON — or, when the field is
// typed as a geometry object (typed_geometry), as that object's selection.
func (s *stmt) spatialFieldSQL(alias string, c *introspect.Column, f *ast.Field, typeName string, depth int) (string, error) {
	flavour := postgis.FlavourOf(c.PGType)
	ref := alias + "." + quoteIdent(c.Name)
	expr := ref
	if v, ok := s.argValue(f, "simplify"); ok && v != nil {
		tol, err := toFloat(v)
		if err != nil {
			return "", errUser("simplify must be a number")
		}
		if tol < 0 {
			return "", errUser("simplify must be >= 0")
		}
		expr = postgis.SimplifySQL(expr, s.param(tol), flavour)
	}
	if v, ok := s.argValue(f, "transform"); ok && v != nil {
		srid, err := toFloat(v)
		if err != nil || srid <= 0 {
			return "", errUser("transform must be a positive SRID")
		}
		// Simplify already produced a geometry; keep the cast consistent.
		flavourForTransform := flavour
		if expr != ref {
			flavourForTransform = postgis.Geometry
		}
		expr = postgis.TransformSQL(expr, int(srid), flavourForTransform)
	}
	if gt, ok := s.typedGeometry(typeName, f.Name); ok {
		return s.typedGeometrySQL(expr, gt, f.SelectionSet, depth)
	}
	return postgis.OutputSQL(expr), nil
}

// spatialDerivedSQL renders a derived spatial accessor field (locationArea,
// locationCentroid, ...) over the owning column.
func (s *stmt) spatialDerivedSQL(alias string, c *introspect.Column, suffix string) (string, error) {
	d, ok := postgis.DerivedBySuffix(suffix)
	if !ok {
		return "", fmt.Errorf("compile: unknown spatial accessor %q", suffix)
	}
	return d.SQL(alias+"."+quoteIdent(c.Name), postgis.FlavourOf(c.PGType)), nil
}
