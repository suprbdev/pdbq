package aggregates

import (
	"strconv"
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// spec is one aggregate group (sum, average, stExtent, ...).
type spec struct {
	id    string
	label string
	// nonNull marks results that are never NULL (sum is coalesced to 0).
	nonNull bool
	// spatial groups only appear in aggregates output, never in having,
	// ordering or filtering.
	spatial bool
	// bulky marks spatial groups whose result grows with the row set
	// (stUnion, stCollect): opt-in via spatial_union_collect, charged
	// bulkyCostFactor times the scan cost.
	bulky bool
	// result maps a column to the aggregate's result type, "" when the
	// aggregate does not apply to it; eq reports whether the column's type
	// has a default btree equality.
	result func(c *introspect.Column, eq bool) string
	// sql renders the aggregate over a reference to column c.
	sql func(ref string, c *introspect.Column) string
}

// bulkyCostFactor multiplies the scan cost of stUnion/stCollect fields.
const bulkyCostFactor = 10

// bind returns the aggregate of sp over c, or nil when sp does not apply.
// money results are converted to numeric: money's text form follows
// lc_monetary, numeric's is locale-independent.
func (sp spec) bind(c *introspect.Column, eq bool, cost int) *schema.Aggregate {
	rt := sp.result(c, eq)
	if rt == "" {
		return nil
	}
	render := func(ref string) string { return sp.sql(ref, c) }
	if rt == "money" {
		rt = "numeric"
		render = func(ref string) string { return "(" + sp.sql(ref, c) + ")::numeric" }
	}
	return &schema.Aggregate{Spec: sp.id, Column: c, SQL: render, ResultType: rt, Cost: cost}
}

// countAggregate counts the related rows (<REL>_COUNT_* ordering).
func countAggregate(cost int) *schema.Aggregate {
	return &schema.Aggregate{
		Spec:       "count",
		SQL:        func(string) string { return "count(*)" },
		ResultType: "int8",
		Cost:       cost,
	}
}

// specs lists the aggregate groups in schema order: the
// `@graphile/pg-aggregates` set, then the spatial extension.
var specs = []spec{
	{id: "sum", label: "Sum", nonNull: true, result: typeMap(map[string]string{
		"int2": "int8", "int4": "int8", "int8": "numeric", "float4": "float4", "float8": "float8",
		"numeric": "numeric", "money": "money", "interval": "interval",
	}), sql: wrap("coalesce(sum(", "), '0')")},
	{id: "distinctCount", label: "Distinct count", result: distinctResult, sql: wrap("count(DISTINCT ", ")")},
	{id: "min", label: "Minimum", result: sameType(orderedTypes), sql: wrap("min(", ")")},
	{id: "max", label: "Maximum", result: sameType(orderedTypes), sql: wrap("max(", ")")},
	{id: "average", label: "Mean average", result: typeMap(map[string]string{
		"int2": "numeric", "int4": "numeric", "int8": "numeric", "numeric": "numeric",
		"float4": "float8", "float8": "float8", "interval": "interval",
	}), sql: wrap("avg(", ")")},
	{id: "stddevSample", label: "Sample standard deviation", result: statResult, sql: wrap("stddev_samp(", ")")},
	{id: "stddevPopulation", label: "Population standard deviation", result: statResult, sql: wrap("stddev_pop(", ")")},
	{id: "varianceSample", label: "Sample variance", result: statResult, sql: wrap("var_samp(", ")")},
	{id: "variancePopulation", label: "Population variance", result: statResult, sql: wrap("var_pop(", ")")},
	{id: "stExtent", label: "Bounding box (ST_Extent)", spatial: true, result: spatialResult, sql: extentSQL},
	{id: "stUnion", label: "Union (ST_Union)", spatial: true, bulky: true, result: spatialResult, sql: spatialWrap("ST_Union(", ")")},
	{id: "stCollect", label: "Collection (ST_Collect)", spatial: true, bulky: true, result: spatialResult, sql: spatialWrap("ST_Collect(", ")")},
	{id: "stCentroid", label: "Centroid of the collection (ST_Centroid(ST_Collect))", spatial: true, result: spatialResult, sql: spatialWrap("ST_Centroid(ST_Collect(", "))")},
}

// orderedTypes are the types min/max apply to: numbers and intervals as
// upstream, plus dates and timestamps.
var orderedTypes = map[string]bool{
	"int2": true, "int4": true, "int8": true, "float4": true, "float8": true,
	"numeric": true, "money": true, "interval": true,
	"date": true, "timestamp": true, "timestamptz": true,
}

// scalar reports a non-array column that is not a table-backed enum.
// Domains arrive resolved to their base type (TypeSchema still names the
// domain's schema), so the type name alone decides the built-in numeric and
// temporal families.
func scalar(c *introspect.Column) bool {
	return !c.IsArray && c.EnumRef == ""
}

func typeMap(m map[string]string) func(*introspect.Column, bool) string {
	return func(c *introspect.Column, _ bool) string {
		if !scalar(c) {
			return ""
		}
		return m[c.PGType]
	}
}

func sameType(set map[string]bool) func(*introspect.Column, bool) string {
	return func(c *introspect.Column, _ bool) string {
		if scalar(c) && set[c.PGType] {
			return c.PGType
		}
		return ""
	}
}

var statResult = typeMap(map[string]string{
	"int2": "numeric", "int4": "numeric", "int8": "numeric", "numeric": "numeric",
	"float4": "float8", "float8": "float8",
})

// btreeTypes are the element types with a default btree operator class,
// which count(DISTINCT) and GROUP BY rely on: the built-in scalars and
// ranges, the contrib types pdbq maps, and the PostGIS types. Enums are
// admitted separately; anything else (json, xml, geometric types,
// composites, unknown extension types) is left out.
var btreeTypes = map[string]bool{
	"bool": true, "int2": true, "int4": true, "int8": true, "float4": true, "float8": true,
	"numeric": true, "money": true, "oid": true,
	"text": true, "varchar": true, "bpchar": true, "char": true, "name": true,
	"uuid": true, "bytea": true, "jsonb": true, "bit": true, "varbit": true, "tsvector": true,
	"date": true, "time": true, "timetz": true, "timestamp": true, "timestamptz": true, "interval": true,
	"inet": true, "cidr": true, "macaddr": true, "macaddr8": true,
	"int4range": true, "int8range": true, "numrange": true, "tsrange": true, "tstzrange": true, "daterange": true,
	"int4multirange": true, "int8multirange": true, "nummultirange": true, "tsmultirange": true,
	"tstzmultirange": true, "datemultirange": true,
	"citext": true, "ltree": true, "hstore": true,
	"geometry": true, "geography": true,
}

func distinctResult(_ *introspect.Column, eq bool) string {
	if !eq {
		return ""
	}
	return "int8"
}

func spatialResult(c *introspect.Column, _ bool) string {
	if c.IsArray || !postgis.IsSpatial(c.PGType) {
		return ""
	}
	return "geometry"
}

func wrap(pre, post string) func(string, *introspect.Column) string {
	return func(ref string, _ *introspect.Column) string { return pre + ref + post }
}

// asGeometry casts geography to geometry: PostGIS defines ST_Extent,
// ST_Union and ST_Collect for geometry only.
func asGeometry(ref string, c *introspect.Column) string {
	if postgis.FlavourOf(c.PGType) == postgis.Geography {
		return ref + "::geometry"
	}
	return ref
}

func spatialWrap(pre, post string) func(string, *introspect.Column) string {
	return func(ref string, c *introspect.Column) string { return pre + asGeometry(ref, c) + post }
}

// extentSQL renders ST_Extent, whose box2d result carries no SRID: it is
// restored from the column's typmod, or from the values themselves on an
// unconstrained column, so the GeoJSON output names the right CRS.
func extentSQL(ref string, c *introspect.Column) string {
	srid := "min(ST_SRID(" + ref + "))"
	if c.Spatial != nil && c.Spatial.SRID > 0 {
		srid = strconv.Itoa(c.Spatial.SRID)
	}
	return "ST_SetSRID(ST_Extent(" + asGeometry(ref, c) + ")::geometry, " + srid + ")"
}

// hasBtree reports whether a column's (element) type is in btreeTypes; the
// caller admits enums on top.
func hasBtree(c *introspect.Column) bool {
	return btreeTypes[strings.TrimPrefix(c.PGType, "_")]
}
