package compile

import (
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// nullScore is a score without a reference.
const nullScore = "NULL::float8"

// scoreList records the list field whose reference arguments the score
// fields of the rows scanned at alias measure against.
func (s *stmt) scoreList(alias string, f *ast.Field) {
	if s.scoreLists == nil {
		s.scoreLists = map[string]*ast.Field{}
	}
	s.scoreLists[alias] = f
}

// scoreSQL renders a score field (schema.KindScore) of the row at alias
// against its list's reference: the spatial distance from near:
// (ST_Distance, or metres on the spheroid with useSpheroid: on a geometry
// column), a pg_trgm similarity to similarTo:, a pgvector or cube distance
// from nearVector: or an earthdistance distance from nearPoint: (the value
// itself, Expr.Render, not its ordering form). Rows read outside such a
// list, or through a list without the
// reference for the column, score NULL. The reference is bound again rather
// than shared with the ordering, so the SQL text only depends on which
// fields and arguments are present.
func (s *stmt) scoreSQL(meta *schema.FieldMeta, f *ast.Field, alias string) (string, error) {
	list := s.scoreLists[alias]
	if list == nil || meta.Score == nil || meta.Column == nil {
		return nullScore, nil
	}
	ref := alias + "." + quoteIdent(meta.Column.Name)
	if meta.Score.Arg == "near" {
		near, err := s.nearExpr(meta.Table, meta.Column.Name, list)
		if err != nil || near == "" {
			return nullScore, err
		}
		if b, _ := s.argValue(f, "useSpheroid"); b == true && postgis.FlavourOf(meta.Column.PGType) == postgis.Geometry {
			return postgis.SpheroidDistanceSQL(ref, near), nil
		}
		return postgis.DistanceSQL(ref, near), nil
	}
	expr, err := s.referenceExpr(meta.Column, meta.Score.Arg, meta.Score.Expr)
	if err != nil {
		return "", err
	}
	v := s.referenceValue(list, meta.Score.Arg, meta.Column.Name)
	if v == nil {
		return nullScore, nil
	}
	params, err := expr.Params(v)
	if err != nil {
		return "", errUser("%s: %s", meta.Score.Arg, err.Error())
	}
	return expr.Render(ref, s.params(params)...), nil
}
