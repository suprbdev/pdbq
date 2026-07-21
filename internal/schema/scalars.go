package schema

// Custom scalar names emitted into every schema.
var customScalars = []string{"BigInt", "BigFloat", "Datetime", "Date", "Time", "UUID", "JSON", "Cursor"}

// scalarFor maps a canonical pg_type name to a GraphQL scalar. Enums and
// composites are resolved by the builder (gqlTypeForColumn) before this is
// consulted.
func scalarFor(pgType string) string {
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

type filterOp struct {
	Name string
	Type string
}
