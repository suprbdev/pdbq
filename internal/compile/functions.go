package compile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/geotype"
	"github.com/suprbdev/pdbq/internal/schema"
)

// fnShape says how a function's result values are compiled.
type fnShape int

const (
	// fnScalar results are encoded with the scalar/enum/array renderers.
	fnScalar fnShape = iota
	// fnRows results are rows (a table's row type, or the record of OUT/TABLE
	// parameters) whose columns the selection reads from the call in FROM.
	fnRows
	// fnComposite results are composite values compiled like a composite
	// column.
	fnComposite
)

// fnResultShape classifies fn's result for a field whose selection is sel.
// Rows and composites need a selection set; without one (a JWT composite kept
// as a String for minting) the value is encoded whole.
func (s *stmt) fnResultShape(fn *introspect.Function, sel ast.SelectionSet) (fnShape, *introspect.Composite) {
	if len(sel) == 0 {
		return fnScalar, nil
	}
	if fn.ReturnType == "record" && len(fn.ReturnColumns) > 0 {
		return fnRows, nil
	}
	if s.c.Built.Catalog.Table(fn.ReturnTypeSchema, fn.ReturnType) != nil {
		return fnRows, nil
	}
	if comp := s.c.Built.Catalog.Composite(fn.ReturnTypeSchema, strings.TrimPrefix(fn.ReturnType, "_")); comp != nil {
		return fnComposite, comp
	}
	return fnScalar, nil
}

// fieldTypeName is the named (list and non-null unwrapped) GraphQL type of f.
func fieldTypeName(f *ast.Field) string {
	if f.Definition == nil {
		return ""
	}
	return f.Definition.Type.Name()
}

// rowsTypeName is the object type a fnRows result's selection is compiled
// against: the field's own type, falling back to the returned table's.
func (s *stmt) rowsTypeName(fn *introspect.Function, f *ast.Field) string {
	if tn := fieldTypeName(f); tn != "" {
		return tn
	}
	return s.c.Built.TypeForTable[fn.ReturnTypeSchema+"."+fn.ReturnType]
}

// fieldArgs collects the arguments f passes, keyed by argument name. An
// argument bound to a variable the request did not supply counts as omitted.
func (s *stmt) fieldArgs(f *ast.Field) (map[string]any, error) {
	out := map[string]any{}
	for _, a := range f.Arguments {
		if a.Value.Kind == ast.Variable {
			if _, ok := s.req.Vars[a.Value.Raw]; !ok {
				continue
			}
		}
		v, err := a.Value.Value(s.req.Vars)
		if err != nil {
			return nil, err
		}
		out[a.Name] = v
	}
	return out, nil
}

// funcCall renders a call of fn via introspect.Function.CallSQL. row, when
// non-empty, is passed for the first parameter (a computed column's row).
// Every other parameter takes the value of its GraphQL argument in args: an
// absent one is left out so its DEFAULT applies, an explicit null passes a
// typed NULL. Leaving out a parameter without a default is a user error.
func (s *stmt) funcCall(fn *introspect.Function, field, row string, args map[string]any) (string, error) {
	first := true
	call, err := fn.CallSQL(func(a introspect.FuncArg) (string, bool, error) {
		if first && row != "" {
			first = false
			return row, true, nil
		}
		first = false
		v, ok := args[inflect.LowerCamel(a.Name)]
		if !ok {
			return "", false, nil
		}
		return s.funcArgExpr(a, v), true, nil
	})
	var missing *introspect.MissingArgsError
	if errors.As(err, &missing) {
		names := make([]string, len(missing.Args))
		for i, n := range missing.Args {
			names[i] = inflect.LowerCamel(n)
		}
		return "", errUser("%s: missing required argument(s): %s", field, strings.Join(names, ", "))
	}
	return call, err
}

// funcArgExpr renders one argument value cast to the parameter's type. The
// value takes the same path as a column input value (valueExpr): enum value
// names map to labels, composites populate from a jsonb parameter, PostGIS
// values are wrapped in their constructor, json is passed as text.
func (s *stmt) funcArgExpr(a introspect.FuncArg, v any) string {
	typ := quoteIdent(a.TypeSchema) + "." + quoteIdent(a.PGType)
	if v == nil {
		return "NULL::" + typ
	}
	col := s.funcArgColumn(a)
	if row := s.rowArgComposite(col); row != nil {
		return s.compositeParam(row, col, v) + "::" + typ
	}
	return s.valueExpr(col, v) + "::" + typ
}

// rowArgComposite views the table whose row type (or array of it) col
// holds as a composite whose attributes are the table's columns, when the
// schema has a row input type for it (schema.Built.RowInputForPG); nil
// otherwise.
func (s *stmt) rowArgComposite(col *introspect.Column) *introspect.Composite {
	name := strings.TrimPrefix(col.PGType, "_")
	if _, ok := s.c.Built.RowInputForPG[col.TypeSchema+"."+name]; !ok {
		return nil
	}
	t := s.c.Built.Catalog.Table(col.TypeSchema, name)
	if t == nil {
		return nil
	}
	return &introspect.Composite{Schema: t.Schema, Name: t.Name, Fields: t.Columns}
}

// funcArgColumn describes a function parameter as a column, a domain
// resolved to its base type (schema.FuncArgColumn).
func (s *stmt) funcArgColumn(a introspect.FuncArg) *introspect.Column {
	return schema.FuncArgColumn(s.c.Built.Catalog, a)
}

// fnReturnColumn describes a function's return type as a column, so the
// scalar/array JSON renderers can apply the same representation fixes they
// apply to real columns (enum value names, BigInt-as-string, bytea base64).
// A domain resolves to its base type (schema.FuncReturnColumn).
func (s *stmt) fnReturnColumn(fn *introspect.Function) *introspect.Column {
	return schema.FuncReturnColumn(s.c.Built.Catalog, fn)
}

// fnTypedGeometry reports whether the non-array geometry or geography result
// of fn is declared on result field f as a geometry object type
// (typed_geometry) rather than the GeoJSON scalar.
func (s *stmt) fnTypedGeometry(fn *introspect.Function, f *ast.Field) (geotype.Type, bool) {
	ret := s.fnReturnColumn(fn)
	if f == nil || f.Definition == nil || ret.IsArray || !postgis.IsSpatial(ret.PGType) {
		return geotype.Type{}, false
	}
	return geotype.Parse(f.Definition.Type.Name())
}

// fnValueJSON renders an already-materialized function result value (a stable
// reference such as "__fn.v", not a call expression) as response JSON; f is
// the result field, whose selection set shapes range and geometry objects.
func (s *stmt) fnValueJSON(ref string, fn *introspect.Function, f *ast.Field) (string, error) {
	var sel ast.SelectionSet
	if f != nil {
		sel = f.SelectionSet
	}
	if gt, ok := s.fnTypedGeometry(fn, f); ok {
		return s.typedGeometrySQL(ref, gt, sel, 1)
	}
	ret := s.fnReturnColumn(fn)
	if expr, ok := s.contribJSON(ref, ret, sel); ok {
		return expr, nil
	}
	if ret.IsArray {
		if expr, ok := s.enumArrayJSON(ref, ret); ok {
			return expr, nil
		}
		return arrayJSON(ref, ret), nil
	}
	return "to_json(" + s.exprJSON(ref, ret) + ")", nil
}

// fnCompositeJSON compiles sel over an already-materialized composite (or
// composite array) result value ref.
func (s *stmt) fnCompositeJSON(ref string, fn *introspect.Function, comp *introspect.Composite, sel ast.SelectionSet, depth int) (string, error) {
	return s.compositeJSON(ref, comp, strings.HasPrefix(fn.ReturnType, "_") && !fn.ReturnsSet, sel, depth)
}

// function compiles a PostgreSQL function field. Scalar returns become
// to_json(fn(...)); row returns (tables, OUT/TABLE records) compile like
// list queries with the function as the row source; composite returns
// compile like composite columns.
func (s *stmt) function(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	fn := meta.Function
	if fn.Volatility == introspect.VolatilityVolatile {
		return s.functionMutation(meta, f)
	}
	args, err := s.fieldArgs(f)
	if err != nil {
		return "", err
	}
	call, err := s.funcCall(fn, f.Name, "", args)
	if err != nil {
		return "", err
	}
	shape, comp := s.fnResultShape(fn, f.SelectionSet)

	if fn.ReturnsSet {
		switch shape {
		case fnRows:
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSONAs(s.rowsTypeName(fn, f), f.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			return fmt.Sprintf("SELECT coalesce(json_agg(%s.data), '[]'::json) AS data\nFROM (\n%s\n) AS %s",
				sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, alias, joinLaterals(laterals))), sub), nil
		case fnComposite:
			expr, err := s.fnCompositeJSON("__v.v", fn, comp, f.SelectionSet, 1)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM (SELECT %s AS v) AS __v", expr, call), nil
		}
		value, err := s.fnValueJSON("__v", fn, f)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM %s AS __v", value, call), nil
	}
	switch shape {
	case fnRows:
		alias := s.alias("t")
		jsonExpr, laterals, err := s.rowJSONAs(s.rowsTypeName(fn, f), f.SelectionSet, alias, 1)
		if err != nil {
			return "", err
		}
		// A strict function returning NULL yields one all-null row; surface
		// that as JSON null rather than an object of null fields.
		return fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE NOT (%s IS NULL)\nLIMIT 1",
			jsonExpr, call, alias, joinLaterals(laterals), alias), nil
	case fnComposite:
		expr, err := s.fnCompositeJSON("__fn.v", fn, comp, f.SelectionSet, 1)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("SELECT %s AS data FROM (SELECT %s AS v) AS __fn", expr, call), nil
	}
	if fn.ReturnType == "void" {
		return fmt.Sprintf("SELECT to_json(true) AS data FROM (SELECT %s) AS __v", call), nil
	}
	// Materialize the call first: fnValueJSON may subscript the value (enum
	// arrays), and subscripting the call directly would re-evaluate it per
	// element.
	value, err := s.fnValueJSON("__fn.v", fn, f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT %s AS data FROM (SELECT %s AS v) AS __fn", value, call), nil
}

// functionMutation compiles a volatile function with PostGraphile's payload
// shape fn(input: FnInput!): FnPayload! { <result> clientMutationId query
// ... }. The call sits in the FROM clause so it executes exactly once,
// whatever the payload selects. A function returning one table row is the
// payload row: its relations and edge read the returned row.
//
// A statement reads the snapshot taken when it started, which does not
// include what the function writes. When the payload reads tables (query,
// the returned row's relations, computed columns), the call runs alone and
// its Then statement reads the payload in the same transaction, after it
// (see functionPayloadThen). Otherwise the payload compiles into the call's
// statement.
func (s *stmt) functionMutation(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	fn := meta.Function
	input, _ := s.argValue(f, "input")
	inputObj, _ := input.(map[string]any)
	call, err := s.funcCall(fn, f.Name, "", inputObj)
	if err != nil {
		return "", err
	}
	payloadType := fieldTypeName(f)
	fields := s.expandFor(f.SelectionSet, payloadType)
	table := s.c.Built.Catalog.Table(fn.ReturnTypeSchema, fn.ReturnType)
	if !s.payloadReadsTables(fn, table, payloadType, fields) {
		return s.functionPayload(fn, call, f, payloadType, fields)
	}

	then := &stmt{c: s.c, req: s.req, maxDepth: s.maxDepth, maxPageSize: s.maxPageSize, args: []any{nil}}
	var first, second string
	if table != nil {
		alias := s.alias("t")
		first = fmt.Sprintf("SELECT coalesce(json_agg(%s::text), '[]'::json) AS data\nFROM %s AS %s", alias, call, alias)
		second, err = then.functionRowsPayload(fn, table, f, payloadType, fields)
	} else {
		var results []*ast.Field
		for _, sub := range fields {
			if m := s.fieldMeta(payloadType, sub.Name); m != nil && m.Kind == schema.KindPayloadResult {
				results = append(results, sub)
			}
		}
		if first, err = s.functionPayload(fn, call, f, payloadType, results); err == nil {
			second, err = then.functionResultsPayload(f, payloadType, fields)
		}
	}
	if err != nil {
		return "", err
	}
	if err := then.err(); err != nil {
		return "", err
	}
	if second, err = then.resolveRanks(second); err != nil {
		return "", err
	}
	s.then = &Statement{SQL: second, Args: then.args[1:]}
	return first, nil
}

// payloadReadsTables reports whether the payload fields of a custom mutation
// of fn read tables: a query field other than __typename, a forward
// relation of the returned row, or a selection of the returned table row
// (result or edge node) reaching beyond its own columns.
func (s *stmt) payloadReadsTables(fn *introspect.Function, table *introspect.Table, payloadType string, fields []*ast.Field) bool {
	for _, sub := range fields {
		m := s.fieldMeta(payloadType, sub.Name)
		if m == nil {
			continue
		}
		switch m.Kind {
		case schema.KindRelationForward:
			return true
		case schema.KindPayloadQuery:
			for _, qf := range s.expandFor(sub.SelectionSet, "Query") {
				if qf.Name != "__typename" {
					return true
				}
			}
		case schema.KindPayloadResult:
			if table != nil && !s.columnsOnly(s.rowsTypeName(fn, sub), sub.SelectionSet) {
				return true
			}
		case schema.KindPayloadEdge:
			typeName := s.c.Built.TypeForTable[table.Schema+"."+table.Name]
			for _, ef := range s.expandFor(sub.SelectionSet, typeName+"Edge") {
				if ef.Name == "node" && !s.columnsOnly(typeName, ef.SelectionSet) {
					return true
				}
			}
		}
	}
	return false
}

// columnsOnly reports whether sel, a selection on the row type typeName,
// reads nothing but the row's own column values.
func (s *stmt) columnsOnly(typeName string, sel ast.SelectionSet) bool {
	for _, f := range s.expandFor(sel, typeName) {
		if f.Name == "__typename" {
			continue
		}
		m := s.fieldMeta(typeName, f.Name)
		if m == nil {
			return false
		}
		switch m.Kind {
		case schema.KindColumn, schema.KindNodeID, schema.KindFullTextRank, schema.KindSpatialDerived, schema.KindScore:
		default:
			return false
		}
	}
	return true
}

// returnedRows renders, as a row source under alias, the table rows a
// function returned, from the JSON array of their text forms bound to $1 of
// a Then statement. The text form round-trips every column type exactly.
// With ordinal, the element alias it returns also carries n, the row's
// position in the array.
func (s *stmt) returnedRows(t *introspect.Table, alias string, ordinal bool) (string, string) {
	r := s.alias("r")
	elems := fmt.Sprintf("json_array_elements_text($1::json) AS %s(v)", r)
	if ordinal {
		elems = fmt.Sprintf("json_array_elements_text($1::json) WITH ORDINALITY AS %s(v, n)", r)
	}
	return fmt.Sprintf("%s\nCROSS JOIN LATERAL (SELECT (%s.v::%s).*) AS %s", elems, r, tableRef(t), alias), r
}

// functionRowsPayload compiles the Then statement of a custom mutation of fn
// returning rows of t: the payload over the returned rows, which $1 holds
// as the JSON array of their text forms. The result field is built from the
// returned values, not re-read from t (so the role's RLS policies on t do
// not hide them); relations, computed columns and query read the database
// as the function left it.
func (s *stmt) functionRowsPayload(fn *introspect.Function, t *introspect.Table, f *ast.Field, payloadType string, fields []*ast.Field) (string, error) {
	var from, alias string
	if !fn.ReturnsSet {
		alias = s.alias("t")
		from, _ = s.returnedRows(t, alias, false)
	}
	var pairs, laterals []string
	for _, sub := range fields {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(payloadType)+"::text")
			continue
		}
		subMeta := s.fieldMeta(payloadType, sub.Name)
		if subMeta == nil {
			continue
		}
		switch subMeta.Kind {
		case schema.KindPayloadResult:
			if !fn.ReturnsSet {
				expr, lats, err := s.rowJSONAs(s.rowsTypeName(fn, sub), sub.SelectionSet, alias, 1)
				if err != nil {
					return "", err
				}
				pairs = append(pairs, key, nullRow(alias, expr))
				laterals = append(laterals, lats...)
				continue
			}
			rowAlias := s.alias("t")
			jsonExpr, lats, err := s.rowJSONAs(s.rowsTypeName(fn, sub), sub.SelectionSet, rowAlias, 1)
			if err != nil {
				return "", err
			}
			rows, r := s.returnedRows(t, rowAlias, true)
			agg := s.alias("s")
			pairs = append(pairs, key, fmt.Sprintf("(SELECT coalesce(json_agg(%s.data ORDER BY %s.n), '[]'::json) FROM (\n%s\n) AS %s)",
				agg, agg, indent(fmt.Sprintf("SELECT %s AS data, %s.n\nFROM %s%s", jsonExpr, r, rows, joinLaterals(lats))), agg))
		case schema.KindPayloadEdge:
			expr, lats, err := s.payloadEdge(t, sub, alias)
			if err != nil {
				return "", err
			}
			pairs = append(pairs, key, nullRow(alias, expr))
			laterals = append(laterals, lats...)
		default:
			expr, lats, ok, err := s.payloadCommon(subMeta, f, sub, alias)
			if err != nil {
				return "", err
			}
			if ok {
				pairs = append(pairs, key, expr)
				laterals = append(laterals, lats...)
			}
		}
	}
	sql := "SELECT " + buildJSONObject(pairs) + " AS data"
	if from != "" {
		sql += "\nFROM " + from + joinLaterals(laterals)
	}
	return sql, nil
}

// functionResultsPayload compiles the Then statement of a custom mutation
// whose function returns no table rows: $1 holds the result fields, keyed by
// response key, as the call's statement computed them, and the payload's
// query reads the database as the function left it.
func (s *stmt) functionResultsPayload(f *ast.Field, payloadType string, fields []*ast.Field) (string, error) {
	var pairs []string
	for _, sub := range fields {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(payloadType)+"::text")
			continue
		}
		subMeta := s.fieldMeta(payloadType, sub.Name)
		if subMeta == nil {
			continue
		}
		if subMeta.Kind == schema.KindPayloadResult {
			pairs = append(pairs, key, "($1::json -> "+key+")")
			continue
		}
		expr, _, ok, err := s.payloadCommon(subMeta, f, sub, "")
		if err != nil {
			return "", err
		}
		if ok {
			pairs = append(pairs, key, expr)
		}
	}
	return "SELECT " + buildJSONObject(pairs) + " AS data", nil
}

// functionPayload compiles the payload fields of a custom mutation of fn
// into the statement making call.
func (s *stmt) functionPayload(fn *introspect.Function, call string, f *ast.Field, payloadType string, fields []*ast.Field) (string, error) {
	var resultField *ast.Field
	for _, sub := range fields {
		if m := s.fieldMeta(payloadType, sub.Name); m != nil && m.Kind == schema.KindPayloadResult {
			resultField = sub
		}
	}
	shape, comp := fnScalar, (*introspect.Composite)(nil)
	var resultSel ast.SelectionSet
	if resultField != nil {
		resultSel = resultField.SelectionSet
		shape, comp = s.fnResultShape(fn, resultSel)
	}
	var row *introspect.Table
	if !fn.ReturnsSet {
		row = s.c.Built.Catalog.Table(fn.ReturnTypeSchema, fn.ReturnType)
	}
	// Pick the row source (always containing the call) and the result
	// expression per return shape.
	var from, resultExpr, alias string
	var laterals []string
	switch {
	case fn.ReturnsSet:
		switch shape {
		case fnRows:
			rowAlias := s.alias("t")
			jsonExpr, lats, err := s.rowJSONAs(s.rowsTypeName(fn, resultField), resultSel, rowAlias, 1)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s.data), '[]'::json) AS data FROM (\n%s\n) AS %s) AS __agg",
				sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, rowAlias, joinLaterals(lats))), sub)
		case fnComposite:
			expr, err := s.fnCompositeJSON("__v.v", fn, comp, resultSel, 1)
			if err != nil {
				return "", err
			}
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM (SELECT %s AS v) AS __v) AS __agg", expr, call)
		default:
			value, err := s.fnValueJSON("__v", fn, resultField)
			if err != nil {
				return "", err
			}
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM %s AS __v) AS __agg", value, call)
		}
		resultExpr = "__agg.data"
	case row != nil || shape == fnRows:
		alias = s.alias("t")
		from = call + " AS " + alias
		if resultField != nil {
			jsonExpr, lats, err := s.rowJSONAs(s.rowsTypeName(fn, resultField), resultSel, alias, 1)
			if err != nil {
				return "", err
			}
			laterals = append(laterals, lats...)
			resultExpr = nullRow(alias, jsonExpr)
		}
	case shape == fnComposite:
		var err error
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		resultExpr, err = s.fnCompositeJSON("__fn.v", fn, comp, resultSel, 1)
		if err != nil {
			return "", err
		}
	default:
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		if fn.ReturnType != "void" {
			var err error
			resultExpr, err = s.fnValueJSON("__fn.v", fn, resultField)
			if err != nil {
				return "", err
			}
		}
	}

	var pairs []string
	for _, sub := range fields {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(payloadType)+"::text")
			continue
		}
		subMeta := s.fieldMeta(payloadType, sub.Name)
		if subMeta == nil {
			continue
		}
		switch subMeta.Kind {
		case schema.KindPayloadResult:
			pairs = append(pairs, key, resultExpr)
		case schema.KindPayloadEdge:
			expr, lats, err := s.payloadEdge(row, sub, alias)
			if err != nil {
				return "", err
			}
			pairs = append(pairs, key, nullRow(alias, expr))
			laterals = append(laterals, lats...)
		default:
			expr, lats, ok, err := s.payloadCommon(subMeta, f, sub, alias)
			if err != nil {
				return "", err
			}
			if ok {
				pairs = append(pairs, key, expr)
				laterals = append(laterals, lats...)
			}
		}
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM %s%s", buildJSONObject(pairs), from, joinLaterals(laterals)), nil
}

// nullRow yields NULL instead of expr when the function row at alias is
// NULL (a function returning a NULL row yields one all-null row).
func nullRow(alias, expr string) string {
	return fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", alias, expr)
}

// computedJSON compiles a computed-column field: the row-type function called
// with an explicit ROW(...)::table cast, which works over any row source —
// a table scan, a function row source, or a mutation RETURNING CTE (whose
// whole-row variable is an anonymous record the function would reject).
// Set-returning functions aggregate into a scalar-transformed json array
// (SETOF <scalar>) or a fully-selected row array (SETOF <table|record>);
// row and composite results compile their selection in a scalar subquery.
func (s *stmt) computedJSON(meta *schema.FieldMeta, f *ast.Field, alias string, depth int) (string, error) {
	fn := meta.Function
	args, err := s.fieldArgs(f)
	if err != nil {
		return "", err
	}
	call, err := s.funcCall(fn, f.Name, rowParamSQL(meta.Table, alias), args)
	if err != nil {
		return "", err
	}
	ret := s.fnReturnColumn(fn)
	shape, comp := s.fnResultShape(fn, f.SelectionSet)
	switch shape {
	case fnRows:
		rowAlias := s.alias("t")
		jsonExpr, laterals, err := s.rowJSONAs(s.rowsTypeName(fn, f), f.SelectionSet, rowAlias, depth)
		if err != nil {
			return "", err
		}
		if !fn.ReturnsSet {
			return fmt.Sprintf("(SELECT %s FROM %s AS %s%s WHERE NOT (%s IS NULL) LIMIT 1)",
				jsonExpr, call, rowAlias, joinLaterals(laterals), rowAlias), nil
		}
		sub := s.alias("s")
		return fmt.Sprintf("(SELECT coalesce(json_agg(%s.data), '[]'::json) FROM (\n%s\n) AS %s)",
			sub,
			indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, rowAlias, joinLaterals(laterals))),
			sub), nil
	case fnComposite:
		sub := s.alias("c")
		expr, err := s.fnCompositeJSON(sub+".v", fn, comp, f.SelectionSet, depth)
		if err != nil {
			return "", err
		}
		if fn.ReturnsSet {
			return fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) FROM (SELECT %s AS v) AS %s)", expr, call, sub), nil
		}
		return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS v) AS %s)", expr, call, sub), nil
	}
	gt, typed := s.fnTypedGeometry(fn, f)
	if fn.ReturnsSet {
		elem := s.alias("v")
		value, ok := s.contribJSON(elem+".v", ret, f.SelectionSet)
		switch {
		case typed:
			if value, err = s.typedGeometrySQL(elem+".v", gt, f.SelectionSet, depth); err != nil {
				return "", err
			}
		case !ok:
			value = s.exprJSON(elem+".v", ret)
		}
		return fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) FROM %s AS %s(v))",
			value, call, elem), nil
	}
	if typed {
		return s.typedGeometrySQL(call, gt, f.SelectionSet, depth)
	}
	if expr, ok := s.contribJSON(call, ret, f.SelectionSet); ok {
		return expr, nil
	}
	if ret.IsArray {
		// A function call is not a stable reference: enumArrayJSON would
		// subscript it repeatedly, re-evaluating the call per element.
		if _, ok := s.enumValuesFor(ret); ok {
			sub := s.alias("a")
			mapped, _ := s.enumArrayJSON(sub+".v", ret)
			return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS v) AS %s)", mapped, call, sub), nil
		}
		return arrayJSON(call, ret), nil
	}
	return s.exprJSON(call, ret), nil
}
