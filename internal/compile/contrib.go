package compile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/schema"
)

// contribFor resolves a column's contrib mapping as the built schema
// declared it: a column whose contrib type name was taken fell back to
// String in the schema and compiles as a plain column.
func contribFor(built *schema.Built, c *introspect.Column) contribtype.Column {
	ci := contribtype.For(built.Catalog, c)
	if ci.Declares() && !built.ContribTypes[ci.Scalar] {
		return contribtype.Column{Vocabs: ci.Vocabs}
	}
	return ci
}

// contribKey identifies everything contrib.ForColumn reads off a column (the
// domain tells an earth column from a plain cube one).
func contribKey(c *introspect.Column) string {
	return c.BaseTypeSchema + "|" + c.PGType + "|" + strconv.FormatBool(c.IsArray) + "|" + strconv.FormatBool(c.Spatial != nil) + "|" + c.EnumRef + "|" + c.Domain
}

// contrib resolves a column's contrib mapping, cached per column type on
// the compiler (columns built on the fly for function results share
// entries with real columns of the same type).
func (s *stmt) contrib(c *introspect.Column) contribtype.Column {
	if c == nil {
		return contribtype.Column{}
	}
	key := contribKey(c)
	if v, ok := s.c.contribCache.Load(key); ok {
		return v.(contribtype.Column)
	}
	ci := contribFor(s.c.Built, c)
	s.c.contribCache.Store(key, ci)
	return ci
}

// params registers values as bind parameters and returns their
// placeholders.
func (s *stmt) params(vals []any) []string {
	phs := make([]string, len(vals))
	for i, v := range vals {
		phs[i] = s.param(v)
	}
	return phs
}

// rangeOperand converts a PostGraphile-shaped range input
// ({start: {value, inclusive}, end: {value, inclusive}, isEmpty}, a missing
// bound being unbounded) into the {start, startInclusive, end, endInclusive,
// isEmpty} object contrib's range coercion takes. Other values pass through.
func rangeOperand(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	out := map[string]any{"startInclusive": false, "endInclusive": false}
	for _, side := range []string{"start", "end"} {
		b, _ := m[side].(map[string]any)
		if b == nil {
			continue
		}
		out[side] = b["value"]
		out[side+"Inclusive"] = b["inclusive"]
	}
	if e, ok := m["isEmpty"]; ok && e != nil {
		out["isEmpty"] = e
	}
	return out
}

// codecOperand adapts a GraphQL input value of the column's own type to its
// codec.
func codecOperand(ci contribtype.Column, v any) any {
	switch ci.Kind {
	case contribtype.Range:
		return rangeOperand(v)
	case contribtype.Multirange:
		list, ok := v.([]any)
		if !ok {
			return v
		}
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = rangeOperand(item)
		}
		return out
	}
	return v
}

// contribValue renders an input value of a contrib-owned column type as its
// typed codec expression (hstore($1::text[]), int4range($1::int4, ...),
// $1::"ext".citext, ...).
func (s *stmt) contribValue(ci contribtype.Column, v any) (string, error) {
	if ci.Kind == contribtype.Interval {
		return s.intervalValue(ci, v)
	}
	params, err := ci.Own.Codec.Params(codecOperand(ci, v))
	if err != nil {
		return "", errUser("%s", err.Error())
	}
	sql := ci.Own.Codec.SQL(s.params(params)...)
	if sql == "" {
		return "", fmt.Errorf("compile: %s codec rejected %d parameters", ci.Own.Name, len(params))
	}
	return sql, nil
}

// comparisonOps maps generic filter operators to the SQL operators they
// compare with.
var comparisonOps = map[string]string{
	"equalTo":              "=",
	"notEqualTo":           "<>",
	"lessThan":             "<",
	"lessThanOrEqualTo":    "<=",
	"greaterThan":          ">",
	"greaterThanOrEqualTo": ">=",
}

// contribOp compiles one operator of a contrib-owned (non-citext) column:
// the generic comparisons, rendered with the type's schema-qualified
// operator and a codec-typed operand, then the vocabulary operators.
func (s *stmt) contribOp(ci contribtype.Column, ref, op string, val any) (string, error) {
	if ci.Kind == contribtype.Interval {
		return s.intervalOp(ci, ref, op, val)
	}
	if sym, ok := comparisonOps[op]; ok {
		v, err := s.contribValue(ci, val)
		if err != nil {
			return "", err
		}
		return ref + " " + ci.Operator(sym) + " " + v, nil
	}
	switch op {
	case "isNull":
		if b, _ := val.(bool); b {
			return ref + " IS NULL", nil
		}
		return ref + " IS NOT NULL", nil
	case "in", "notIn":
		return s.contribInList(ci, ref, op, val)
	}
	return s.vocabOp(ci, ref, op, val)
}

// contribInList renders in/notIn over a list of values of the column's
// type, sent as one text[] parameter and cast to the type's array.
func (s *stmt) contribInList(ci contribtype.Column, ref, op string, val any) (string, error) {
	list, err := pgcontrib.TextList(val)
	if err != nil {
		return "", errUser("%s must be a list of strings", op)
	}
	arr := s.param(list) + "::text[]::" + ci.TypeSQL() + "[]"
	if op == "notIn" {
		return ref + " " + ci.Operator("<>") + " ALL(" + arr + ")", nil
	}
	return ref + " " + ci.Operator("=") + " ANY(" + arr + ")", nil
}

// vocabOp compiles a vocabulary operator (full-text, pg_trgm, hstore,
// ltree, range, inet) through contrib's parameterization protocol, and
// remembers full-text matches for rank fields and ordering.
func (s *stmt) vocabOp(ci contribtype.Column, ref, op string, val any) (string, error) {
	o, ok := ci.Op(op)
	if !ok {
		return "", errUser("unknown filter operator %q", op)
	}
	if o.Arg == pgcontrib.ArgRange {
		val = rangeOperand(val)
	}
	params, err := o.Params(val)
	if err != nil {
		return "", errUser("%s: %s", op, err.Error())
	}
	phs := s.params(params)
	sql := o.Render(ref, phs...)
	if sql == "" {
		return "", fmt.Errorf("compile: operator %s rejected %d parameters", op, len(params))
	}
	if o.Arg == pgcontrib.ArgFullText && s.rankScope {
		if _, seen := s.ranks[ref]; !seen {
			if s.ranks == nil {
				s.ranks = map[string]rankSource{}
			}
			s.ranks[ref] = rankSource{op: o, phs: phs}
		}
	}
	return sql, nil
}

// citextOp compiles the String operators on a citext column with citext's
// schema-qualified operators and citext-typed operands; Postgres would
// otherwise resolve them to the case-sensitive text operators whenever the
// extension's schema is off the search_path. ok is false for operators
// citext has no special form of.
func (s *stmt) citextOp(ci contribtype.Column, ref, op string, val any) (string, bool, error) {
	if sym, ok := comparisonOps[op]; ok {
		v, err := s.contribValue(ci, val)
		return ref + " " + ci.Operator(sym) + " " + v, true, err
	}
	pattern := func(sym string, v any) (string, bool, error) {
		p, err := s.contribValue(ci, v)
		return ref + " " + ci.Operator(sym) + " " + p, true, err
	}
	switch op {
	case "in", "notIn":
		sql, err := s.contribInList(ci, ref, op, val)
		return sql, true, err
	case "like":
		return pattern("~~", val)
	case "likeInsensitive":
		return pattern("~~*", val)
	case "startsWith":
		str, _ := val.(string)
		return pattern("~~", escapeLike(str)+"%")
	case "endsWith":
		str, _ := val.(string)
		return pattern("~~", "%"+escapeLike(str))
	}
	return "", false, nil
}

// fullTextOperand attaches the sibling config: of a FullTextFilterOps
// object to a matches* operand.
func fullTextOperand(ci contribtype.Column, op string, val, config any) any {
	if config == nil {
		return val
	}
	if o, ok := ci.Op(op); !ok || o.Arg != pgcontrib.ArgFullText {
		return val
	}
	return map[string]any{"query": val, "config": config}
}

// rankSource is the first full-text match compiled against a column
// reference in the conjunctive top level of a filter (see stmt.rankScope):
// its operator and placeholders, reused by rank expressions.
type rankSource struct {
	op  pgcontrib.Op
	phs []string
}

// rankRef is a pending rank expression: the filtered column reference whose
// match it ranks against and the reference it is rendered over (the same
// column, or the anchor row's in keyset subqueries).
type rankRef struct {
	key, render string
	// order is the orderBy value requiring the match; empty for rank
	// fields, which render NULL without one.
	order string
	// headline marks a headline field: render is then the document column
	// and options the ts_headline options text, bound when the match
	// exists (an unreferenced parameter would have no type).
	headline bool
	options  string
}

// rankMarker returns a placeholder for a rank expression. A row's rank
// depends on the filter of the list it is read through, which compiles
// after the selection and ordering, so markers are resolved once the whole
// statement is built (resolveRanks).
func (s *stmt) rankMarker(r rankRef) string {
	s.rankRefs = append(s.rankRefs, r)
	return rankMarkerText(len(s.rankRefs) - 1)
}

func rankMarkerText(i int) string { return "%%RANK_" + strconv.Itoa(i) + "%%" }

// headlineMarker renders a <field>Headline field: a rank marker over the
// document column the document: argument names, bounded to the field's
// max_document characters (contrib.HeadlineDocumentSQL), resolved like the
// rank of the same tsvector column, with the validated options bound as one
// text parameter whether or not any were given (none without the match).
func (s *stmt) headlineMarker(meta *schema.FieldMeta, f *ast.Field, alias string) (string, error) {
	docs, _ := meta.Nested["documents"].(map[string]*introspect.Column)
	v, _ := s.argValue(f, "document")
	name, _ := v.(string)
	doc := docs[name]
	if doc == nil {
		return "", errUser("%s: unknown document %q", f.Name, name)
	}
	opts, _ := s.argValue(f, "options")
	h, err := pgcontrib.ParseHeadlineOptions(opts)
	if err != nil {
		return "", errUser("%s: %s", f.Name, err.Error())
	}
	maxDoc, _ := meta.Nested["max_document"].(int)
	if maxDoc < 1 {
		maxDoc = pgcontrib.MaxHeadlineDocument
	}
	render := alias + "." + quoteIdent(doc.Name)
	if bounded := pgcontrib.HeadlineDocumentSQL(render, doc.PGType, strconv.Itoa(maxDoc)); bounded != "" {
		render = bounded
	}
	return s.rankMarker(rankRef{
		key:      alias + "." + quoteIdent(meta.Column.Name),
		render:   render,
		headline: true,
		options:  h.String(),
	}), nil
}

// resolveRanks replaces rank markers with ts_rank over the matched query,
// NULL for rank fields without a match, and fails rank ordering without
// one.
func (s *stmt) resolveRanks(sql string) (string, error) {
	for i, r := range s.rankRefs {
		expr := "NULL::float4"
		if r.headline {
			expr = "NULL::text"
		}
		if src, ok := s.ranks[r.key]; ok {
			if r.headline {
				if !strings.Contains(sql, rankMarkerText(i)) {
					continue
				}
				expr = src.op.HeadlineSQL(r.render, s.param(r.options), src.phs...)
			} else {
				expr = src.op.RankSQL(pgcontrib.RankFrequency, r.render, src.phs...)
			}
		} else if r.order != "" {
			return "", errUser("orderBy %s requires a matches filter on the same column", r.order)
		}
		sql = strings.ReplaceAll(sql, rankMarkerText(i), expr)
	}
	return sql, nil
}

// contribJSON renders a contrib-owned value whose JSON form differs from its
// text form: hstore as a JSON object, ranges as their selected
// {start {value inclusive} end {...}} objects, multiranges as lists of them,
// vector and halfvec values as arrays of numbers, cubes as lists of
// coordinates and earth locations as their selected {lat lon} objects.
func (s *stmt) contribJSON(ref string, c *introspect.Column, sel ast.SelectionSet) (string, bool) {
	ci := s.contrib(c)
	switch ci.Kind {
	case contribtype.Interval:
		return s.intervalJSON(ci, ref, sel), true
	case contribtype.Hstore, contribtype.Cube:
		return ci.Own.Codec.OutputJSONSQL(ref), true
	case contribtype.Earth:
		return s.latLonJSON(ci, ref, sel), true
	case contribtype.Vector:
		if !ci.List() {
			return "", false
		}
		return ci.Own.Codec.OutputJSONSQL(ref), true
	case contribtype.Range:
		v := s.alias("rg")
		return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS r) AS %s WHERE %s.r IS NOT NULL)",
			s.rangeObject(ci, v+".r", sel), ref, v, v), true
	case contribtype.Multirange:
		v, e := s.alias("mr"), s.alias("rg")
		return fmt.Sprintf("(SELECT CASE WHEN %s.m IS NULL THEN NULL ELSE coalesce((SELECT json_agg(%s ORDER BY %s.r) FROM unnest(%s.m) AS %s(r)), '[]'::json) END FROM (SELECT %s AS m) AS %s)",
			v, s.rangeObject(ci, e+".r", sel), e, v, e, ref, v), true
	}
	return "", false
}

// latLonJSON renders the selection of a LatLon object over an earth value,
// null for a NULL value. ref is evaluated once.
func (s *stmt) latLonJSON(ci contribtype.Column, ref string, sel ast.SelectionSet) string {
	v := s.alias("ll")
	e := v + ".e"
	var pairs []string
	for _, f := range s.expandFor(sel, ci.Scalar) {
		key := quoteLiteral(f.Alias)
		switch f.Name {
		case "__typename":
			pairs = append(pairs, key, quoteLiteral(ci.Scalar)+"::text")
		case "lat", "lon":
			name := "latitude"
			if f.Name == "lon" {
				name = "longitude"
			}
			a, _ := ci.Own.Accessor(name)
			pairs = append(pairs, key, a.SQL(e))
		}
	}
	return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS e) AS %s WHERE %s IS NOT NULL)", buildJSONObject(pairs), ref, v, e)
}

// rangeObject renders the selection of a range object over a non-null range
// reference.
func (s *stmt) rangeObject(ci contribtype.Column, ref string, sel ast.SelectionSet) string {
	var pairs []string
	for _, f := range s.expandFor(sel, ci.Scalar) {
		key := quoteLiteral(f.Alias)
		switch f.Name {
		case "__typename":
			pairs = append(pairs, key, quoteLiteral(ci.Scalar)+"::text")
		case "start", "end":
			pairs = append(pairs, key, s.rangeBound(ci, ref, f))
		case "isEmpty":
			a, _ := ci.Own.Accessor("isEmpty")
			pairs = append(pairs, key, a.SQL(ref))
		}
	}
	return buildJSONObject(pairs)
}

// rangeBound renders one bound object of a range, null when the range is
// unbounded on that side or empty.
func (s *stmt) rangeBound(ci contribtype.Column, ref string, f *ast.Field) string {
	value, inclusive := "start", "startInclusive"
	if f.Name == "end" {
		value, inclusive = "end", "endInclusive"
	}
	va, _ := ci.Own.Accessor(value)
	ia, _ := ci.Own.Accessor(inclusive)
	val := va.SQL(ref)
	boundType := contribtype.RangeBound(ci.Scalar)
	var pairs []string
	for _, bf := range s.expandFor(f.SelectionSet, boundType) {
		key := quoteLiteral(bf.Alias)
		switch bf.Name {
		case "__typename":
			pairs = append(pairs, key, quoteLiteral(boundType)+"::text")
		case "value":
			pairs = append(pairs, key, s.exprJSON(val, &introspect.Column{PGType: ci.Elem}))
		case "inclusive":
			pairs = append(pairs, key, ia.SQL(ref))
		}
	}
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", val, buildJSONObject(pairs))
}

// exprTerm resolves an orderBy value sorting by a contrib expression of a
// column against a per-column reference: pg_trgm similarity or Levenshtein
// distance (spec.Similarity, referenced by similarTo:) or a pgvector, cube
// or earthdistance distance (spec.Vector, referenced by the spec.Reference
// argument, nearVector: or nearPoint:). The reference's parameters are
// allocated here.
func (s *stmt) exprTerm(t *introspect.Table, f *ast.Field, value string, spec schema.OrderSpec) (orderTerm, error) {
	col := t.Column(spec.Column)
	if col == nil {
		return orderTerm{}, fmt.Errorf("compile: order column %q not found", spec.Column)
	}
	name, arg := spec.Similarity, "similarTo"
	if spec.Vector != "" {
		name, arg = spec.Vector, spec.Reference
	}
	expr, err := s.referenceExpr(col, arg, name)
	if err != nil {
		return orderTerm{}, err
	}
	ref := s.referenceValue(f, arg, spec.Column)
	if ref == nil {
		return orderTerm{}, errUser("orderBy %s requires a %s: reference for the same column", value, arg)
	}
	params, err := expr.Params(ref)
	if err != nil {
		return orderTerm{}, errUser("%s: %s", arg, err.Error())
	}
	return orderTerm{col: spec.Column, expr: &expr, exprArgs: s.params(params), desc: spec.Desc}, nil
}

// referenceExpr resolves the contrib expression name measured against the
// list argument arg: a pg_trgm expression or the fuzzystrmatch Levenshtein
// distance for similarTo:, a distance of the column's
// [contribtype.Column.Distances] vocabulary for nearVector: (pgvector,
// cube) and nearPoint: (earthdistance).
func (s *stmt) referenceExpr(col *introspect.Column, arg, name string) (pgcontrib.Expr, error) {
	ci := s.contrib(col)
	vocab, ok := ci.Trgm()
	if fz, fok := ci.Fuzzy(); fok {
		if _, has := fz.Expr(name); has {
			vocab, ok = fz, true
		}
	}
	if arg != "similarTo" {
		vocab, ok = ci.Distances()
	}
	if !ok {
		return pgcontrib.Expr{}, fmt.Errorf("compile: column %q has no vocabulary for %s", col.Name, name)
	}
	expr, ok := vocab.Expr(name)
	if !ok {
		return pgcontrib.Expr{}, fmt.Errorf("compile: unknown %s expression %q", vocab.Name, name)
	}
	return expr, nil
}

// referenceValue returns the reference the list field f's argument arg
// (similarTo:, nearVector:, nearPoint:) holds for column col; nil when
// absent.
func (s *stmt) referenceValue(f *ast.Field, arg, col string) any {
	v, ok := s.argValue(f, arg)
	if !ok {
		return nil
	}
	m, _ := v.(map[string]any)
	for field, colName := range s.c.Built.InputColumns[argTypeName(f, arg)] {
		if colName == col {
			return m[field]
		}
	}
	return nil
}

// contribLiteral renders an input value of a contrib-owned type as the text
// literal its type input function parses: what jsonb_populate_record needs
// for a composite attribute, which it reads from a JSON string. hstore
// objects become "k"=>"v" pairs, ranges [lower,upper) literals (with
// multiranges as {...} lists of them) and cubes their text form; the other
// types already arrive in their text form. Earth locations have no literal
// short of computing ll_to_earth, so they are rejected.
func contribLiteral(ci contribtype.Column, v any) (any, error) {
	switch ci.Kind {
	case contribtype.Interval:
		return intervalLiteral(ci, v)
	case contribtype.Cube:
		c, err := pgcontrib.CubeParam(v)
		if err != nil {
			return nil, errUser("%s", err.Error())
		}
		return c, nil
	case contribtype.Earth:
		return nil, errUser("earth values are not supported inside composite inputs")
	case contribtype.Vector:
		params, err := ci.Own.Codec.Params(v)
		if err != nil {
			return nil, errUser("%s", err.Error())
		}
		return params[0], nil
	case contribtype.Hstore:
		pairs, err := pgcontrib.HstoreParam(v)
		if err != nil {
			return nil, errUser("%s", err.Error())
		}
		parts := make([]string, 0, len(pairs)/2)
		for i := 0; i+1 < len(pairs); i += 2 {
			val := "NULL"
			if pairs[i+1] != nil {
				val = quoteLiteralValue(*pairs[i+1])
			}
			parts = append(parts, quoteLiteralValue(*pairs[i])+"=>"+val)
		}
		return strings.Join(parts, ", "), nil
	case contribtype.Range:
		r, err := pgcontrib.ParseRange(rangeOperand(v), integralElem(ci.Elem))
		if err != nil {
			return nil, errUser("%s", err.Error())
		}
		return rangeLiteral(r), nil
	case contribtype.Multirange:
		list, ok := v.([]any)
		if !ok {
			return nil, errUser("multirange value must be a list of ranges")
		}
		parts := make([]string, len(list))
		for i, item := range list {
			r, err := pgcontrib.ParseRange(rangeOperand(item), integralElem(ci.Elem))
			if err != nil {
				return nil, errUser("%s", err.Error())
			}
			parts[i] = rangeLiteral(r)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	}
	return v, nil
}

func integralElem(elem string) bool { return elem == "int2" || elem == "int4" || elem == "int8" }

// rangeLiteral renders a range in Postgres's range text format, quoting
// bounds so any element text survives.
func rangeLiteral(r pgcontrib.RangeValue) string {
	if r.Empty {
		return "empty"
	}
	bound := func(b *string) string {
		if b == nil {
			return ""
		}
		return quoteLiteralValue(*b)
	}
	b := r.Bounds()
	return b[:1] + bound(r.Start) + "," + bound(r.End) + b[1:]
}

// quoteLiteralValue double-quotes a value for the hstore and range text
// formats, backslash-escaping quotes and backslashes.
func quoteLiteralValue(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
