package compile

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// jsonbBuildObjectChunk keeps each jsonb_build_object call under PostgreSQL's
// 100-argument limit; wider selections are concatenated with ||.
const jsonbBuildObjectChunk = 40 // pairs (80 args)

// rowJSON compiles a selection set over a table into a jsonb expression plus
// the LEFT JOIN LATERAL clauses it needs against `alias`.
func (s *stmt) rowJSON(t *introspect.Table, sel ast.SelectionSet, alias string, depth int) (string, []string, error) {
	if depth > s.maxDepth {
		return "", nil, errUser("selection depth exceeds limit (%d)", s.maxDepth)
	}
	typeName := s.c.Built.TypeForTable[t.Schema+"."+t.Name]
	fields := s.expandFor(sel, typeName)
	if len(fields) == 0 {
		// Validation guarantees a non-empty selection set, so this only
		// happens when @skip/@include or non-matching fragments removed every
		// field — per spec the result is an empty object, not an error.
		return "'{}'::jsonb", nil, nil
	}
	var pairs []string // key expr, value expr alternating
	var laterals []string
	for _, f := range fields {
		key := quoteLiteral(f.Alias)
		if f.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(typeName)+"::text")
			continue
		}
		meta := s.fieldMeta(typeName, f.Name)
		if meta == nil {
			return "", nil, fmt.Errorf("compile: no metadata for %s.%s", typeName, f.Name)
		}
		switch meta.Kind {
		case schema.KindColumn:
			if comp := s.compositeFor(meta.Column); comp != nil {
				expr, err := s.compositeJSON(alias+"."+quoteIdent(meta.Column.Name), comp, meta.Column.IsArray, f.SelectionSet, depth+1)
				if err != nil {
					return "", nil, err
				}
				pairs = append(pairs, key, expr)
				continue
			}
			if postgis.IsSpatial(meta.Column.PGType) && !meta.Column.IsArray {
				// transform:/simplify: arguments apply before GeoJSON encoding.
				expr, err := s.spatialFieldSQL(alias, meta.Column, f)
				if err != nil {
					return "", nil, err
				}
				pairs = append(pairs, key, expr)
				continue
			}
			pairs = append(pairs, key, s.columnJSON(alias, meta.Column))
		case schema.KindSpatialDerived:
			suffix, _ := meta.Nested["suffix"].(string)
			expr, err := s.spatialDerivedSQL(alias, meta.Column, suffix)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindNodeID:
			pairs = append(pairs, key, nodeIDSQL(typeName, meta.Table, alias))
		case schema.KindComputed:
			expr, err := s.computedJSON(meta, f, alias, depth+1)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindRelationForward:
			expr, lat, err := s.forwardRelation(meta, f, alias, depth+1)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
			laterals = append(laterals, lat)
		case schema.KindRelationBackward:
			expr, lat, err := s.backwardRelation(meta, f, alias, depth+1)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
			laterals = append(laterals, lat)
		default:
			return "", nil, fmt.Errorf("compile: field %s.%s (kind %s) is not selectable here", typeName, f.Name, meta.Kind)
		}
	}
	return buildJSONObject(pairs), laterals, nil
}

// buildJSONObject renders pairs into chunked jsonb_build_object calls.
func buildJSONObject(pairs []string) string {
	if len(pairs) == 0 {
		return "'{}'::jsonb"
	}
	var chunks []string
	for i := 0; i < len(pairs); i += jsonbBuildObjectChunk * 2 {
		end := min(i+jsonbBuildObjectChunk*2, len(pairs))
		chunks = append(chunks, "jsonb_build_object("+strings.Join(pairs[i:end], ", ")+")")
	}
	return strings.Join(chunks, " || ")
}

// columnJSON renders a column value for inclusion in the JSON response.
func (s *stmt) columnJSON(alias string, c *introspect.Column) string {
	ref := alias + "." + quoteIdent(c.Name)
	if c.IsArray {
		if f := postgis.FlavourOf(c.PGType); f.Spatial() {
			// to_jsonb on a geometry[] would emit WKB hex strings; map each
			// element through ST_AsGeoJSON instead.
			return s.spatialArrayJSON(ref)
		}
		if expr, ok := s.enumArrayJSON(ref, c); ok {
			return expr
		}
		return "to_jsonb(" + ref + ")"
	}
	return s.exprJSON(ref, c)
}

// spatialArrayJSON renders a geometry/geography array as a JSON array of
// GeoJSON objects, preserving element order and NULL-ness of the array itself.
func (s *stmt) spatialArrayJSON(ref string) string {
	elem := s.alias("g")
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(%s ORDER BY %s.o), '[]'::jsonb) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
		ref, postgis.OutputSQL("("+ref+")["+elem+".o]"), elem, ref, elem)
}

// exprJSON renders a scalar value expression for inclusion in the JSON
// response, applying representation fixes: 64-bit and arbitrary-precision
// numbers are serialized as strings (BigInt/BigFloat scalars), bytea as
// base64, and enum labels map back to their GraphQL enum value names.
func (s *stmt) exprJSON(ref string, c *introspect.Column) string {
	if expr, ok := s.enumCase(ref, c); ok {
		return expr
	}
	if postgis.FlavourOf(c.PGType).Spatial() {
		// PostGIS values render as WKB hex by default; the GeoJSON scalar
		// contract is a real JSON object.
		return postgis.OutputSQL(ref)
	}
	switch c.PGType {
	case "int8", "numeric", "money":
		return "(" + ref + ")::text"
	case "bytea":
		return "encode(" + ref + ", 'base64')"
	default:
		return ref
	}
}

// computedJSON compiles a computed-column field: the row-type function called
// with an explicit ROW(...)::table cast, which works over any row source —
// a table scan, a function row source, or a mutation RETURNING CTE (whose
// whole-row variable is an anonymous record the function would reject).
// Set-returning functions aggregate into a scalar-transformed jsonb array
// (SETOF <scalar>) or a fully-selected row array (SETOF <table>).
func (s *stmt) computedJSON(meta *schema.FieldMeta, f *ast.Field, alias string, depth int) (string, error) {
	fn := meta.Function
	params := []string{rowParamSQL(meta.Table, alias)}
	for _, a := range fn.Args[1:] {
		argDef := f.Arguments.ForName(inflect.LowerCamel(a.Name))
		if argDef == nil {
			params = append(params, "NULL")
			continue
		}
		v, err := argDef.Value.Value(s.req.Vars)
		if err != nil {
			return "", err
		}
		params = append(params, s.argParam(a, v)+"::"+quoteIdent(a.PGType))
	}
	call := quoteIdent(fn.Schema) + "." + quoteIdent(fn.Name) + "(" + strings.Join(params, ", ") + ")"
	ret := &introspect.Column{
		PGType:     fn.ReturnType,
		TypeSchema: fn.ReturnTypeSchema,
		IsArray:    strings.HasPrefix(fn.ReturnType, "_"),
	}
	if fn.ReturnsSet {
		if rt := s.c.Built.Catalog.Table(fn.ReturnTypeSchema, fn.ReturnType); rt != nil && len(f.SelectionSet) > 0 {
			rowAlias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(rt, f.SelectionSet, rowAlias, depth)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			return fmt.Sprintf("(SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) FROM (\n%s\n) AS %s)",
				sub,
				indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, rowAlias, joinLaterals(laterals))),
				sub), nil
		}
		elem := s.alias("v")
		return fmt.Sprintf("(SELECT coalesce(jsonb_agg(%s), '[]'::jsonb) FROM %s AS %s(v))",
			s.exprJSON(elem+".v", ret), call, elem), nil
	}
	if ret.IsArray {
		// A function call is not a stable reference: enumArrayJSON would
		// subscript it repeatedly, re-evaluating the call per element.
		if _, ok := s.enumValuesFor(ret); ok {
			sub := s.alias("a")
			mapped, _ := s.enumArrayJSON(sub+".v", ret)
			return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS v) AS %s)", mapped, call, sub), nil
		}
		return "to_jsonb(" + call + ")", nil
	}
	return s.exprJSON(call, ret), nil
}

// rowParamSQL renders alias's row as an explicit ROW(...)::table value, which
// works over any row source — a table scan, a function row source, or a
// mutation RETURNING CTE (whose whole-row variable is an anonymous record).
// Referencing every column here is what forces the mutation RETURNING list to
// include the whole row when a computed field is selected — the function takes
// the full row type, so this is inherent, not an over-projection.
func rowParamSQL(t *introspect.Table, alias string) string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = alias + "." + quoteIdent(c.Name)
	}
	return "ROW(" + strings.Join(cols, ", ") + ")::" + tableRef(t)
}

// computedCallSQL renders a computed-column function called on the current
// row (single-row-argument functions only: filter/order positions have no
// GraphQL arguments to map).
func computedCallSQL(fn *introspect.Function, t *introspect.Table, alias string) string {
	return quoteIdent(fn.Schema) + "." + quoteIdent(fn.Name) + "(" + rowParamSQL(t, alias) + ")"
}

// compositeFor resolves a column to its catalog composite type, or nil.
func (s *stmt) compositeFor(c *introspect.Column) *introspect.Composite {
	if c == nil {
		return nil
	}
	return s.c.Built.Catalog.Composite(c.TypeSchema, strings.TrimPrefix(c.PGType, "_"))
}

// compositeJSON compiles a selection set over a composite-typed value into a
// jsonb expression. ref is a raw composite (or composite-array) value; NULL
// detection uses the text representation because `row IS NULL` is also true
// for a row whose attributes are all NULL.
func (s *stmt) compositeJSON(ref string, comp *introspect.Composite, isArray bool, sel ast.SelectionSet, depth int) (string, error) {
	if depth > s.maxDepth {
		return "", errUser("selection depth exceeds limit (%d)", s.maxDepth)
	}
	typeName := s.c.Built.CompositeTypeForPG[comp.Schema+"."+comp.Name]
	if isArray {
		// unnest(composite[]) in FROM expands the composite into columns, so
		// index the array by subscript instead to keep each element whole.
		elem := s.alias("e")
		inner, err := s.compositeJSON("("+ref+")["+elem+".o]", comp, false, sel, depth)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(%s ORDER BY %s.o), '[]'::jsonb) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
			ref, inner, elem, ref, elem), nil
	}
	fields := s.expandFor(sel, typeName)
	if len(fields) == 0 {
		// Every field removed by @skip/@include: empty object per spec.
		return fmt.Sprintf("CASE WHEN (%s)::text IS NULL THEN NULL ELSE '{}'::jsonb END", ref), nil
	}
	var pairs []string
	for _, f := range fields {
		key := quoteLiteral(f.Alias)
		if f.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(typeName)+"::text")
			continue
		}
		meta := s.fieldMeta(typeName, f.Name)
		if meta == nil || meta.Kind != schema.KindCompositeField {
			return "", fmt.Errorf("compile: no metadata for %s.%s", typeName, f.Name)
		}
		attr := "(" + ref + ")." + quoteIdent(meta.Column.Name)
		var expr string
		if nested := s.compositeFor(meta.Column); nested != nil {
			var err error
			expr, err = s.compositeJSON(attr, nested, meta.Column.IsArray, f.SelectionSet, depth+1)
			if err != nil {
				return "", err
			}
		} else if meta.Column.IsArray {
			if mapped, ok := s.enumArrayJSON(attr, meta.Column); ok {
				expr = mapped
			} else {
				expr = "to_jsonb(" + attr + ")"
			}
		} else {
			expr = s.exprJSON(attr, meta.Column)
		}
		pairs = append(pairs, key, expr)
	}
	return fmt.Sprintf("CASE WHEN (%s)::text IS NULL THEN NULL ELSE %s END", ref, buildJSONObject(pairs)), nil
}

// enumCase maps a pg enum value back to its GraphQL enum value name with a
// CASE expression (NULL falls through automatically). The column's element
// type is what matters, so an enum array resolves the same mapping; callers
// handling arrays use enumArrayJSON instead.
func (s *stmt) enumCase(ref string, c *introspect.Column) (string, bool) {
	values, ok := s.enumValuesFor(c)
	if !ok {
		return "", false
	}
	var b strings.Builder
	b.WriteString("CASE " + ref)
	for _, name := range sortedFilterKeys(anyMap(values)) {
		fmt.Fprintf(&b, " WHEN %s THEN %s", quoteLiteral(values[name]), quoteLiteral(name))
	}
	b.WriteString(" END")
	return b.String(), true
}

// enumValuesFor returns the GraphQL-value-name -> pg-value map for a column
// whose (element) type is an enum — native or table-backed (Column.EnumRef).
func (s *stmt) enumValuesFor(c *introspect.Column) (map[string]string, bool) {
	enumType, ok := enumTypeFor(s.c.Built, c)
	if !ok {
		return nil, false
	}
	values := s.c.Built.EnumValues[enumType]
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}

// enumArrayJSON renders an enum array as a JSON array of GraphQL enum value
// names. to_jsonb would emit the raw lowercase pg labels, which do not match
// the enum values the schema declares for the field ([Mood!]), so each element
// goes through the same CASE mapping the scalar path uses. Element order and
// the NULL-ness of the array itself are preserved.
func (s *stmt) enumArrayJSON(ref string, c *introspect.Column) (string, bool) {
	if _, ok := s.enumValuesFor(c); !ok {
		return "", false
	}
	elem := s.alias("m")
	mapped, _ := s.enumCase("("+ref+")["+elem+".o]", c)
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(jsonb_agg(%s ORDER BY %s.o), '[]'::jsonb) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
		ref, mapped, elem, ref, elem), true
}

// argParam renders a function argument value as a parameter, mapping GraphQL
// enum value names back to their pg labels first: enum-typed arguments are
// declared with the generated enum type, so the incoming value is a value name
// ("HAPPY") that PostgreSQL would reject as a label.
func (s *stmt) argParam(a introspect.FuncArg, v any) string {
	col := &introspect.Column{
		PGType:     a.PGType,
		TypeSchema: a.TypeSchema,
		IsArray:    strings.HasPrefix(a.PGType, "_"),
	}
	return s.param(s.coerce(col, v))
}

// fnReturnColumn describes a function's return type as a column, so the
// scalar/array JSON renderers can apply the same representation fixes they
// apply to real columns (enum value names, BigInt-as-string, bytea base64).
func fnReturnColumn(fn *introspect.Function) *introspect.Column {
	return &introspect.Column{
		PGType:     fn.ReturnType,
		TypeSchema: fn.ReturnTypeSchema,
		IsArray:    strings.HasPrefix(fn.ReturnType, "_"),
	}
}

// fnValueJSON renders a already-materialized function result value (a stable
// reference such as "__fn.v", not a call expression) as response JSON.
func (s *stmt) fnValueJSON(ref string, fn *introspect.Function) string {
	ret := fnReturnColumn(fn)
	if ret.IsArray {
		if expr, ok := s.enumArrayJSON(ref, ret); ok {
			return expr
		}
		return "to_jsonb(" + ref + ")"
	}
	return "to_jsonb(" + s.exprJSON(ref, ret) + ")"
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// forwardRelation compiles a many-to-one field: one lateral join fetching the
// referenced row's JSON.
func (s *stmt) forwardRelation(meta *schema.FieldMeta, f *ast.Field, parentAlias string, depth int) (string, string, error) {
	ref := meta.Table
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(ref, f.SelectionSet, alias, depth)
	if err != nil {
		return "", "", err
	}
	var conds []string
	for i, col := range meta.FK.Columns {
		conds = append(conds, fmt.Sprintf("%s.%s = %s.%s",
			alias, quoteIdent(meta.FK.RefColumns[i]), parentAlias, quoteIdent(col)))
	}
	latAlias := s.alias("l")
	lat := fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) AS %s ON true",
		indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE %s\nLIMIT 1",
			jsonExpr, s.sourceRef(ref), alias, joinLaterals(laterals), strings.Join(conds, " AND "))),
		latAlias)
	return latAlias + ".data", lat, nil
}

// backwardRelation compiles a one-to-many field as a nested connection: a
// lateral join whose subquery aggregates the child connection JSON.
func (s *stmt) backwardRelation(meta *schema.FieldMeta, f *ast.Field, parentAlias string, depth int) (string, string, error) {
	child := meta.Table
	var conds []string
	for i, col := range meta.FK.Columns {
		conds = append(conds, fmt.Sprintf("%%%%ALIAS%%%%.%s = %s.%s",
			quoteIdent(col), parentAlias, quoteIdent(meta.FK.RefColumns[i])))
	}
	body, err := s.connectionJSON(child, f, strings.Join(conds, " AND "), depth)
	if err != nil {
		return "", "", err
	}
	latAlias := s.alias("l")
	lat := fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) AS %s ON true", indent(body), latAlias)
	return latAlias + ".data", lat, nil
}

// connectionQuery compiles a root Relay connection field.
func (s *stmt) connectionQuery(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	return s.connectionJSON(meta.Table, f, "", 1)
}

// connectionJSON compiles a Relay connection selection into a complete
// one-row SELECT: nodes/edges from one paginated subquery, totalCount and
// pageInfo derived alongside. extraCond correlates child connections to a
// parent row via the %%ALIAS%% placeholder (replaced per row source).
//
// Two pagination modes:
//   - keyset (tables with a PK): cursors are nodeIds; after/before become
//     anchor-row lexicographic predicates; the scan fetches limit+1 rows so
//     hasNext/hasPreviousPage is exact in the paging direction; `last`
//     reverses the scan and the outer aggregates re-reverse.
//   - offset (PK-less tables, or legacy "o:<n>" cursors): the original
//     row_number()+OFFSET shapes.
func (s *stmt) connectionJSON(t *introspect.Table, f *ast.Field, extraCond string, depth int) (string, error) {
	typeName := s.c.Built.TypeForTable[t.Schema+"."+t.Name]
	connType := typeName + "Connection"
	plan, err := s.planPage(t, f, typeName)
	if err != nil {
		return "", err
	}
	dcols, err := s.distinctCols(f)
	if err != nil {
		return "", err
	}
	if len(dcols) > 0 {
		// DISTINCT ON changes row identity, so keyset cursors over base rows
		// no longer address result rows: force plain offset pagination.
		if plan.hasAfter && plan.predKeyset || plan.hasBefore || plan.reversed {
			return "", errUser("distinctOn supports only first/offset pagination")
		}
		plan.emitKeyset = false
		plan.predKeyset = false
	}

	fields := s.expandFor(f.SelectionSet, connType)
	var nodesSel, edgeNodeSel ast.SelectionSet
	var edgeFields, pageInfoFields []*ast.Field
	wantTotal, wantEdges, wantPageInfo := false, false, false
	for _, sub := range fields {
		switch sub.Name {
		case "nodes":
			nodesSel = sub.SelectionSet
		case "edges":
			wantEdges = true
			edgeFields = s.expandFor(sub.SelectionSet, typeName+"Edge")
			for _, ef := range edgeFields {
				if ef.Name == "node" {
					edgeNodeSel = ef.SelectionSet
				}
			}
		case "totalCount":
			wantTotal = true
		case "pageInfo":
			wantPageInfo = true
			pageInfoFields = s.expandFor(sub.SelectionSet, "PageInfo")
		}
	}

	// ---- inner paginated scan ----
	alias := s.alias("t")
	selectCols := []string{}
	var innerLaterals []string
	if nodesSel != nil {
		jsonExpr, laterals, err := s.rowJSON(t, nodesSel, alias, depth)
		if err != nil {
			return "", err
		}
		selectCols = append(selectCols, jsonExpr+" AS ndata")
		innerLaterals = append(innerLaterals, laterals...)
	}
	if edgeNodeSel != nil {
		jsonExpr, laterals, err := s.rowJSON(t, edgeNodeSel, alias, depth)
		if err != nil {
			return "", err
		}
		selectCols = append(selectCols, jsonExpr+" AS edata")
		innerLaterals = append(innerLaterals, laterals...)
	}
	if plan.emitKeyset && (wantEdges || wantPageInfo) {
		selectCols = append(selectCols, nodeIDSQL(typeName, t, alias)+" AS __cursor")
	}
	terms := reorderForDistinct(s.orderTerms(t, f), dcols)
	// __rn must number rows in the emitted order, so the window carries the
	// same ORDER BY as the scan: a bare OVER () is evaluated below the sort
	// node and would follow plan-dependent scan order instead.
	//
	// Under DISTINCT ON the window still numbers pre-distinct rows, leaving
	// gaps; a wrapper below renumbers the survivors, so the scan emits __rn0.
	rnName := "__rn"
	if len(dcols) > 0 {
		rnName = "__rn0"
	}
	rn := "row_number() OVER () AS " + rnName
	if o := s.renderOrder(t, terms, alias, plan.reversed); o != "" {
		rn = "row_number() OVER (" + strings.TrimPrefix(o, "\n") + ") AS " + rnName
	}
	selectCols = append(selectCols, rn)
	conds := []string{}
	if extraCond != "" {
		conds = append(conds, strings.ReplaceAll(extraCond, "%%ALIAS%%", alias))
	}
	if plan.predKeyset && plan.hasAfter {
		vals, err := s.anchorVals(t, terms, plan.afterKeys)
		if err != nil {
			return "", err
		}
		conds = append(conds, s.keysetPredicate(alias, vals, terms, t, false))
	}
	if plan.predKeyset && plan.hasBefore {
		vals, err := s.anchorVals(t, terms, plan.beforeKeys)
		if err != nil {
			return "", err
		}
		conds = append(conds, s.keysetPredicate(alias, vals, terms, t, true))
	}
	where, err := s.whereClause(t, f, alias, strings.Join(conds, " AND "))
	if err != nil {
		return "", err
	}
	order := s.renderOrder(t, terms, alias, plan.reversed)
	limit := ""
	trimParam := "" // placeholder trimming the +1 detection row (keyset mode)
	if plan.limit >= 0 {
		if plan.predKeyset {
			limit = "\nLIMIT " + s.param(plan.limit+1)
		} else {
			limit = "\nLIMIT " + s.param(plan.limit)
		}
	}
	pageOffset := ""
	if plan.offset > 0 {
		pageOffset = "\nOFFSET " + s.param(plan.offset)
	}
	distinct := ""
	if len(dcols) > 0 {
		refs := make([]string, len(dcols))
		for i, c := range dcols {
			refs[i] = alias + "." + quoteIdent(c)
		}
		distinct = "DISTINCT ON (" + strings.Join(refs, ", ") + ") "
	}
	inner := fmt.Sprintf("SELECT %s%s\nFROM %s AS %s%s%s%s%s%s",
		distinct, strings.Join(selectCols, ", "), s.sourceRef(t), alias,
		joinLaterals(innerLaterals), where, order, limit, pageOffset)
	if len(dcols) > 0 {
		// Renumber the distinct survivors so offset cursors stay dense.
		w := s.alias("d")
		inner = fmt.Sprintf("SELECT %s.*, row_number() OVER (ORDER BY %s.__rn0) AS __rn\nFROM (\n%s\n) AS %s",
			w, w, indent(inner), w)
	}

	sub := s.alias("s")

	// ---- outer aggregation ----
	aggOrder := fmt.Sprintf(" ORDER BY %s.__rn ASC", sub)
	if plan.reversed {
		aggOrder = fmt.Sprintf(" ORDER BY %s.__rn DESC", sub)
	}
	aggFilter := ""
	// Only allocate the trim parameter when an aggregate actually consumes it
	// (a totalCount-only selection has no aggregates; an unused parameter
	// would make pgx reject the statement with an argument-count mismatch).
	if plan.predKeyset && plan.limit >= 0 && (nodesSel != nil || wantEdges || wantPageInfo) {
		// __rn numbers rows in the ordered scan BEFORE OFFSET trims, so rows
		// surviving `LIMIT limit+1 OFFSET offset` carry __rn in
		// [offset+1, offset+limit+1] — the trim threshold is offset+limit.
		trimParam = s.param(plan.offset + plan.limit)
		aggFilter = fmt.Sprintf(" FILTER (WHERE %s.__rn <= %s)", sub, trimParam)
	}
	agg := func(expr string) string {
		return fmt.Sprintf("coalesce(jsonb_agg(%s%s)%s, '[]'::jsonb)", expr, aggOrder, aggFilter)
	}
	cursorExpr := encodeCursorSQL(sub+".__rn", plan.offset)
	if plan.emitKeyset {
		cursorExpr = sub + ".__cursor"
	}

	countSQL := ""
	if wantTotal || (wantPageInfo && !plan.predKeyset) {
		countAlias := s.alias("c")
		countExtra := ""
		if extraCond != "" {
			countExtra = strings.ReplaceAll(extraCond, "%%ALIAS%%", countAlias)
		}
		countWhere, err := s.whereClause(t, f, countAlias, countExtra)
		if err != nil {
			return "", err
		}
		if len(dcols) > 0 {
			// count(DISTINCT ...) would skip an all-NULL group that DISTINCT ON
			// keeps, so count over a DISTINCT subquery instead.
			refs := make([]string, len(dcols))
			for i, c := range dcols {
				refs[i] = countAlias + "." + quoteIdent(c)
			}
			countSQL = fmt.Sprintf("(SELECT count(*) FROM (SELECT DISTINCT %s FROM %s AS %s%s) AS %s)",
				strings.Join(refs, ", "), s.sourceRef(t), countAlias, countWhere, s.alias("c"))
		} else {
			countSQL = fmt.Sprintf("(SELECT count(*) FROM %s AS %s%s)", s.sourceRef(t), countAlias, countWhere)
		}
	}

	var pairs []string
	if nodesSel != nil {
		pairs = append(pairs, quoteLiteral("nodes"), agg(sub+".ndata"))
	}
	if wantEdges {
		var edgePairs []string
		for _, ef := range edgeFields {
			switch ef.Name {
			case "cursor":
				edgePairs = append(edgePairs, quoteLiteral(ef.Alias), cursorExpr)
			case "node":
				edgePairs = append(edgePairs, quoteLiteral(ef.Alias), sub+".edata")
			case "__typename":
				edgePairs = append(edgePairs, quoteLiteral(ef.Alias), quoteLiteral(typeName+"Edge")+"::text")
			}
		}
		pairs = append(pairs, quoteLiteral("edges"), agg(buildJSONObject(edgePairs)))
	}
	if wantTotal {
		pairs = append(pairs, quoteLiteral("totalCount"), countSQL+"::int")
	}
	if wantPageInfo {
		boolLit := func(b bool) string {
			if b {
				return "true"
			}
			return "false"
		}
		hasNext, hasPrev := "", ""
		if plan.predKeyset {
			// Exact in the paging direction (limit+1 fetch); the opposite
			// side falls back to what the supplied cursors imply.
			// count(*) counts post-OFFSET fetched rows (max limit+1), while
			// the shared trim placeholder holds offset+limit — add the offset
			// to the count so the comparison stays `fetched > limit`.
			countExpr := "count(*)"
			if plan.offset > 0 {
				countExpr = fmt.Sprintf("(count(*) + %d)", plan.offset)
			}
			switch {
			case plan.reversed:
				hasNext = boolLit(plan.hasBefore)
				hasPrev = fmt.Sprintf("(%s > %s)", countExpr, trimOrParam(&trimParam, s, plan.offset+plan.limit))
			case plan.limit >= 0:
				hasNext = fmt.Sprintf("(%s > %s)", countExpr, trimOrParam(&trimParam, s, plan.offset+plan.limit))
				hasPrev = boolLit(plan.hasAfter || plan.offset > 0)
			default:
				hasNext = boolLit(plan.hasBefore)
				hasPrev = boolLit(plan.hasAfter || plan.offset > 0)
			}
		} else {
			hasNext = fmt.Sprintf("(%s > %d + count(%s.__rn))", countSQL, plan.offset, sub)
			hasPrev = fmt.Sprintf("(%d > 0)", plan.offset)
		}
		var piPairs []string
		for _, pf := range pageInfoFields {
			switch pf.Name {
			case "hasNextPage":
				piPairs = append(piPairs, quoteLiteral(pf.Alias), hasNext)
			case "hasPreviousPage":
				piPairs = append(piPairs, quoteLiteral(pf.Alias), hasPrev)
			case "startCursor":
				piPairs = append(piPairs, quoteLiteral(pf.Alias),
					fmt.Sprintf("(jsonb_agg(%s%s)%s)->0", cursorExpr, aggOrder, aggFilter))
			case "endCursor":
				piPairs = append(piPairs, quoteLiteral(pf.Alias),
					fmt.Sprintf("(jsonb_agg(%s%s)%s)->-1", cursorExpr, aggOrder, aggFilter))
			case "__typename":
				piPairs = append(piPairs, quoteLiteral(pf.Alias), "'PageInfo'::text")
			}
		}
		pairs = append(pairs, quoteLiteral("pageInfo"), buildJSONObject(piPairs))
	}
	for _, sf := range fields {
		if sf.Name == "__typename" {
			pairs = append(pairs, quoteLiteral(sf.Alias), quoteLiteral(connType)+"::text")
		}
	}

	// GROUP BY () (the empty grouping set) guarantees exactly one result row
	// even when the page subquery is empty — a totalCount-only selection has
	// no aggregate calls, and without it an empty page would compile to zero
	// rows and surface as JSON null instead of a connection object.
	return fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) AS %s\nGROUP BY ()", buildJSONObject(pairs), indent(inner), sub), nil
}

// trimOrParam reuses the trim placeholder when aggFilter already created one,
// otherwise allocates a parameter for the limit comparison.
func trimOrParam(trim *string, s *stmt, limit int) string {
	if *trim == "" {
		*trim = s.param(limit)
	}
	return *trim
}

func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
