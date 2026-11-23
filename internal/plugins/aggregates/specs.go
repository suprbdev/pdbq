package aggregates

import (
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
	// result maps a column to the aggregate's result type, "" when the
	// aggregate does not apply to it.
	result func(c *introspect.Column) string
	// sql renders the aggregate over a reference to column c.
	sql func(ref string, c *introspect.Column) string
}

// bind returns the aggregate of sp over c, or nil when sp does not apply.
func (sp spec) bind(c *introspect.Column) *schema.Aggregate {
	rt := sp.result(c)
	if rt == "" {
		return nil
	}
	return &schema.Aggregate{
		Spec:       sp.id,
		Column:     c,
		SQL:        func(ref string) string { return sp.sql(ref, c) },
		ResultType: rt,
	}
}

// countAggregate counts the related rows (<REL>_COUNT_* ordering).
var countAggregate = &schema.Aggregate{
	Spec:       "count",
	SQL:        func(string) string { return "count(*)" },
	ResultType: "int8",
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
	{id: "stExtent", label: "Bounding box (ST_Extent)", spatial: true, result: spatialResult, sql: spatialWrap("ST_Extent(", ")::geometry")},
	{id: "stUnion", label: "Union (ST_Union)", spatial: true, result: spatialResult, sql: spatialWrap("ST_Union(", ")")},
	{id: "stCollect", label: "Collection (ST_Collect)", spatial: true, result: spatialResult, sql: spatialWrap("ST_Collect(", ")")},
	{id: "stCentroid", label: "Centroid of the collection (ST_Centroid(ST_Collect))", spatial: true, result: spatialResult, sql: spatialWrap("ST_Centroid(ST_Collect(", "))")},
}

// orderedTypes are the types min/max apply to: numbers and intervals as
// upstream, plus dates and timestamps.
var orderedTypes = map[string]bool{
	"int2": true, "int4": true, "int8": true, "float4": true, "float8": true,
	"numeric": true, "money": true, "interval": true,
	"date": true, "timestamp": true, "timestamptz": true,
}

// scalar reports a non-array column. Domains arrive resolved to their base
// type (TypeSchema still names the domain's schema), so the type name alone
// decides the built-in numeric and temporal families.
func scalar(c *introspect.Column) bool {
	return !c.IsArray && c.EnumRef == ""
}

func typeMap(m map[string]string) func(*introspect.Column) string {
	return func(c *introspect.Column) string {
		if !scalar(c) {
			return ""
		}
		return m[c.PGType]
	}
}

func sameType(set map[string]bool) func(*introspect.Column) string {
	return func(c *introspect.Column) string {
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

// noEquality are element types without a default btree/hash equality, which
// count(DISTINCT) and GROUP BY need.
var noEquality = map[string]bool{
	"json": true, "xml": true, "point": true, "line": true, "lseg": true,
	"box": true, "path": true, "polygon": true, "circle": true,
}

// groupable reports whether a column's values compare for equality.
func groupable(c *introspect.Column) bool {
	return !noEquality[strings.TrimPrefix(c.PGType, "_")]
}

func distinctResult(c *introspect.Column) string {
	if !groupable(c) {
		return ""
	}
	return "int8"
}

func spatialResult(c *introspect.Column) string {
	if c.IsArray || !postgis.IsSpatial(c.PGType) {
		return ""
	}
	return "geometry"
}

func wrap(pre, post string) func(string, *introspect.Column) string {
	return func(ref string, _ *introspect.Column) string { return pre + ref + post }
}

// spatialWrap casts geography to geometry first: PostGIS defines ST_Extent,
// ST_Union and ST_Collect for geometry only.
func spatialWrap(pre, post string) func(string, *introspect.Column) string {
	return func(ref string, c *introspect.Column) string {
		if postgis.FlavourOf(c.PGType) == postgis.Geography {
			ref += "::geometry"
		}
		return pre + ref + post
	}
}
