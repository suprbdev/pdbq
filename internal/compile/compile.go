// Package compile turns one validated GraphQL root field into exactly one
// parameterized SQL statement, PostGraphile-style: nested selections become
// jsonb_build_object trees, relations become LEFT JOIN LATERAL subqueries,
// lists become jsonb_agg — so the database returns the response JSON directly
// and there is no N+1 problem to paper over.
package compile

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Statement is a single executable SQL statement. The statement always
// returns a single row with a single jsonb column named "data" (possibly
// zero rows for missed single-row lookups).
type Statement struct {
	SQL  string
	Args []any
	// ReturnsRow is false when zero rows means null (single lookups).
	Mutation bool
}

// Request is one root field to compile.
type Request struct {
	Field     *ast.Field
	Fragments ast.FragmentDefinitionList
	Vars      map[string]any
	Built     *schema.Built
	MaxDepth  int
	// MaxPageSize is the largest accepted first/last (larger is rejected)
	// and the default page size when neither is given. Values <= 0 fall back
	// to defaultMaxPageSize.
	MaxPageSize int
}

// Func compiles a request; CompileHook plugins wrap it middleware-style.
type Func func(ctx context.Context, req *Request) (*Statement, error)

// Compiler compiles GraphQL fields against a built schema.
type Compiler struct {
	Built *schema.Built
}

func New(built *schema.Built) *Compiler { return &Compiler{Built: built} }

// defaultMaxPageSize backstops the page-size cap when the request does not
// carry one, so no compile path can ever emit an unbounded scan.
const defaultMaxPageSize = 100

// Compile is the base Func.
func (c *Compiler) Compile(ctx context.Context, req *Request) (*Statement, error) {
	s := &stmt{c: c, req: req, maxDepth: req.MaxDepth, maxPageSize: req.MaxPageSize}
	if s.maxDepth <= 0 {
		s.maxDepth = 50
	}
	if s.maxPageSize <= 0 {
		s.maxPageSize = defaultMaxPageSize
	}
	meta := c.fieldMeta(rootTypeName(req), req.Field.Name)
	if meta == nil {
		return nil, fmt.Errorf("compile: no metadata for field %q", req.Field.Name)
	}
	var sql string
	var err error
	switch meta.Kind {
	case schema.KindRowByKey:
		sql, err = s.rowByKey(meta, req.Field)
	case schema.KindListQuery:
		sql, err = s.listQuery(meta, req.Field)
	case schema.KindConnectionQuery:
		sql, err = s.connectionQuery(meta, req.Field)
	case schema.KindCreate, schema.KindUpdate, schema.KindDelete, schema.KindUpsert,
		schema.KindCreateMany, schema.KindUpdateMany, schema.KindDeleteMany:
		sql, err = s.mutation(meta, req.Field)
	case schema.KindFunction:
		sql, err = s.function(meta, req.Field)
	case schema.KindNode:
		sql, err = s.nodeQuery(req.Field)
	case schema.KindSynthetic:
		sql = "SELECT NULL::jsonb AS data"
	default:
		err = fmt.Errorf("compile: unsupported root field kind %q", meta.Kind)
	}
	if err != nil {
		return nil, err
	}
	// Helpers that render SQL without an error return (valueExpr and the
	// spatial builders it calls) park failures on the statement; surface them
	// here so a bad value never reaches the database.
	if err := s.err(); err != nil {
		return nil, err
	}
	return &Statement{
		SQL:  sql,
		Args: s.args,
		Mutation: meta.Kind == schema.KindCreate || meta.Kind == schema.KindUpdate ||
			meta.Kind == schema.KindDelete || meta.Kind == schema.KindUpsert ||
			meta.Kind == schema.KindCreateMany || meta.Kind == schema.KindUpdateMany ||
			meta.Kind == schema.KindDeleteMany,
	}, nil
}

func rootTypeName(req *Request) string {
	if req.Field.ObjectDefinition != nil {
		return req.Field.ObjectDefinition.Name
	}
	return "Query"
}

// stmt carries per-statement state: the parameter list and an alias counter.
type stmt struct {
	c           *Compiler
	req         *Request
	args        []any
	aliasN      int
	maxDepth    int
	maxPageSize int
	// overrides remaps "schema.table" to an alternate row source (e.g. a
	// CTE holding rows inserted earlier in the same statement, which a
	// plain table scan could not see within one snapshot).
	overrides map[string]string
	// deferred holds the first error raised from a helper with no error
	// return (valueExpr and friends). Callers surface it via s.err().
	deferred error
	// mutAliases records the aliases the mutation payload reads the __mut CTE
	// under, so the DML's RETURNING list can project only the columns the
	// payload actually references (RETURNING requires SELECT privilege per
	// column — RETURNING * would demand SELECT on every column and break
	// tables using column-level grants).
	mutAliases []string
}

// fail records an error from a rendering helper that cannot return one. The
// first error wins; compilation aborts before the statement is executed.
func (s *stmt) fail(err error) {
	if s.deferred == nil {
		s.deferred = err
	}
}

// err returns the first deferred rendering error, if any.
func (s *stmt) err() error { return s.deferred }

// sourceRef resolves the SQL row source for a table, honouring overrides.
func (s *stmt) sourceRef(t *introspect.Table) string {
	if ov, ok := s.overrides[t.Schema+"."+t.Name]; ok {
		return ov
	}
	return tableRef(t)
}

// param appends a value and returns its placeholder.
func (s *stmt) param(v any) string {
	s.args = append(s.args, v)
	return "$" + strconv.Itoa(len(s.args))
}

func (s *stmt) alias(prefix string) string {
	s.aliasN++
	return fmt.Sprintf("__%s_%d", prefix, s.aliasN)
}

func (s *stmt) fieldMeta(typeName, fieldName string) *schema.FieldMeta {
	return s.c.fieldMeta(typeName, fieldName)
}

func (c *Compiler) fieldMeta(typeName, fieldName string) *schema.FieldMeta {
	if m, ok := c.Built.Meta[typeName]; ok {
		return m[fieldName]
	}
	return nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func tableRef(t *introspect.Table) string {
	return quoteIdent(t.Schema) + "." + quoteIdent(t.Name)
}

// argValue resolves a field argument to a Go value (nil if absent).
func (s *stmt) argValue(f *ast.Field, name string) (any, bool) {
	arg := f.Arguments.ForName(name)
	if arg == nil {
		return nil, false
	}
	v, err := arg.Value.Value(s.req.Vars)
	if err != nil || v == nil {
		return nil, false
	}
	return v, true
}

// expandFor resolves fragment spreads and inline fragments to a flat field
// list, keeping only fragments whose type condition matches typeName (either
// exactly or via an interface typeName implements, e.g. `... on Node`) and
// dropping selections excluded by @skip/@include.
func (s *stmt) expandFor(sel ast.SelectionSet, typeName string) []*ast.Field {
	var out []*ast.Field
	for _, item := range sel {
		switch v := item.(type) {
		case *ast.Field:
			if SkipByDirectives(v.Directives, s.req.Vars) {
				continue
			}
			out = append(out, v)
		case *ast.InlineFragment:
			if SkipByDirectives(v.Directives, s.req.Vars) {
				continue
			}
			if s.fragmentApplies(v.TypeCondition, typeName) {
				out = append(out, s.expandFor(v.SelectionSet, typeName)...)
			}
		case *ast.FragmentSpread:
			if SkipByDirectives(v.Directives, s.req.Vars) {
				continue
			}
			if def := s.req.Fragments.ForName(v.Name); def != nil && s.fragmentApplies(def.TypeCondition, typeName) {
				out = append(out, s.expandFor(def.SelectionSet, typeName)...)
			}
		}
	}
	return out
}

func (s *stmt) fragmentApplies(cond, typeName string) bool {
	if cond == "" || cond == typeName {
		return true
	}
	def := s.c.Built.Schema.Types[typeName]
	if def == nil {
		return false
	}
	for _, iface := range def.Interfaces {
		if iface == cond {
			return true
		}
	}
	return false
}

// ---- root shapes ----

func (s *stmt) rowByKey(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	t := meta.Table
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(t, f.SelectionSet, alias, 1)
	if err != nil {
		return "", err
	}
	conds, err := s.keyConds(t, meta.KeyColumns, f, alias)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE %s\nLIMIT 1",
		jsonExpr, s.sourceRef(t), alias, joinLaterals(laterals), strings.Join(conds, " AND ")), nil
}

// keyConds builds `alias."col" = $n` for each key column, reading the
// matching GraphQL argument (argument names are the inflected column names,
// matched positionally against KeyColumns via the field definition order).
func (s *stmt) keyConds(t *introspect.Table, keyCols []string, f *ast.Field, alias string) ([]string, error) {
	if f.Definition == nil || len(f.Definition.Arguments) < len(keyCols) {
		return nil, fmt.Errorf("compile: field %s: missing key argument definitions", f.Name)
	}
	var conds []string
	for i, col := range keyCols {
		argDef := f.Definition.Arguments[i]
		v, ok := s.argValue(f, argDef.Name)
		if !ok {
			return nil, fmt.Errorf("compile: field %s: missing key argument %q", f.Name, argDef.Name)
		}
		c := t.Column(col)
		conds = append(conds, fmt.Sprintf("%s.%s = %s", alias, quoteIdent(col), s.valueExpr(c, v)))
	}
	return conds, nil
}

func (s *stmt) listQuery(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	inner, err := s.listInner(meta.Table, f, "", "", 1)
	if err != nil {
		return "", err
	}
	sub := s.alias("s")
	return fmt.Sprintf("SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) AS data\nFROM (\n%s\n) AS %s",
		sub, indent(inner), sub), nil
}

// listInner builds `SELECT <json> AS data FROM ... WHERE ... ORDER ... LIMIT`
// for a table-valued field, applying filter/orderBy/first/offset arguments.
// extraCond correlates the subquery to a parent row for backward relations.
func (s *stmt) listInner(t *introspect.Table, f *ast.Field, extraCond, extraSelect string, depth int) (string, error) {
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(t, f.SelectionSet, alias, depth)
	if err != nil {
		return "", err
	}
	where, err := s.whereClause(t, f, alias, extraCond)
	if err != nil {
		return "", err
	}
	order := s.orderClause(t, f, alias)
	page, err := s.pageClause(f, 0)
	if err != nil {
		return "", err
	}
	sel := jsonExpr + " AS data"
	if extraSelect != "" {
		sel += ", " + extraSelect
	}
	// extraCond may reference the child alias via the %%ALIAS%% placeholder,
	// since the alias is only allocated here.
	q := fmt.Sprintf("SELECT %s\nFROM %s AS %s%s%s%s%s", sel, s.sourceRef(t), alias, joinLaterals(laterals), where, order, page)
	return strings.ReplaceAll(q, "%%ALIAS%%", alias), nil
}

func (s *stmt) whereClause(t *introspect.Table, f *ast.Field, alias, extraCond string) (string, error) {
	var conds []string
	if extraCond != "" {
		conds = append(conds, extraCond)
	}
	if fv, ok := s.argValue(f, "filter"); ok {
		filterType := argTypeName(f, "filter")
		cond, err := s.filterCond(t, filterType, fv, alias)
		if err != nil {
			return "", err
		}
		if cond != "" {
			conds = append(conds, cond)
		}
	}
	if cv, ok := s.argValue(f, "condition"); ok {
		cond, err := s.conditionCond(t, argTypeName(f, "condition"), cv, alias)
		if err != nil {
			return "", err
		}
		if cond != "" {
			conds = append(conds, cond)
		}
	}
	if len(conds) == 0 {
		return "", nil
	}
	return "\nWHERE " + strings.Join(conds, " AND "), nil
}

// orderTerm is one resolved ORDER BY column with its direction.
type orderTerm struct {
	col string
	// fn set: order by a computed column (row-type function) instead of col.
	fn *introspect.Function
	// rel set: order by a related table's column (col stays empty; the sort
	// column lives in rel.Column).
	rel *schema.OrderRelation
	// near set: order by spatial distance from this already-parameterized
	// geometry expression to col (postgis plugin).
	near string
	desc bool
}

// orderTerms maps orderBy enum values to order terms, always appending the
// primary key for a stable order when available.
func (s *stmt) orderTerms(t *introspect.Table, f *ast.Field) []orderTerm {
	var terms []orderTerm
	usedPK := false
	if ov, ok := s.argValue(f, "orderBy"); ok {
		enumName := argTypeName(f, "orderBy")
		specs := s.c.Built.OrderBy[enumName]
		vals, _ := ov.([]any)
		if len(vals) == 0 {
			if sv, isStr := ov.(string); isStr {
				vals = []any{sv}
			}
		}
		for _, v := range vals {
			name, _ := v.(string)
			if spec, ok := specs[name]; ok {
				near := ""
				if spec.Distance {
					// Distance ordering needs a reference geometry; without
					// near: the value is meaningless, so drop the term rather
					// than emit an unordered scan that looks sorted.
					expr, err := s.nearExpr(t, spec.Column, f)
					if err != nil {
						s.fail(err)
						continue
					}
					if expr == "" {
						continue
					}
					near = expr
				}
				terms = append(terms, orderTerm{col: spec.Column, fn: spec.Computed, rel: spec.Relation, near: near, desc: spec.Desc})
				if t.PrimaryKey != nil && len(t.PrimaryKey.Columns) == 1 && spec.Column == t.PrimaryKey.Columns[0] {
					usedPK = true
				}
			}
		}
	}
	if t.PrimaryKey != nil && !usedPK {
		for _, col := range t.PrimaryKey.Columns {
			terms = append(terms, orderTerm{col: col})
		}
	}
	return terms
}

// renderOrder renders terms as an ORDER BY clause over t rows at alias;
// reversed flips every direction (backward pagination). Descending terms
// override PostgreSQL's DESC = NULLS FIRST default so NULLs sort last in
// both directions; a reversed scan emits the exact mirror (ASC NULLS FIRST)
// so backward pagination stays a true inverse of the forward order.
func (s *stmt) renderOrder(t *introspect.Table, terms []orderTerm, alias string, reversed bool) string {
	if len(terms) == 0 {
		return ""
	}
	parts := make([]string, len(terms))
	for i, term := range terms {
		dir := " ASC"
		if term.desc != reversed {
			dir = " DESC"
		}
		if term.desc && orderTermNullable(t, term) {
			if reversed {
				dir = " ASC NULLS FIRST"
			} else {
				dir = " DESC NULLS LAST"
			}
		}
		parts[i] = s.orderExprSQL(t, term, alias) + dir
	}
	return "\nORDER BY " + strings.Join(parts, ", ")
}

// orderTermNullable reports whether a term's sort expression can yield NULL:
// computed functions and relational subqueries always can; plain columns
// only when not declared NOT NULL.
func orderTermNullable(t *introspect.Table, term orderTerm) bool {
	if term.fn != nil || term.rel != nil {
		return true
	}
	c := t.Column(term.col)
	return c == nil || !c.NotNull
}

// orderExprSQL renders the sort expression for one term: a column reference,
// the computed function called on the current row, or a correlated scalar
// subquery over a related table.
func (s *stmt) orderExprSQL(t *introspect.Table, term orderTerm, alias string) string {
	if term.near != "" {
		// <-> is the KNN operator: a GiST index answers it directly, which is
		// what makes "nearest N" cheap.
		return postgis.KNNSQL(alias+"."+quoteIdent(term.col), term.near)
	}
	if term.rel != nil {
		return s.relationOrderSQL(term, alias)
	}
	if term.fn != nil {
		return computedCallSQL(term.fn, t, alias)
	}
	return alias + "." + quoteIdent(term.col)
}

// relationOrderSQL renders the correlated scalar subquery for a relational
// order term. Forward relations select the sort column off the single
// referenced row; reverse relations aggregate it over the referencing rows
// (MIN for ASC terms, MAX for DESC), yielding NULL when no related row
// exists so those rows sort last (renderOrder emits NULLS LAST for
// descending terms). The inner alias is
// fixed: relational subqueries never nest, so it cannot collide.
func (s *stmt) relationOrderSQL(term orderTerm, alias string) string {
	rel := term.rel
	const sub = "__ro"
	joins := make([]string, len(rel.FK.Columns))
	for i, col := range rel.FK.Columns {
		if rel.Forward {
			joins[i] = fmt.Sprintf("%s.%s = %s.%s",
				sub, quoteIdent(rel.FK.RefColumns[i]), alias, quoteIdent(col))
		} else {
			joins[i] = fmt.Sprintf("%s.%s = %s.%s",
				sub, quoteIdent(col), alias, quoteIdent(rel.FK.RefColumns[i]))
		}
	}
	expr := sub + "." + quoteIdent(rel.Column)
	if !rel.Forward {
		agg := "MIN"
		if term.desc {
			agg = "MAX"
		}
		expr = agg + "(" + expr + ")"
	}
	return fmt.Sprintf("(SELECT %s FROM %s AS %s WHERE %s)",
		expr, s.sourceRef(rel.Table), sub, strings.Join(joins, " AND "))
}

func (s *stmt) orderClause(t *introspect.Table, f *ast.Field, alias string) string {
	return s.renderOrder(t, s.orderTerms(t, f), alias, false)
}

// distinctCols resolves the distinctOn argument to column names via the
// enum's Column bindings (collected into Built.OrderBy).
func (s *stmt) distinctCols(f *ast.Field) ([]string, error) {
	v, ok := s.argValue(f, "distinctOn")
	if !ok {
		return nil, nil
	}
	specs := s.c.Built.OrderBy[argTypeName(f, "distinctOn")]
	vals, _ := v.([]any)
	if len(vals) == 0 {
		if sv, isStr := v.(string); isStr {
			vals = []any{sv}
		}
	}
	var cols []string
	for _, item := range vals {
		name, _ := item.(string)
		spec, ok := specs[name]
		if !ok || spec.Column == "" {
			return nil, errUser("unknown distinctOn value %q", name)
		}
		cols = append(cols, spec.Column)
	}
	return cols, nil
}

// reorderForDistinct puts the DISTINCT ON columns at the head of the order
// terms (PostgreSQL requires them leftmost in ORDER BY), keeping a matching
// orderBy direction when one was given and dropping the duplicates.
func reorderForDistinct(terms []orderTerm, dcols []string) []orderTerm {
	if len(dcols) == 0 {
		return terms
	}
	used := map[string]bool{}
	var out []orderTerm
	for _, dc := range dcols {
		if used[dc] {
			continue
		}
		used[dc] = true
		desc := false
		for _, term := range terms {
			if term.fn == nil && term.col == dc {
				desc = term.desc
				break
			}
		}
		out = append(out, orderTerm{col: dc, desc: desc})
	}
	for _, term := range terms {
		if term.fn == nil && used[term.col] {
			continue
		}
		out = append(out, term)
	}
	return out
}

// pageClause renders LIMIT/OFFSET from first/offset (+ cursor extraOffset).
// first above maxPageSize is rejected; maxPageSize is the default when first
// is absent, so a LIMIT clause is always emitted.
func (s *stmt) pageClause(f *ast.Field, extraOffset int) (string, error) {
	limit := s.maxPageSize
	if v, ok := s.argValue(f, "first"); ok {
		if n, ok := toInt(v); ok {
			if n < 0 {
				return "", errUser("first must be >= 0")
			}
			if int(n) > s.maxPageSize {
				return "", errPageSize("first", int(n), s.maxPageSize)
			}
			limit = int(n)
		}
	}
	out := "\nLIMIT " + s.param(limit)
	off := extraOffset
	if v, ok := s.argValue(f, "offset"); ok {
		if n, ok := toInt(v); ok {
			if n < 0 {
				return "", errUser("offset must be >= 0")
			}
			off += int(n)
		}
	}
	if off > 0 {
		out += "\nOFFSET " + s.param(off)
	}
	return out, nil
}

func encodeCursorSQL(rnExpr string, offset int) string {
	return fmt.Sprintf("encode(convert_to('o:' || (%s + %d)::text, 'UTF8'), 'base64')", rnExpr, offset)
}

func decodeCursor(cur string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(cur)
	if err != nil {
		return 0, errUser("malformed cursor")
	}
	str := string(raw)
	if !strings.HasPrefix(str, "o:") {
		return 0, errUser("malformed cursor")
	}
	n, err := strconv.Atoi(str[2:])
	if err != nil || n < 0 {
		return 0, errUser("malformed cursor")
	}
	return n, nil
}

func toInt(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// argTypeName returns the named type of an argument from the validated
// field definition (e.g. the filter input type or orderBy enum type).
func argTypeName(f *ast.Field, arg string) string {
	if f.Definition == nil {
		return ""
	}
	def := f.Definition.Arguments.ForName(arg)
	if def == nil {
		return ""
	}
	return def.Type.Name()
}

func joinLaterals(laterals []string) string {
	if len(laterals) == 0 {
		return ""
	}
	return "\n" + strings.Join(laterals, "\n")
}

func indent(sql string) string {
	return "  " + strings.ReplaceAll(sql, "\n", "\n  ")
}
