package compile

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/geotype"
)

const (
	geoTop   = "__geo"
	geoValue = "__geo_v"
)

// geoCtx carries the per-column values every nested object shares.
type geoCtx struct {
	// dims is the SQL text of the dimension suffix ('' / 'Z' / 'M' / 'ZM').
	dims string
}

// geoNode is one geometry value inside the tree: either a GeoJSON object
// (geojson set) or a bare coordinate array whose kind the static type fixes.
type geoNode struct {
	geojson string
	coords  string
}

func (n geoNode) coordinates() string {
	if n.coords != "" {
		return n.coords
	}
	return n.geojson + "->'coordinates'"
}

func (n geoNode) geoJSON(k geotype.Kind) string {
	if n.geojson != "" {
		return n.geojson
	}
	return "json_build_object('type', " + quoteLiteral(k.GeoJSON()) + ", 'coordinates', " + n.coords + ")"
}

// typedGeometry reports whether field fieldName of typeName is declared as a
// geometry object type rather than the GeoJSON scalar.
func (s *stmt) typedGeometry(typeName, fieldName string) (geotype.Type, bool) {
	def := s.c.Built.Schema.Types[typeName]
	if def == nil {
		return geotype.Type{}, false
	}
	fd := def.Fields.ForName(fieldName)
	if fd == nil || fd.Type == nil {
		return geotype.Type{}, false
	}
	return geotype.Parse(fd.Type.Name())
}

// typedGeometrySQL renders a spatial value expression as the geometry object
// selected by sel (plugins.settings.postgis.typed_geometry), as one scalar
// subquery:
//
//	(SELECT <object> FROM (
//	   SELECT ST_AsGeoJSON(__geo_v.g)::jsonb AS j, ST_SRID(__geo_v.g) AS srid [, dims]
//	   FROM (SELECT <expr> AS g) AS __geo_v
//	 ) AS __geo WHERE __geo.j IS NOT NULL)
//
// The value is encoded to GeoJSON once; every nested field (x, points,
// exterior, geometries, ...) is read back out of that jsonb, so the selection
// set alone decides the SQL text. Interface-typed values dispatch on
// __typename, built from the GeoJSON "type" member plus the dimension suffix
// (ST_Zmflag, unless the static type fixes the dimensions).
//
// Subquery aliases inside the tree are named by nesting depth (__geo_<depth>)
// rather than allocated, so the arms of an interface dispatch that select the
// same fields render identically and can share one CASE arm.
func (s *stmt) typedGeometrySQL(expr string, t geotype.Type, sel ast.SelectionSet, depth int) (string, error) {
	ctx := geoCtx{dims: quoteLiteral(t.Dims.Suffix())}
	cols := fmt.Sprintf("ST_AsGeoJSON(%s.g)::jsonb AS j, ST_SRID(%s.g) AS srid", geoValue, geoValue)
	if t.AnyDims {
		ctx.dims = geoTop + ".dims"
		cols += fmt.Sprintf(", CASE ST_Zmflag(%s.g::geometry) WHEN 1 THEN 'M' WHEN 2 THEN 'Z' WHEN 3 THEN 'ZM' ELSE '' END AS dims", geoValue)
	}
	obj, err := s.geoObject(ctx, geoNode{geojson: geoTop + ".j"}, t, sel, depth)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(SELECT %s FROM (SELECT %s FROM (SELECT %s AS g) AS %s) AS %s WHERE %s.j IS NOT NULL)",
		obj, cols, expr, geoValue, geoTop, geoTop), nil
}

// geoObject renders node as static type t. An interface dispatches over its
// possible concrete types; types whose selections render identically share a
// CASE arm, and the largest group becomes the ELSE. The dispatch is guarded
// by the seven GeoJSON geometry types, so a value of any other type renders
// as null instead of carrying a __typename the schema does not define.
func (s *stmt) geoObject(ctx geoCtx, n geoNode, t geotype.Type, sel ast.SelectionSet, depth int) (string, error) {
	if depth > s.maxDepth {
		return "", errUser("selection depth exceeds limit (%d)", s.maxDepth)
	}
	if t.Concrete() {
		return s.geoConcrete(ctx, n, t, quoteLiteral(t.Name()), sel, depth)
	}
	kind := "(" + n.geojson + "->>'type')"
	typename := quoteLiteral(t.Prefix()) + " || " + kind
	if ctx.dims != "''" {
		typename += " || " + ctx.dims
	}
	known := make([]string, len(geotype.Kinds))
	for i, k := range geotype.Kinds {
		known[i] = quoteLiteral(k.GeoJSON())
	}
	guard := "CASE WHEN " + kind + " IN (" + strings.Join(known, ", ") + ") THEN "

	var order []string
	groups := map[string][]string{}
	for _, p := range t.Possible() {
		obj, err := s.geoConcrete(ctx, n, p, typename, sel, depth)
		if err != nil {
			return "", err
		}
		if _, seen := groups[obj]; !seen {
			order = append(order, obj)
		}
		groups[obj] = append(groups[obj], p.Name())
	}
	if len(order) == 1 {
		return guard + order[0] + " END", nil
	}
	fallback := order[0]
	for _, obj := range order[1:] {
		if len(groups[obj]) > len(groups[fallback]) {
			fallback = obj
		}
	}
	var b strings.Builder
	b.WriteString(guard + "CASE")
	for _, obj := range order {
		if obj == fallback {
			continue
		}
		names := groups[obj]
		sort.Strings(names)
		quoted := make([]string, len(names))
		for i, nm := range names {
			quoted[i] = quoteLiteral(nm)
		}
		fmt.Fprintf(&b, " WHEN %s IN (%s) THEN %s", typename, strings.Join(quoted, ", "), obj)
	}
	fmt.Fprintf(&b, " ELSE %s END END", fallback)
	return b.String(), nil
}

// geoConcrete renders node as concrete type t, with typename the SQL text of
// its __typename. An empty point (ST_AsGeoJSON's "coordinates": []) has no
// value for its non-null coordinate fields, so it renders as null.
func (s *stmt) geoConcrete(ctx geoCtx, n geoNode, t geotype.Type, typename string, sel ast.SelectionSet, depth int) (string, error) {
	coords := n.coordinates()
	var pairs []string
	for _, f := range s.expandFor(sel, t.Name()) {
		key := quoteLiteral(f.Alias)
		var expr string
		switch f.Name {
		case "__typename":
			expr = typename
		case "geojson":
			expr = n.geoJSON(t.Kind)
		case "srid":
			expr = geoTop + ".srid"
		case "exterior":
			ring, _ := t.Member(f.Name)
			first := coords + "->0"
			obj, err := s.geoObject(ctx, geoNode{coords: first}, ring, f.SelectionSet, depth+1)
			if err != nil {
				return "", err
			}
			expr = "CASE WHEN " + first + " IS NULL THEN NULL ELSE " + obj + " END"
		case "points", "interiors", "lines", "polygons", "geometries":
			elem, ok := t.Member(f.Name)
			if !ok {
				return "", fmt.Errorf("compile: no geometry field %s.%s", t.Name(), f.Name)
			}
			var err error
			expr, err = s.geoList(ctx, n, f.Name, elem, f.SelectionSet, depth+1)
			if err != nil {
				return "", err
			}
		default:
			i, ok := t.CoordIndex(f.Name)
			if !ok {
				return "", fmt.Errorf("compile: no geometry field %s.%s", t.Name(), f.Name)
			}
			expr = coords + "->" + strconv.Itoa(i)
		}
		pairs = append(pairs, key, expr)
	}
	obj := buildJSONObject(pairs)
	if t.Kind == geotype.Point {
		obj = "CASE WHEN jsonb_array_length(" + coords + ") = 0 THEN NULL ELSE " + obj + " END"
	}
	return obj, nil
}

// geoList renders a list field (points, interiors, lines, polygons,
// geometries) as a json array over the matching JSON array of the parent,
// in source order.
func (s *stmt) geoList(ctx geoCtx, parent geoNode, field string, elem geotype.Type, sel ast.SelectionSet, depth int) (string, error) {
	a := geoTop + "_" + strconv.Itoa(depth)
	src := parent.coordinates()
	item := geoNode{coords: a + ".v"}
	where := ""
	switch field {
	case "geometries":
		src = parent.geoJSON(geotype.GeometryCollection) + "->'geometries'"
		item = geoNode{geojson: a + ".v"}
	case "interiors":
		where = " WHERE " + a + ".i > 1"
	}
	obj, err := s.geoObject(ctx, item, elem, sel, depth)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(SELECT coalesce(json_agg(%s ORDER BY %s.i), '[]'::json) FROM jsonb_array_elements(%s) WITH ORDINALITY AS %s(v, i)%s)",
		obj, a, src, a, where), nil
}
