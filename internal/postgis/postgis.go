// Package postgis models the PostGIS spatial types as a small, catalog-driven
// value layer shared by the schema builder, the SQL compiler, and the postgis
// plugin.
//
// The design mirrors how enums and composites are handled elsewhere: nothing
// here emits a whole statement. It answers three questions —
//
//   - is this column spatial, and which flavour (geometry vs geography)?
//   - how does a GraphQL input value become a single SQL parameter?
//   - what SQL expression reads or transforms such a value?
//
// Everything else (filter surface, ordering, derived fields) is assembled from
// these pieces by callers.
//
// # The parameterization invariant
//
// PostGIS values are user data, so they must move through the parameter list
// exactly like any other value (see the FuzzFilter invariant: compiled SQL text
// is a pure function of the query *shape*). A geometry input is therefore always
// reduced to ONE text parameter plus a constructor whose shape depends only on
// the column type — never on the value:
//
//	ST_GeomFromGeoJSON($1)   -- GeoJSON object input
//	ST_GeomFromEWKT($1)      -- WKT / EWKT string input
//
// The choice between those two constructors is made from the Go type of the
// input (object vs string), which is part of the query shape as far as the
// fuzzer is concerned only if the same query text is reused — so callers pass
// the constructor through Value.SQL, which folds the decision into the
// expression and keeps the value itself in a parameter.
package postgis

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Flavour distinguishes the two spatial base types. They share an operator
// vocabulary but differ in units and in a few function names, so the compiler
// needs to keep them apart.
type Flavour int

const (
	// NotSpatial marks a column PostGIS does not own.
	NotSpatial Flavour = iota
	// Geometry is planar: distances and areas are in SRID units.
	Geometry
	// Geography is spheroidal: distances and areas are in metres.
	Geography
)

func (f Flavour) String() string {
	switch f {
	case Geometry:
		return "geometry"
	case Geography:
		return "geography"
	}
	return ""
}

// Spatial reports whether the flavour is one of the PostGIS base types.
func (f Flavour) Spatial() bool { return f == Geometry || f == Geography }

// MetresNative reports whether distance/length arguments and results are in
// metres without an explicit cast. Geography is metres by definition; geometry
// is in whatever unit its SRID uses.
func (f Flavour) MetresNative() bool { return f == Geography }

// TypeName is the GraphQL scalar every spatial column maps to. Values are
// GeoJSON objects on the way out and GeoJSON-or-WKT on the way in.
const TypeName = "GeoJSON"

// FlavourOf classifies a canonical pg type name (array prefix already stripped
// by the caller, or not — both forms are accepted).
//
// PostGIS installs its types into whichever schema the extension lives in, so
// classification is by type name rather than by schema: a `geometry` column is
// spatial whether the extension sits in public, postgis, or a tenant schema.
func FlavourOf(pgType string) Flavour {
	switch strings.TrimPrefix(pgType, "_") {
	case "geometry":
		return Geometry
	case "geography":
		return Geography
	}
	return NotSpatial
}

// IsSpatial is a convenience wrapper over FlavourOf.
func IsSpatial(pgType string) bool { return FlavourOf(pgType).Spatial() }

// Value is a spatial input value reduced to exactly one SQL parameter plus the
// constructor that turns it back into a PostGIS value.
type Value struct {
	// Param is the single value handed to the driver: GeoJSON text or a
	// WKT/EWKT string.
	Param string
	// ctor is the PostGIS constructor wrapping the placeholder.
	ctor string
	// Flavour is the target type the expression is cast to.
	Flavour Flavour
	// SRID, when non-zero, is applied with ST_SetSRID. GeoJSON is always
	// WGS84 (4326) per RFC 7946 unless the value carries an explicit CRS;
	// EWKT carries its own SRID so none is forced.
	SRID int
}

// SQL renders the value expression given the placeholder for Param (e.g. "$3").
// The result is a well-typed geometry/geography expression.
//
// The placeholder is the ONLY value-derived part; the rest of the string is
// fixed by the column's type, preserving the compiled-SQL-is-a-pure-function-
// of-query-shape invariant.
func (v Value) SQL(placeholder string) string {
	expr := v.ctor + "(" + placeholder + ")"
	if v.SRID != 0 {
		expr = fmt.Sprintf("ST_SetSRID(%s, %d)", expr, v.SRID)
	}
	if v.Flavour == Geography {
		// ST_GeomFromGeoJSON/EWKT return geometry; geography columns need the
		// cast so the operator resolves to the geography overload (metres).
		expr = "(" + expr + ")::geography"
	}
	return expr
}

// defaultGeoJSONSRID is the CRS mandated by RFC 7946 for GeoJSON.
const defaultGeoJSONSRID = 4326

// Parse converts a GraphQL input value into a Value.
//
// Two input forms are accepted, both collapsing to a single parameter:
//
//   - a GeoJSON object ({"type":"Point","coordinates":[1,2]}), including
//     Feature / FeatureCollection unwrapping, re-encoded as text;
//   - a WKT or EWKT string ("POINT(1 2)", "SRID=4326;POINT(1 2)").
//
// The value is validated structurally so malformed input fails at compile time
// with a user-facing error rather than as a Postgres error mid-statement.
func Parse(v any, f Flavour) (Value, error) {
	if !f.Spatial() {
		return Value{}, fmt.Errorf("postgis: not a spatial type")
	}
	switch in := v.(type) {
	case string:
		return parseWKT(in, f)
	case map[string]any:
		return parseGeoJSON(in, f)
	default:
		return Value{}, fmt.Errorf("spatial value must be a GeoJSON object or a WKT string")
	}
}

// parseWKT validates a WKT/EWKT string well enough to reject obvious garbage
// and reports whether it already carries an SRID.
func parseWKT(s string, f Flavour) (Value, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return Value{}, fmt.Errorf("spatial value must not be empty")
	}
	body := trimmed
	hasSRID := false
	if rest, ok := cutEWKTPrefix(trimmed); ok {
		body = rest
		hasSRID = true
	}
	if !looksLikeWKT(body) {
		return Value{}, fmt.Errorf("spatial value %q is not valid WKT", clip(s))
	}
	val := Value{Param: trimmed, ctor: "ST_GeomFromEWKT", Flavour: f}
	if !hasSRID && f == Geography {
		// Bare WKT has SRID 0; geography requires a real CRS.
		val.SRID = defaultGeoJSONSRID
	}
	return val, nil
}

// cutEWKTPrefix strips a leading "SRID=<n>;" prefix, reporting whether one was
// present and well-formed.
func cutEWKTPrefix(s string) (string, bool) {
	if len(s) < 5 || !strings.EqualFold(s[:5], "SRID=") {
		return s, false
	}
	semi := strings.IndexByte(s, ';')
	if semi < 0 {
		return s, false
	}
	digits := s[5:semi]
	if digits == "" {
		return s, false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return s, false
		}
	}
	return strings.TrimSpace(s[semi+1:]), true
}

// wktTypes is the set of geometry keywords a WKT body may start with.
var wktTypes = []string{
	"POINT", "LINESTRING", "POLYGON",
	"MULTIPOINT", "MULTILINESTRING", "MULTIPOLYGON",
	"GEOMETRYCOLLECTION", "CIRCULARSTRING", "COMPOUNDCURVE",
	"CURVEPOLYGON", "MULTICURVE", "MULTISURFACE",
	"POLYHEDRALSURFACE", "TRIANGLE", "TIN",
}

// looksLikeWKT checks the leading keyword. Full WKT parsing is PostGIS's job;
// this only rejects input that is definitely not WKT (which is also what keeps
// a stray SQL fragment from reaching the database as a "geometry").
func looksLikeWKT(s string) bool {
	upper := strings.ToUpper(strings.TrimSpace(s))
	for _, t := range wktTypes {
		if !strings.HasPrefix(upper, t) {
			continue
		}
		rest := strings.TrimSpace(upper[len(t):])
		// Allow "POINT(...)", "POINT Z (...)", "POINT ZM (...)", "POINT EMPTY".
		rest = strings.TrimPrefix(rest, "ZM")
		rest = strings.TrimPrefix(rest, "Z")
		rest = strings.TrimPrefix(rest, "M")
		rest = strings.TrimSpace(rest)
		if strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "EMPTY") {
			return true
		}
	}
	return false
}

// geoJSONTypes enumerates the RFC 7946 geometry types accepted directly.
var geoJSONTypes = map[string]bool{
	"Point": true, "LineString": true, "Polygon": true,
	"MultiPoint": true, "MultiLineString": true, "MultiPolygon": true,
	"GeometryCollection": true,
}

// parseGeoJSON validates a GeoJSON object and re-encodes it as the single text
// parameter fed to ST_GeomFromGeoJSON.
//
// Feature and FeatureCollection are unwrapped because clients routinely paste
// them straight from a map library, and ST_GeomFromGeoJSON rejects both.
func parseGeoJSON(m map[string]any, f Flavour) (Value, error) {
	geom, err := unwrapGeoJSON(m, 0)
	if err != nil {
		return Value{}, err
	}
	b, err := json.Marshal(geom)
	if err != nil {
		return Value{}, fmt.Errorf("spatial value is not encodable as GeoJSON")
	}
	val := Value{
		Param:   string(b),
		ctor:    "ST_GeomFromGeoJSON",
		Flavour: f,
		SRID:    defaultGeoJSONSRID,
	}
	if srid, ok := explicitSRID(m); ok {
		val.SRID = srid
	}
	return val, nil
}

// maxUnwrapDepth bounds Feature/FeatureCollection nesting so a hostile document
// cannot drive unbounded recursion.
const maxUnwrapDepth = 8

// unwrapGeoJSON reduces a GeoJSON document to a bare geometry object.
// A FeatureCollection collapses to a GeometryCollection, which is the closest
// faithful PostGIS representation of "all of these shapes".
func unwrapGeoJSON(m map[string]any, depth int) (map[string]any, error) {
	if depth > maxUnwrapDepth {
		return nil, fmt.Errorf("GeoJSON nesting is too deep")
	}
	typ, _ := m["type"].(string)
	switch {
	case typ == "":
		return nil, fmt.Errorf("GeoJSON object requires a %q member", "type")
	case geoJSONTypes[typ]:
		if err := validateGeometry(m, typ); err != nil {
			return nil, err
		}
		return m, nil
	case typ == "Feature":
		geom, ok := m["geometry"].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("GeoJSON Feature requires a geometry object")
		}
		return unwrapGeoJSON(geom, depth+1)
	case typ == "FeatureCollection":
		feats, ok := m["features"].([]any)
		if !ok || len(feats) == 0 {
			return nil, fmt.Errorf("GeoJSON FeatureCollection requires a non-empty features array")
		}
		geoms := make([]any, 0, len(feats))
		for _, item := range feats {
			fm, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("GeoJSON FeatureCollection features must be objects")
			}
			g, err := unwrapGeoJSON(fm, depth+1)
			if err != nil {
				return nil, err
			}
			geoms = append(geoms, g)
		}
		return map[string]any{"type": "GeometryCollection", "geometries": geoms}, nil
	}
	return nil, fmt.Errorf("unknown GeoJSON type %q", clip(typ))
}

// validateGeometry checks that the members PostGIS needs are present, so a
// typo surfaces as a GraphQL error instead of a raw Postgres exception.
func validateGeometry(m map[string]any, typ string) error {
	if typ == "GeometryCollection" {
		if _, ok := m["geometries"].([]any); !ok {
			return fmt.Errorf("GeoJSON GeometryCollection requires a geometries array")
		}
		return nil
	}
	if _, ok := m["coordinates"].([]any); !ok {
		return fmt.Errorf("GeoJSON %s requires a coordinates array", typ)
	}
	return nil
}

// explicitSRID reads a legacy GeoJSON CRS member ({"crs":{"properties":
// {"name":"EPSG:3857"}}}). RFC 7946 removed CRS in favour of always-WGS84, but
// PostGIS emits it and clients echo it back, so it is honoured when present.
func explicitSRID(m map[string]any) (int, bool) {
	crs, ok := m["crs"].(map[string]any)
	if !ok {
		return 0, false
	}
	props, ok := crs["properties"].(map[string]any)
	if !ok {
		return 0, false
	}
	name, ok := props["name"].(string)
	if !ok {
		return 0, false
	}
	for _, prefix := range []string{"EPSG:", "urn:ogc:def:crs:EPSG::"} {
		if rest, found := strings.CutPrefix(name, prefix); found {
			n := 0
			for _, r := range rest {
				if r < '0' || r > '9' {
					return 0, false
				}
				n = n*10 + int(r-'0')
			}
			if n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// clip bounds untrusted text echoed back in error messages.
func clip(s string) string {
	const max = 40
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
