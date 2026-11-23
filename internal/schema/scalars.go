package schema

import (
	"slices"

	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/contribtype"
)

// Custom scalar names emitted into every schema.
var customScalars = []string{"BigInt", "BigFloat", "Datetime", "Date", "Time", "UUID", "JSON", "Cursor"}

// RangeElementType returns the element type of a built-in range or
// multirange type (daterange -> date, tstzmultirange -> timestamptz), or ""
// for any other type, including user-defined ranges.
func RangeElementType(pgType string) string {
	switch pgType {
	case "int4range", "int4multirange":
		return "int4"
	case "int8range", "int8multirange":
		return "int8"
	case "numrange", "nummultirange":
		return "numeric"
	case "tsrange", "tsmultirange":
		return "timestamp"
	case "tstzrange", "tstzmultirange":
		return "timestamptz"
	case "daterange", "datemultirange":
		return "date"
	}
	return ""
}

// scalarFor maps a canonical pg_type name to a GraphQL scalar. Enums and
// composites are resolved by the builder (gqlTypeForColumn) before this is
// consulted.
func scalarFor(pgType string) string {
	// PostGIS geometry/geography read and write as GeoJSON. The scalar is
	// only declared in the SDL when a spatial column actually exists (see
	// catalogHasSpatial), so non-spatial databases keep the same schema.
	if postgis.IsSpatial(pgType) {
		return postgis.TypeName
	}
	switch pgType {
	case "int2", "int4", "serial", "smallserial":
		return "Int"
	case "int8", "bigserial", "oid":
		return "BigInt"
	case "float4", "float8":
		return "Float"
	case "numeric", "money":
		return "BigFloat"
	case "bool":
		return "Boolean"
	case "uuid":
		return "UUID"
	case "json", "jsonb":
		return "JSON"
	case "timestamp", "timestamptz":
		return "Datetime"
	case "date":
		return "Date"
	case "time", "timetz":
		return "Time"
	default:
		// text, varchar, bpchar, citext, name, inet, cidr, macaddr, interval,
		// bytea, ranges, ltree, tsvector, unknown extensions -> String
		return "String"
	}
}

// filterOps returns the operator set exposed for a GraphQL scalar/enum type:
// each Postgres type family gets only the operators that make sense for it
// (comparisons for scalars, pattern ops for text, containment for arrays/jsonb).
func filterOps(gqlType string, pgType string, isArray bool) []filterOp {
	// PostGIS columns have no meaningful scalar ordering or equality: the
	// spatial vocabulary (bounding boxes, DE-9IM predicates, radius search)
	// is added by the postgis plugin. Emitting <, >, in here would generate
	// operators PostgreSQL rejects at execution time.
	if postgis.IsSpatial(pgType) {
		return []filterOp{{"isNull", "Boolean"}}
	}
	if isArray {
		return []filterOp{
			{"equalTo", "[" + gqlType + "!]"}, {"notEqualTo", "[" + gqlType + "!]"},
			{"contains", "[" + gqlType + "!]"}, {"containedBy", "[" + gqlType + "!]"},
			{"overlaps", "[" + gqlType + "!]"}, {"isNull", "Boolean"},
		}
	}
	if pgType == "jsonb" || pgType == "json" {
		return []filterOp{
			{"equalTo", "JSON"}, {"contains", "JSON"}, {"containedBy", "JSON"},
			{"containsKey", "String"}, {"isNull", "Boolean"},
			{"pathExists", "String"}, {"pathMatch", "String"},
		}
	}
	base := []filterOp{
		{"equalTo", gqlType}, {"notEqualTo", gqlType},
		{"in", "[" + gqlType + "!]"}, {"notIn", "[" + gqlType + "!]"},
		{"isNull", "Boolean"},
		{"lessThan", gqlType}, {"lessThanOrEqualTo", gqlType},
		{"greaterThan", gqlType}, {"greaterThanOrEqualTo", gqlType},
	}
	if gqlType == "String" {
		base = append(base,
			filterOp{"like", "String"}, filterOp{"likeInsensitive", "String"},
			filterOp{"startsWith", "String"}, filterOp{"endsWith", "String"})
	}
	return base
}

// contribFilterOps returns the generic comparison operators of a contrib
// type's operator input: those its type supports with an operand of the
// column's own input type. Vocabulary operators are added by the contrib
// plugin.
func contribFilterOps(ci contribtype.Column) []filterOp {
	t, list := ci.Input, "["+ci.Input+"!]"
	eq := []filterOp{{"equalTo", t}, {"notEqualTo", t}}
	in := []filterOp{{"in", list}, {"notIn", list}}
	isNull := []filterOp{{"isNull", "Boolean"}}
	cmp := []filterOp{
		{"lessThan", t}, {"lessThanOrEqualTo", t},
		{"greaterThan", t}, {"greaterThanOrEqualTo", t},
	}
	var out []filterOp
	switch ci.Kind {
	case contribtype.FullText:
		return isNull
	case contribtype.Hstore:
		out = append(slices.Clone(eq), isNull...)
	case contribtype.Ltree, contribtype.Inet:
		out = append(append(append(eq, in...), isNull...), cmp...)
	case contribtype.LtreeArray, contribtype.Multirange:
		out = append([]filterOp{{"equalTo", list}, {"notEqualTo", list}}, isNull...)
	case contribtype.Range:
		out = append(append(eq, isNull...), cmp...)
	case contribtype.Interval:
		if ci.Array {
			return []filterOp{
				{"equalTo", list}, {"notEqualTo", list},
				{"contains", list}, {"containedBy", list},
				{"overlaps", list}, {"isNull", "Boolean"},
			}
		}
		out = append(append(append(eq, in...), isNull...), cmp...)
	}
	return out
}

type filterOp struct {
	Name string
	Type string
}
