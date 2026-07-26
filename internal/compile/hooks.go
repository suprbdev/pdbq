package compile

import (
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/postgis"
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
// appending the single value parameter to the set.
//
// CompileHook plugins writing geometry/geography columns must use this rather
// than Add: a spatial column needs the constructor wrapper, and passing the
// raw GeoJSON text as a bare parameter would fail type resolution.
func (p *ParamSet) SpatialValue(col *introspect.Column, v any) (string, error) {
	f := postgis.FlavourOf(col.PGType)
	if !f.Spatial() {
		return "", fmt.Errorf("compile: column %q is not a PostGIS type", col.Name)
	}
	val, err := postgis.Parse(v, f)
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	return val.SQL(p.Add(val.Param)), nil
}

// MutationWithCTEs compiles the payload selection of a mutation root field
// against a caller-supplied CTE chain. The chain's final CTE must be named
// __mut and yield the mutated row(s); params created via ps are preserved
// and the payload's own params continue after them. This is the extension
// point CompileHook plugins (e.g. nested-mutations) use to replace the DML
// while reusing payload/relation compilation.
// overrides remaps "schema.table" row sources to CTE names so the payload
// can see rows inserted by the supplied CTEs (same-snapshot rule).
func (c *Compiler) MutationWithCTEs(req *Request, f *ast.Field, meta *schema.FieldMeta, ctes string, ps *ParamSet, overrides map[string]string) (*Statement, error) {
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
	return &Statement{
		SQL:      fmt.Sprintf("WITH %s\n%s", ctes, payload),
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
