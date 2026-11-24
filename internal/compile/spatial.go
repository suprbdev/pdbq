package compile

import (
	"encoding/json"
	"fmt"
	"math"
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
//
// srid is the column's typmod SRID (0 when unconstrained): bare WKT operands
// are tagged with it, GeoJSON is reprojected to it, and a bounding box without
// an explicit srid uses it.
func (s *stmt) spatialOp(f postgis.Flavour, srid int, ref, op string, val any) (string, error) {
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
		box, err := s.bboxExpr(render, srid, val)
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
		geom, err := s.spatialOperand(f, srid, geomVal)
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
		geom, err := s.spatialOperandAs(f, render, srid, val)
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
func (s *stmt) spatialOperandAs(f, render postgis.Flavour, srid int, v any) (string, error) {
	val, err := postgis.ParseFor(v, f, srid)
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	val.Flavour = render
	return val.SQL(s.param(val.Param)), nil
}

// spatialOperand renders a GeoJSON/WKT operand as a parameterized PostGIS
// value expression for a column of flavour f and typmod SRID srid (see
// postgis.ParseFor).
func (s *stmt) spatialOperand(f postgis.Flavour, srid int, v any) (string, error) {
	val, err := postgis.ParseFor(v, f, srid)
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	return val.SQL(s.param(val.Param)), nil
}

// bboxExpr renders a BBoxInput as ST_MakeEnvelope over four bind parameters.
// The SRID is structural (an integer literal in the compiled text) exactly
// like a page size or an enum label, so the four coordinates remain the only
// value-carrying parts. srid applies when the box names none.
func (s *stmt) bboxExpr(f postgis.Flavour, srid int, v any) (string, error) {
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

// nearExpr resolves column col's reference geometry in the list field f's
// near: argument into a parameterized geometry expression, or "" when the
// argument holds none for col.
func (s *stmt) nearExpr(t *introspect.Table, col string, f *ast.Field) (string, error) {
	geom, _ := s.nearValue(f, col)
	if geom == nil {
		return "", nil
	}
	c := t.Column(col)
	if c == nil {
		return "", fmt.Errorf("compile: spatial order column %q not found", col)
	}
	return s.spatialOperand(postgis.FlavourOf(c.PGType), columnSRID(c), geom)
}

// columnSRID returns the SRID a spatial column's type modifier enforces, or 0
// when it is unconstrained or not spatial.
func columnSRID(c *introspect.Column) int {
	if c == nil || c.Spatial == nil {
		return 0
	}
	return c.Spatial.SRID
}

// spatialFieldSQL renders a selected spatial column, applying the optional
// per-field projection arguments before the value is encoded as GeoJSON —
// or, when the field is typed as a geometry object (typed_geometry), as that
// object's selection: `buffer:` (ST_Buffer, metres on geography), then
// `simplify:` (vertex reduction), then `transform:` (reproject), and
// `precision:` (decimal digits of the encoded coordinates). Buffer distance,
// tolerance and precision are bind parameters.
func (s *stmt) spatialFieldSQL(alias string, c *introspect.Column, f *ast.Field, typeName string, depth int) (string, error) {
	flavour := postgis.FlavourOf(c.PGType)
	expr := alias + "." + quoteIdent(c.Name)
	if v, ok := s.argValue(f, "buffer"); ok {
		d, err := toFloat(v)
		if err == nil {
			err = postgis.CheckBuffer(d)
		}
		if err != nil {
			return "", errUser("buffer must be a finite number")
		}
		expr = postgis.BufferSQL(expr, s.param(d))
	}
	if v, ok := s.argValue(f, "simplify"); ok {
		tol, err := toFloat(v)
		if err != nil {
			return "", errUser("simplify must be a number")
		}
		if tol < 0 {
			return "", errUser("simplify must be >= 0")
		}
		expr = postgis.SimplifySQL(expr, s.param(tol), flavour)
		flavour = postgis.Geometry
	}
	if v, ok := s.argValue(f, "transform"); ok {
		srid, err := toFloat(v)
		if err != nil || srid <= 0 {
			return "", errUser("transform must be a positive SRID")
		}
		expr = postgis.TransformSQL(expr, int(srid), flavour)
	}
	digits := ""
	if v, ok := s.argValue(f, "precision"); ok {
		n, err := toFloat(v)
		if err == nil && n != math.Trunc(n) {
			err = fmt.Errorf("not an integer")
		}
		if err == nil {
			err = postgis.CheckPrecision(int64(n))
		}
		if err != nil {
			return "", errUser("precision must be an integer between 0 and %d", postgis.MaxPrecision)
		}
		digits = s.param(int64(n))
	}
	if gt, ok := s.typedGeometry(typeName, f.Name); ok {
		return s.typedGeometryDigitsSQL(expr, digits, gt, f.SelectionSet, depth)
	}
	if digits != "" {
		return postgis.OutputJSONPrecisionSQL(expr, digits), nil
	}
	return postgis.OutputJSONSQL(expr), nil
}

// spatialDerivedSQL renders a derived spatial accessor field (locationArea,
// locationCentroid, ...) over the owning column.
func (s *stmt) spatialDerivedSQL(alias string, c *introspect.Column, suffix string) (string, error) {
	d, ok := postgis.DerivedBySuffix(suffix)
	if !ok {
		return "", fmt.Errorf("compile: unknown spatial accessor %q", suffix)
	}
	return d.JSONSQL(alias+"."+quoteIdent(c.Name), postgis.FlavourOf(c.PGType)), nil
}

// rasterAccessorSQL renders a raster metadata accessor field (rastWidth,
// rastEnvelope, ...) over the owning column.
func (s *stmt) rasterAccessorSQL(alias string, c *introspect.Column, suffix string) (string, error) {
	a, ok := postgis.RasterAccessorBySuffix(suffix)
	if !ok {
		return "", fmt.Errorf("compile: unknown raster accessor %q", suffix)
	}
	return a.JSONSQL(alias + "." + quoteIdent(c.Name)), nil
}

// rasterBand binds the 1-based band number of a raster field's band:
// argument, 1 when absent.
func (s *stmt) rasterBand(f *ast.Field) (string, error) {
	band := int64(1)
	if v, ok := s.argValue(f, "band"); ok {
		n, ok := toInt(v)
		if !ok || n < 1 {
			return "", errUser("%s: band must be a positive integer", f.Name)
		}
		band = n
	}
	return s.param(band), nil
}

// rasterValueSQL renders <field>Value(point:, band:): the pixel value of the
// band at the point (ST_Value), the point reprojected to the raster's SRID.
func (s *stmt) rasterValueSQL(alias string, c *introspect.Column, f *ast.Field) (string, error) {
	point, ok := s.argValue(f, "point")
	if !ok {
		return "", errUser("%s requires a point", f.Name)
	}
	geom, err := s.spatialOperand(postgis.Geometry, 0, point)
	if err != nil {
		return "", err
	}
	band, err := s.rasterBand(f)
	if err != nil {
		return "", err
	}
	return postgis.RasterValueSQL(alias+"."+quoteIdent(c.Name), geom, band), nil
}

// rasterSummaryStatsSQL renders the selection of a RasterSummaryStats object
// over <field>SummaryStats(band:) (ST_SummaryStats), null for a NULL raster
// or a missing band. The statistics are computed once per row.
func (s *stmt) rasterSummaryStatsSQL(alias string, c *introspect.Column, f *ast.Field, typeName string) (string, error) {
	band, err := s.rasterBand(f)
	if err != nil {
		return "", err
	}
	v := s.alias("rs")
	j := v + ".j"
	var pairs []string
	for _, sf := range s.expandFor(f.SelectionSet, typeName) {
		key := quoteLiteral(sf.Alias)
		if sf.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(typeName)+"::text")
			continue
		}
		pairs = append(pairs, key, j+"->"+quoteLiteral(sf.Name))
	}
	return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS j) AS %s WHERE %s IS NOT NULL)",
		buildJSONObject(pairs), postgis.RasterSummaryStatsJSONSQL(alias+"."+quoteIdent(c.Name), band), v, j), nil
}

// rasterOp compiles one operator of RasterFilter: isNull, or intersects
// with a geometry in the raster's SRID (ST_Intersects).
func (s *stmt) rasterOp(ref, op string, val any) (string, error) {
	switch op {
	case "isNull":
		if b, _ := val.(bool); b {
			return ref + " IS NULL", nil
		}
		return ref + " IS NOT NULL", nil
	case "intersects":
		geom, err := s.spatialOperand(postgis.Geometry, 0, val)
		if err != nil {
			return "", err
		}
		return postgis.RasterIntersectsSQL(ref, geom), nil
	}
	return "", errUser("unknown raster filter operator %q", op)
}
