package compile

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/schema"
)

// jsonBuildObjectChunk keeps each json_build_object call under PostgreSQL's
// 100-argument limit; wider selections are spliced together (see
// JSONObject).
const jsonBuildObjectChunk = 40 // pairs (80 args)

// rowJSON compiles a selection set over a table into a json expression plus
// the LEFT JOIN LATERAL clauses it needs against `alias`.
func (s *stmt) rowJSON(t *introspect.Table, sel ast.SelectionSet, alias string, depth int) (string, []string, error) {
	return s.rowJSONAs(s.c.Built.TypeForTable[t.Schema+"."+t.Name], sel, alias, depth)
}

// rowJSONAs is rowJSON for the row object type typeName, which may also be a
// function's generated record type: its fields are bound to columns of the
// row source like a table's.
func (s *stmt) rowJSONAs(typeName string, sel ast.SelectionSet, alias string, depth int) (string, []string, error) {
	if depth > s.maxDepth {
		return "", nil, errUser("selection depth exceeds limit (%d)", s.maxDepth)
	}
	fields := s.expandFor(sel, typeName)
	if len(fields) == 0 {
		// Validation guarantees a non-empty selection set, so this only
		// happens when @skip/@include or non-matching fragments removed every
		// field — per spec the result is an empty object, not an error.
		return "'{}'::json", nil, nil
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
				expr, err := s.spatialFieldSQL(alias, meta.Column, f, typeName, depth+1)
				if err != nil {
					return "", nil, err
				}
				pairs = append(pairs, key, expr)
				continue
			}
			if expr, ok := s.contribJSON(alias+"."+quoteIdent(meta.Column.Name), meta.Column, f.SelectionSet); ok {
				pairs = append(pairs, key, expr)
				continue
			}
			pairs = append(pairs, key, s.columnJSON(alias, meta.Column))
		case schema.KindFullTextRank:
			ref := alias + "." + quoteIdent(meta.Column.Name)
			pairs = append(pairs, key, s.rankMarker(rankRef{key: ref, render: ref}))
		case schema.KindScore:
			expr, err := s.scoreSQL(meta, f, alias)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindContribAccessor:
			expr, err := s.contribAccessorSQL(meta, alias)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindContribDerived:
			expr, err := s.contribDerivedSQL(meta, f, alias)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindSpatialDerived:
			suffix, _ := meta.Nested["suffix"].(string)
			expr, err := s.spatialDerivedSQL(alias, meta.Column, suffix)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
		case schema.KindNodeID:
			pairs = append(pairs, key, nodeIDSQL(typeName, meta.Table, alias))
		case schema.KindPlaceholder:
			pairs = append(pairs, key, "NULL::json")
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

// JSONObject renders a json object constructor over alternating key and
// value expressions (key1, value1, key2, value2, ...), keeping the keys in
// the order given. Response objects are built as json rather than jsonb:
// json_build_object only concatenates the text of its values, where
// jsonb_build_object converts each nested value into the binary jsonb form,
// and jsonb also reorders keys. CompileHook plugins that emit response
// objects use this rather than naming the SQL function.
//
// No pairs yield '{}'::json. More pairs than one call accepts are split
// into several json_build_object calls whose text is spliced into a single
// object, since json has no object concatenation operator.
func JSONObject(pairs ...string) string {
	return buildJSONObject(pairs)
}

func buildJSONObject(pairs []string) string {
	if len(pairs) == 0 {
		return "'{}'::json"
	}
	var chunks []string
	for i := 0; i < len(pairs); i += jsonBuildObjectChunk * 2 {
		end := min(i+jsonBuildObjectChunk*2, len(pairs))
		chunks = append(chunks, "json_build_object("+strings.Join(pairs[i:end], ", ")+")")
	}
	if len(chunks) == 1 {
		return chunks[0]
	}
	parts := make([]string, len(chunks))
	for i, c := range chunks {
		switch i {
		case 0:
			parts[i] = "left(" + c + "::text, -1)"
		case len(chunks) - 1:
			parts[i] = "substr(" + c + "::text, 2)"
		default:
			parts[i] = "substr(left(" + c + "::text, -1), 2)"
		}
	}
	return "(" + strings.Join(parts, " || ', ' || ") + ")::json"
}

// numberedCol names the i-th (0-based) inner scan column with prefix base:
// base itself, then base2, base3, ...
func numberedCol(base string, i int) string {
	if i == 0 {
		return base
	}
	return base + strconv.Itoa(i+1)
}

// columnJSON renders a column value for inclusion in the JSON response.
func (s *stmt) columnJSON(alias string, c *introspect.Column) string {
	ref := alias + "." + quoteIdent(c.Name)
	if c.IsArray {
		if f := postgis.FlavourOf(c.PGType); f.Spatial() {
			// to_json on a geometry[] would emit WKB hex strings; map each
			// element through ST_AsGeoJSON instead.
			return s.spatialArrayJSON(ref)
		}
		if expr, ok := s.enumArrayJSON(ref, c); ok {
			return expr
		}
		return arrayJSON(ref, c)
	}
	return s.exprJSON(ref, c)
}

// arrayJSON renders an array value as a JSON array. Arrays of json elements
// go through jsonb, like scalar json values in exprJSON.
func arrayJSON(ref string, c *introspect.Column) string {
	if strings.TrimPrefix(c.PGType, "_") == "json" {
		return "to_jsonb(" + ref + ")"
	}
	return "to_json(" + ref + ")"
}

// spatialArrayJSON renders a geometry/geography array as a JSON array of
// GeoJSON objects, preserving element order and NULL-ness of the array itself.
func (s *stmt) spatialArrayJSON(ref string) string {
	elem := s.alias("g")
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(%s ORDER BY %s.o), '[]'::json) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
		ref, postgis.OutputJSONSQL("("+ref+")["+elem+".o]"), elem, ref, elem)
}

// exprJSON renders a scalar value expression for inclusion in the JSON
// response, applying representation fixes: 64-bit and arbitrary-precision
// numbers are serialized as strings (BigInt/BigFloat scalars), bytea as
// base64, enum labels map back to their GraphQL enum value names, and json
// values are converted to jsonb, which drops duplicate keys.
func (s *stmt) exprJSON(ref string, c *introspect.Column) string {
	if expr, ok := s.enumCase(ref, c); ok {
		return expr
	}
	if postgis.FlavourOf(c.PGType).Spatial() {
		// PostGIS values render as WKB hex by default; the GeoJSON scalar
		// contract is a real JSON object.
		return postgis.OutputJSONSQL(ref)
	}
	switch c.PGType {
	case "int8", "numeric", "money":
		return "(" + ref + ")::text"
	case "bytea":
		return "encode(" + ref + ", 'base64')"
	case "json":
		return "(" + ref + ")::jsonb"
	default:
		return ref
	}
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
// json expression. ref is a raw composite (or composite-array) value; NULL
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
		return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(%s ORDER BY %s.o), '[]'::json) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
			ref, inner, elem, ref, elem), nil
	}
	fields := s.expandFor(sel, typeName)
	if len(fields) == 0 {
		// Every field removed by @skip/@include: empty object per spec.
		return fmt.Sprintf("CASE WHEN (%s)::text IS NULL THEN NULL ELSE '{}'::json END", ref), nil
	}
	var pairs []string
	for _, f := range fields {
		key := quoteLiteral(f.Alias)
		if f.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(typeName)+"::text")
			continue
		}
		meta := s.fieldMeta(typeName, f.Name)
		if meta != nil && meta.Kind == schema.KindPlaceholder {
			pairs = append(pairs, key, "NULL::json")
			continue
		}
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
		} else if gt, ok := s.typedGeometry(typeName, f.Name); ok && postgis.IsSpatial(meta.Column.PGType) && !meta.Column.IsArray {
			var err error
			expr, err = s.typedGeometrySQL(attr, gt, f.SelectionSet, depth+1)
			if err != nil {
				return "", err
			}
		} else if cexpr, ok := s.contribJSON(attr, meta.Column, f.SelectionSet); ok {
			expr = cexpr
		} else if meta.Column.IsArray {
			if mapped, ok := s.enumArrayJSON(attr, meta.Column); ok {
				expr = mapped
			} else {
				expr = arrayJSON(attr, meta.Column)
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
// names. to_json would emit the raw lowercase pg labels, which do not match
// the enum values the schema declares for the field ([Mood!]), so each element
// goes through the same CASE mapping the scalar path uses. Element order and
// the NULL-ness of the array itself are preserved.
func (s *stmt) enumArrayJSON(ref string, c *introspect.Column) (string, bool) {
	if _, ok := s.enumValuesFor(c); !ok {
		return "", false
	}
	elem := s.alias("m")
	mapped, _ := s.enumCase("("+ref+")["+elem+".o]", c)
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(%s ORDER BY %s.o), '[]'::json) FROM (SELECT generate_subscripts(%s, 1) AS o) AS %s) END",
		ref, mapped, elem, ref, elem), true
}

func anyMap(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// fkJoinSQL renders the join predicate of fk (see
// [introspect.ForeignKey.JoinPairs]) between the referencing row at
// childAlias and the referenced row at parentAlias. refFirst puts the
// referenced side on the left of each operator; both operators (= and &&)
// are commutative, so it only fixes the rendered text. Equality on extension
// types (citext, hstore, ltree) uses their schema-qualified operator.
func (s *stmt) fkJoinSQL(fk *introspect.ForeignKey, childAlias, parentAlias string, refFirst bool) string {
	pairs := fk.JoinPairs()
	ref := s.c.Built.Catalog.Table(fk.RefSchema, fk.RefTable)
	conds := make([]string, len(pairs))
	for i, p := range pairs {
		child := childAlias + "." + quoteIdent(p.Column)
		parent := parentAlias + "." + quoteIdent(p.RefColumn)
		op := string(p.Op)
		if p.Op == introspect.JoinEqual && ref != nil {
			op = s.contrib(ref.Column(p.RefColumn)).Operator(op)
		}
		if refFirst {
			conds[i] = parent + " " + op + " " + child
		} else {
			conds[i] = child + " " + op + " " + parent
		}
	}
	return strings.Join(conds, " AND ")
}

// forwardRelation compiles a many-to-one field: one lateral join fetching the
// referenced row's JSON. A foreign key that may reference several rows (a
// PERIOD key) compiles to a connection over them instead.
func (s *stmt) forwardRelation(meta *schema.FieldMeta, f *ast.Field, parentAlias string, depth int) (string, string, error) {
	ref := meta.Table
	if !meta.FK.ReferencesOne() {
		return s.relationConnection(ref, f, s.fkJoinSQL(meta.FK, parentAlias, "%%ALIAS%%", true), depth)
	}
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSON(ref, f.SelectionSet, alias, depth)
	if err != nil {
		return "", "", err
	}
	latAlias := s.alias("l")
	lat := fmt.Sprintf("LEFT JOIN LATERAL (\n%s\n) AS %s ON true",
		indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE %s\nLIMIT 1",
			jsonExpr, s.sourceRef(ref, alias), alias, joinLaterals(laterals), s.fkJoinSQL(meta.FK, parentAlias, alias, true))),
		latAlias)
	return latAlias + ".data", lat, nil
}

// backwardRelation compiles a one-to-many field as a nested connection: a
// lateral join whose subquery aggregates the child connection JSON.
func (s *stmt) backwardRelation(meta *schema.FieldMeta, f *ast.Field, parentAlias string, depth int) (string, string, error) {
	return s.relationConnection(meta.Table, f, s.fkJoinSQL(meta.FK, "%%ALIAS%%", parentAlias, false), depth)
}

// relationConnection compiles a to-many relation over t as a lateral join
// whose subquery aggregates the connection JSON; cond correlates t's rows
// (the %%ALIAS%% placeholder) to the current row.
func (s *stmt) relationConnection(t *introspect.Table, f *ast.Field, cond string, depth int) (string, string, error) {
	body, err := s.connectionJSON(t, f, cond, depth)
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
//
// When the rows embed relations (and no distinctOn applies), the page is
// cut in a subquery reading only the columns the selection needs, and the
// relation laterals join onto that page, so they run once per returned row
// rather than for every row the sort reads. LIMIT is a literal, which lets
// PostgreSQL reuse a generic plan for the prepared statement.
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
	separate, err := s.windowConnection(t, f, fields, &plan, extraCond, depth)
	if err != nil {
		return "", err
	}
	var nodesFields, edgeNodeFields, aggFields []*ast.Field
	rowCols := map[*ast.Field]string{}
	edgeSubs := map[*ast.Field][]*ast.Field{}
	pageInfoSubs := map[*ast.Field][]*ast.Field{}
	wantTotal, wantEdges, wantPageInfo := false, false, false
	for _, sub := range fields {
		if _, ok := separate[sub]; ok {
			continue
		}
		switch sub.Name {
		case "nodes":
			rowCols[sub] = numberedCol("ndata", len(nodesFields))
			nodesFields = append(nodesFields, sub)
		case "edges":
			wantEdges = true
			edgeSubs[sub] = s.expandFor(sub.SelectionSet, typeName+"Edge")
			for _, ef := range edgeSubs[sub] {
				if ef.Name == "node" {
					rowCols[ef] = numberedCol("edata", len(edgeNodeFields))
					edgeNodeFields = append(edgeNodeFields, ef)
				}
			}
		case "totalCount":
			wantTotal = true
		case "pageInfo":
			wantPageInfo = true
			pageInfoSubs[sub] = s.expandFor(sub.SelectionSet, "PageInfo")
		default:
			if m := s.fieldMeta(connType, sub.Name); m != nil && (m.Kind == schema.KindAggregates || m.Kind == schema.KindGroupedAggregates) {
				aggFields = append(aggFields, sub)
			}
		}
	}
	if len(aggFields) > 0 && len(dcols) > 0 {
		return "", errUser("aggregates cannot be combined with distinctOn")
	}

	// ---- inner paginated scan ----
	alias := s.alias("t")
	s.scoreList(alias, f)
	selectCols := []string{}
	var innerLaterals []string
	for _, rf := range append(append([]*ast.Field{}, nodesFields...), edgeNodeFields...) {
		jsonExpr, laterals, err := s.rowJSON(t, rf.SelectionSet, alias, depth)
		if err != nil {
			return "", err
		}
		selectCols = append(selectCols, jsonExpr+" AS "+rowCols[rf])
		innerLaterals = append(innerLaterals, laterals...)
	}
	if plan.emitKeyset && (wantEdges || wantPageInfo) {
		selectCols = append(selectCols, nodeIDSQL(typeName, t, alias)+" AS __cursor")
	}
	terms := reorderForDistinct(s.orderTerms(t, f, alias), dcols)
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
			limit = "\nLIMIT " + strconv.Itoa(plan.limit+1)
		} else {
			limit = "\nLIMIT " + strconv.Itoa(plan.limit)
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
	var pageCols []string
	if len(innerLaterals) > 0 && distinct == "" {
		pageCols = s.pageColumns(t, alias, strings.Join(selectCols, ", ")+joinLaterals(innerLaterals))
	}
	var inner string
	if len(pageCols) > 0 {
		refs := make([]string, 0, len(pageCols)+1)
		for _, c := range pageCols {
			refs = append(refs, alias+"."+quoteIdent(c))
		}
		page := fmt.Sprintf("SELECT %s\nFROM %s AS %s%s%s%s%s",
			strings.Join(append(refs, rn), ", "), s.sourceRef(t, alias), alias, where, order, limit, pageOffset)
		selectCols = append(selectCols, alias+"."+rnName)
		inner = fmt.Sprintf("SELECT %s\nFROM (\n%s\n) AS %s%s",
			strings.Join(selectCols, ", "), indent(page), alias, joinLaterals(innerLaterals))
	} else {
		selectCols = append(selectCols, rn)
		inner = fmt.Sprintf("SELECT %s%s\nFROM %s AS %s%s%s%s%s%s",
			distinct, strings.Join(selectCols, ", "), s.sourceRef(t, alias), alias,
			joinLaterals(innerLaterals), where, order, limit, pageOffset)
	}
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
	if plan.predKeyset && plan.limit >= 0 && (len(nodesFields) > 0 || wantEdges || wantPageInfo) {
		// __rn numbers rows in the ordered scan BEFORE OFFSET trims, so rows
		// surviving `LIMIT limit+1 OFFSET offset` carry __rn in
		// [offset+1, offset+limit+1] — the trim threshold is offset+limit.
		trimParam = s.param(plan.offset + plan.limit)
		aggFilter = fmt.Sprintf(" FILTER (WHERE %s.__rn <= %s)", sub, trimParam)
	}
	agg := func(expr string) string {
		return fmt.Sprintf("coalesce(json_agg(%s%s)%s, '[]'::json)", expr, aggOrder, aggFilter)
	}
	cursorOffset := 0
	if len(dcols) > 0 {
		cursorOffset = plan.offset
	}
	cursorExpr := encodeCursorSQL(sub+".__rn", cursorOffset)
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
				strings.Join(refs, ", "), s.sourceRef(t, countAlias), countAlias, countWhere, s.alias("c"))
		} else {
			countSQL = fmt.Sprintf("(SELECT count(*) FROM %s AS %s%s)", s.sourceRef(t, countAlias), countAlias, countWhere)
		}
	}

	hasNext, hasPrev := "", ""
	if wantPageInfo {
		boolLit := func(b bool) string {
			if b {
				return "true"
			}
			return "false"
		}
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
	}

	var pairs []string
	for _, cf := range fields {
		key := quoteLiteral(cf.Alias)
		if expr, ok := separate[cf]; ok {
			pairs = append(pairs, key, expr)
			continue
		}
		switch cf.Name {
		case "nodes":
			pairs = append(pairs, key, agg(sub+"."+rowCols[cf]))
		case "edges":
			var edgePairs []string
			for _, ef := range edgeSubs[cf] {
				switch ef.Name {
				case "cursor":
					edgePairs = append(edgePairs, quoteLiteral(ef.Alias), cursorExpr)
				case "node":
					edgePairs = append(edgePairs, quoteLiteral(ef.Alias), sub+"."+rowCols[ef])
				case "__typename":
					edgePairs = append(edgePairs, quoteLiteral(ef.Alias), quoteLiteral(typeName+"Edge")+"::text")
				}
			}
			pairs = append(pairs, key, agg(buildJSONObject(edgePairs)))
		case "totalCount":
			pairs = append(pairs, key, countSQL+"::int")
		case "pageInfo":
			var piPairs []string
			for _, pf := range pageInfoSubs[cf] {
				switch pf.Name {
				case "hasNextPage":
					piPairs = append(piPairs, quoteLiteral(pf.Alias), hasNext)
				case "hasPreviousPage":
					piPairs = append(piPairs, quoteLiteral(pf.Alias), hasPrev)
				case "startCursor":
					piPairs = append(piPairs, quoteLiteral(pf.Alias),
						fmt.Sprintf("(json_agg(%s%s)%s)->0", cursorExpr, aggOrder, aggFilter))
				case "endCursor":
					piPairs = append(piPairs, quoteLiteral(pf.Alias),
						fmt.Sprintf("(json_agg(%s%s)%s)->-1", cursorExpr, aggOrder, aggFilter))
				case "__typename":
					piPairs = append(piPairs, quoteLiteral(pf.Alias), "'PageInfo'::text")
				}
			}
			pairs = append(pairs, key, buildJSONObject(piPairs))
		case "__typename":
			pairs = append(pairs, key, quoteLiteral(connType)+"::text")
		default:
			if !slices.Contains(aggFields, cf) {
				continue
			}
			expr, err := s.connectionAggregate(t, f, cf, s.fieldMeta(connType, cf.Name), extraCond, depth+1)
			if err != nil {
				return "", err
			}
			pairs = append(pairs, key, expr)
		}
	}

	// GROUP BY () (the empty grouping set) guarantees exactly one result row
	// even when the page subquery is empty — a totalCount-only selection has
	// no aggregate calls, and without it an empty page would compile to zero
	// rows and surface as JSON null instead of a connection object.
	return fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) AS %s\nGROUP BY ()", buildJSONObject(pairs), indent(inner), sub), nil
}

// pageColumns returns, in table order, the columns of t that sql reads
// through alias, counting the column behind each pending rank marker in sql.
func (s *stmt) pageColumns(t *introspect.Table, alias, sql string) []string {
	for i, r := range s.rankRefs {
		if strings.Contains(sql, rankMarkerText(i)) {
			sql += "\n" + r.render
		}
	}
	return referencedColumns(t, sql, []string{alias})
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
