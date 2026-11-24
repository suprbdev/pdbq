package compile

import (
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// contribVocab resolves the vocabulary a contrib value of col belongs to.
func (s *stmt) contribVocab(col *introspect.Column, cv *schema.ContribValue) (pgcontrib.Vocabulary, error) {
	v, ok := s.contrib(col).Vocab(cv.Vocab)
	if !ok {
		return pgcontrib.Vocabulary{}, fmt.Errorf("compile: column %q has no %s vocabulary", col.Name, cv.Vocab)
	}
	return v, nil
}

// contribAccessor resolves an accessor contrib value of col.
func (s *stmt) contribAccessor(col *introspect.Column, cv *schema.ContribValue) (pgcontrib.Accessor, error) {
	v, err := s.contribVocab(col, cv)
	if err != nil {
		return pgcontrib.Accessor{}, err
	}
	a, ok := v.Accessor(cv.Name)
	if !ok {
		return pgcontrib.Accessor{}, fmt.Errorf("compile: unknown %s accessor %q", cv.Vocab, cv.Name)
	}
	return a, nil
}

// contribExpr resolves an expression contrib value of col.
func (s *stmt) contribExpr(col *introspect.Column, cv *schema.ContribValue) (pgcontrib.Expr, error) {
	v, err := s.contribVocab(col, cv)
	if err != nil {
		return pgcontrib.Expr{}, err
	}
	e, ok := v.Expr(cv.Name)
	if !ok {
		return pgcontrib.Expr{}, fmt.Errorf("compile: unknown %s expression %q", cv.Vocab, cv.Name)
	}
	return e, nil
}

// contribValueJSON renders a contrib value of pg type typ for a JSON
// response: integers as numbers, everything else (text, ltree) as strings.
func contribValueJSON(expr, typ string) string {
	if typ == "int4" {
		return expr
	}
	return "(" + expr + ")::text"
}

// contribAccessorSQL renders a KindContribAccessor field of the row at
// alias.
func (s *stmt) contribAccessorSQL(meta *schema.FieldMeta, alias string) (string, error) {
	if meta.Contrib == nil || meta.Column == nil {
		return "", fmt.Errorf("compile: contrib accessor field without binding")
	}
	a, err := s.contribAccessor(meta.Column, meta.Contrib)
	if err != nil {
		return "", err
	}
	return contribValueJSON(a.SQL(alias+"."+quoteIdent(meta.Column.Name)), a.Type), nil
}

// contribDerivedSQL renders a KindContribDerived field of the row at alias
// from the field's arguments, bound as parameters: key: (hstore value),
// path: (ltree lca), offset: and length: (ltree subpath, whose length is
// bound even when omitted, as NULL).
func (s *stmt) contribDerivedSQL(meta *schema.FieldMeta, f *ast.Field, alias string) (string, error) {
	if meta.Contrib == nil || meta.Column == nil {
		return "", fmt.Errorf("compile: contrib derived field without binding")
	}
	e, err := s.contribExpr(meta.Column, meta.Contrib)
	if err != nil {
		return "", err
	}
	var operand any
	if e.Arg == pgcontrib.ArgSubpath {
		m := map[string]any{}
		for _, name := range []string{"offset", "length"} {
			if v, ok := s.argValue(f, name); ok {
				m[name] = v
			}
		}
		operand = m
	} else {
		operand, _ = s.argValue(f, meta.Contrib.Arg)
	}
	params, err := e.Params(operand)
	if err != nil {
		return "", errUser("%s: %s", f.Name, err.Error())
	}
	sql := e.Render(alias+"."+quoteIdent(meta.Column.Name), s.params(params)...)
	if sql == "" {
		return "", fmt.Errorf("compile: expression %s rejected %d parameters", e.Name, len(params))
	}
	return contribValueJSON(sql, e.Type), nil
}

// contribFilterCond compiles a table-filter field comparing a contrib value
// of a column of t: an accessor's IntFilter object, or an hstore value's
// {key, ...StringFilter} object. The key is bound only when some
// comparison uses it.
func (s *stmt) contribFilterCond(t *introspect.Table, cv *schema.ContribValue, v any, alias string) (string, error) {
	col := t.Column(cv.Column)
	if col == nil {
		return "", fmt.Errorf("compile: filter column %q not found", cv.Column)
	}
	ref := alias + "." + quoteIdent(col.Name)
	if !cv.Expr {
		a, err := s.contribAccessor(col, cv)
		if err != nil {
			return "", err
		}
		return s.refOps(&introspect.Column{Name: col.Name, PGType: a.Type, TypeSchema: "pg_catalog"}, a.SQL(ref), v)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", errUser("%s value filter must be an object", col.Name)
	}
	ops := map[string]any{}
	for k, val := range m {
		if k != "key" && val != nil {
			ops[k] = val
		}
	}
	if len(ops) == 0 {
		return "", nil
	}
	e, err := s.contribExpr(col, cv)
	if err != nil {
		return "", err
	}
	params, err := e.Params(m["key"])
	if err != nil {
		return "", errUser("key: %s", err.Error())
	}
	expr := "(" + e.Render(ref, s.params(params)...) + ")"
	return s.refOps(&introspect.Column{Name: col.Name, PGType: e.Type, TypeSchema: "pg_catalog"}, expr, ops)
}

// contribOrderTerm resolves an orderBy value sorting by a contrib value of a
// column: an accessor, or an expression whose operand the list field's
// near: argument holds for the column (an hstore key).
func (s *stmt) contribOrderTerm(t *introspect.Table, f *ast.Field, value string, spec schema.OrderSpec) (orderTerm, error) {
	col := t.Column(spec.Column)
	if col == nil {
		return orderTerm{}, fmt.Errorf("compile: order column %q not found", spec.Column)
	}
	cv := spec.Contrib
	if !cv.Expr {
		a, err := s.contribAccessor(col, cv)
		if err != nil {
			return orderTerm{}, err
		}
		return orderTerm{col: spec.Column, acc: &a, desc: spec.Desc}, nil
	}
	e, err := s.contribExpr(col, cv)
	if err != nil {
		return orderTerm{}, err
	}
	ref, field := s.nearValue(f, spec.Column)
	if ref == nil {
		return orderTerm{}, errNoReference("orderBy "+value, field)
	}
	params, err := e.Params(ref)
	if err != nil {
		return orderTerm{}, errUser("near: %s", err.Error())
	}
	return orderTerm{col: spec.Column, expr: &e, exprArgs: s.params(params), desc: spec.Desc}, nil
}
