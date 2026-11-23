package compile

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// ParamSet lets CompileHook plugins build parameterized SQL fragments whose
// placeholder numbering composes with the core compiler.
type ParamSet struct {
	args []any
}

func (p *ParamSet) Add(v any) string {
	p.args = append(p.args, v)
	return fmt.Sprintf("$%d", len(p.args))
}

func (p *ParamSet) Args() []any { return p.args }

// SpatialValue renders a GeoJSON/WKT input value for a PostGIS column as a
// parameterized constructor expression (e.g. ST_GeomFromGeoJSON($3)),
// appending the single value parameter to the set. The column's typmod SRID
// applies as in postgis.ParseFor.
//
// CompileHook plugins writing geometry/geography columns must use this rather
// than Add: a spatial column needs the constructor wrapper, and passing the
// raw GeoJSON text as a bare parameter would fail type resolution.
func (p *ParamSet) SpatialValue(col *introspect.Column, v any) (string, error) {
	f := postgis.FlavourOf(col.PGType)
	if !f.Spatial() {
		return "", fmt.Errorf("compile: column %q is not a PostGIS type", col.Name)
	}
	val, err := postgis.ParseFor(v, f, columnSRID(col))
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	return val.SQL(p.Add(val.Param)), nil
}

// Value renders an input value for col as a parameterized SQL expression,
// appending its parameters to the set: PostGIS constructors for spatial
// columns, codec expressions for contrib types (hstore($1::text[]),
// int4range($1::int4, $2::int4, $3::text), $1::"ext".citext, ...), and a
// coerced bare placeholder (see CoerceInput) for everything else.
//
// CompileHook plugins writing column values should use this rather than
// Add: those types need their constructor or cast to be typed correctly.
func (p *ParamSet) Value(built *schema.Built, col *introspect.Column, v any) (string, error) {
	if col != nil && v != nil && postgis.IsSpatial(col.PGType) {
		return p.SpatialValue(col, v)
	}
	if col != nil && v != nil {
		s := &stmt{c: &Compiler{Built: built}, args: p.args}
		if ci := s.contrib(col); ci.Owned() {
			sql, err := s.contribValue(ci, v)
			p.args = s.args
			return sql, err
		}
	}
	return p.Add(coerceInput(built, col, v)), nil
}

// KeyValue renders a decoded nodeId key value (see DecodeNodeID) for col as
// a parameterized SQL expression: json.Number values are passed as text,
// enum and jsonb values are coerced, and contrib-owned types take their
// codec expression, as for the core node(nodeId:) lookup.
func (p *ParamSet) KeyValue(built *schema.Built, col *introspect.Column, v any) string {
	s := &stmt{c: &Compiler{Built: built}, args: p.args}
	sql := s.keyValue(col, v)
	p.args = s.args
	return sql
}

// EqualOperator returns the equality operator to compare col with a value
// rendered by ParamSet.Value: "=", or the schema-qualified operator of an
// extension type (OPERATOR("ext".=) for citext, hstore, ltree), without
// which citext compares case-sensitively and hstore/ltree fail to resolve
// when the extension's schema is off the search_path.
func EqualOperator(built *schema.Built, col *introspect.Column) string {
	return contribFor(built, col).Operator("=")
}

// DecodeNodeID parses a global identifier into its GraphQL type name and
// primary key values, in primary-key column order. A malformed nodeId is a
// UserError.
func DecodeNodeID(nodeID string) (string, []any, error) {
	return decodeNodeID(nodeID)
}

// UserErrorf builds a compile error whose message is safe to return to API
// clients (see UserError). The message must not name tables, columns or SQL
// types; GraphQL field and argument names typed by the client are fine.
func UserErrorf(format string, args ...any) error {
	return &UserError{msg: fmt.Sprintf(format, args...)}
}

// QuoteLiteral renders s as a SQL string literal.
func QuoteLiteral(s string) string { return quoteLiteral(s) }

// MutationWithCTEs compiles the payload selection of a mutation root field
// against a caller-supplied CTE chain. The chain's final CTE must be named
// __mut and yield the mutated row(s); params created via ps are preserved
// and the payload's own params continue after them. This is the extension
// point CompileHook plugins (e.g. nested-mutations) use to replace the DML
// while reusing payload/relation compilation.
// overrides remaps "schema.table" row sources to CTE names so the payload
// can see rows inserted by the supplied CTEs (same-snapshot rule).
//
// guards are boolean SQL expressions over the CTEs that must all hold for
// the payload row to be returned; a guard either yields true or raises an
// error, so a failed guard aborts the whole statement.
func (c *Compiler) MutationWithCTEs(req *Request, f *ast.Field, meta *schema.FieldMeta, ctes string, ps *ParamSet, overrides map[string]string, guards ...string) (*Statement, error) {
	s := &stmt{c: c, req: req, maxDepth: req.MaxDepth, maxPageSize: req.MaxPageSize, args: ps.args, overrides: overrides}
	if s.maxDepth <= 0 {
		s.maxDepth = 50
	}
	if s.maxPageSize <= 0 {
		s.maxPageSize = defaultMaxPageSize
	}
	payload, err := s.payloadSelect(meta.Table, f)
	if err != nil {
		return nil, err
	}
	// Rendering helpers with no error return (valueExpr and the spatial
	// builders) park failures on the statement; surface them here as well as
	// in Compile, so a plugin-driven mutation cannot emit SQL built from a
	// value that failed to parse.
	if err := s.err(); err != nil {
		return nil, err
	}
	if payload, err = s.resolveRanks(payload); err != nil {
		return nil, err
	}
	sql := fmt.Sprintf("WITH %s\n%s", ctes, payload)
	if len(guards) > 0 {
		sql = fmt.Sprintf("WITH %s\nSELECT __guarded.data\nFROM (\n%s\n) AS __guarded\nWHERE %s",
			ctes, indent(payload), strings.Join(guards, "\n  AND "))
	}
	return &Statement{
		SQL:      sql,
		Args:     s.args,
		Mutation: true,
	}, nil
}

// FieldMeta exposes metadata lookup to plugins.
func (c *Compiler) FieldMeta(typeName, fieldName string) *schema.FieldMeta {
	return c.fieldMeta(typeName, fieldName)
}

// CoerceInput converts a GraphQL input value into a driver-friendly parameter
// for the given column — enum value names map to their PostgreSQL labels,
// json/jsonb values are re-encoded as text, array elements coerce recursively.
// CompileHook plugins building their own DML must route column values through
// this before ParamSet.Add, or enum/jsonb params reach the driver raw.
func CoerceInput(built *schema.Built, col *introspect.Column, v any) any {
	return coerceInput(built, col, v)
}

// QuoteIdent is exported for plugins building SQL fragments.
func QuoteIdent(s string) string { return quoteIdent(s) }

// TableRefSQL renders a quoted schema-qualified relation reference.
func TableRefSQL(schemaName, table string) string {
	return quoteIdent(schemaName) + "." + quoteIdent(table)
}
