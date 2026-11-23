package compile

import (
	"errors"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
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
	return s.valueExpr(funcArgColumn(a), v) + "::" + typ
}

// funcArgColumn describes a function parameter as a column.
func funcArgColumn(a introspect.FuncArg) *introspect.Column {
	return &introspect.Column{
		Name:       a.Name,
		PGType:     a.PGType,
		TypeSchema: a.TypeSchema,
		IsArray:    strings.HasPrefix(a.PGType, "_"),
	}
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

// fnValueJSON renders an already-materialized function result value (a stable
// reference such as "__fn.v", not a call expression) as response JSON; sel
// is the result field's selection (range objects).
func (s *stmt) fnValueJSON(ref string, fn *introspect.Function, sel ast.SelectionSet) string {
	ret := fnReturnColumn(fn)
	if expr, ok := s.contribJSON(ref, ret, sel); ok {
		return expr
	}
	if ret.IsArray {
		if expr, ok := s.enumArrayJSON(ref, ret); ok {
			return expr
		}
		return arrayJSON(ref, ret)
	}
	return "to_json(" + s.exprJSON(ref, ret) + ")"
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
		return fmt.Sprintf("SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM %s AS __v",
			s.fnValueJSON("__v", fn, f.SelectionSet), call), nil
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
	return fmt.Sprintf("SELECT %s AS data FROM (SELECT %s AS v) AS __fn",
		s.fnValueJSON("__fn.v", fn, f.SelectionSet), call), nil
}

// functionMutation compiles a volatile function with the Relay-classic shape
// fn(input: FnInput!): FnPayload! { result clientMutationId }. The call sits
// in the FROM clause so it executes exactly once, whether or not `result` is
// selected.
func (s *stmt) functionMutation(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	fn := meta.Function
	input, _ := s.argValue(f, "input")
	inputObj, _ := input.(map[string]any)
	call, err := s.funcCall(fn, f.Name, "", inputObj)
	if err != nil {
		return "", err
	}

	payloadType := fieldTypeName(f)
	cmid, hasCmid := "", false
	if id, ok := inputObj["clientMutationId"].(string); ok {
		cmid, hasCmid = id, true
	}

	var resultField *ast.Field
	var pairs []string
	for _, sub := range s.expandFor(f.SelectionSet, payloadType) {
		if sub.Name == "__typename" {
			pairs = append(pairs, quoteLiteral(sub.Alias), quoteLiteral(payloadType)+"::text")
			continue
		}
		subMeta := s.fieldMeta(payloadType, sub.Name)
		if subMeta == nil {
			continue
		}
		switch subMeta.Kind {
		case schema.KindPayloadResult:
			resultField = sub
		case schema.KindClientMutationID:
			expr := "NULL::text"
			if hasCmid {
				expr = s.param(cmid) + "::text"
			}
			pairs = append(pairs, quoteLiteral(sub.Alias), expr)
		}
	}

	shape, comp := fnScalar, (*introspect.Composite)(nil)
	var resultSel ast.SelectionSet
	if resultField != nil {
		resultSel = resultField.SelectionSet
		shape, comp = s.fnResultShape(fn, resultField.SelectionSet)
	}
	// Pick the row source (always containing the call) and the result
	// expression per return shape.
	var from, resultExpr string
	switch {
	case fn.ReturnsSet:
		switch shape {
		case fnRows:
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSONAs(s.rowsTypeName(fn, resultField), resultField.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s.data), '[]'::json) AS data FROM (\n%s\n) AS %s) AS __agg",
				sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, alias, joinLaterals(laterals))), sub)
		case fnComposite:
			expr, err := s.fnCompositeJSON("__v.v", fn, comp, resultField.SelectionSet, 1)
			if err != nil {
				return "", err
			}
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM (SELECT %s AS v) AS __v) AS __agg", expr, call)
		default:
			from = fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) AS data FROM %s AS __v) AS __agg",
				s.fnValueJSON("__v", fn, resultSel), call)
		}
		resultExpr = "__agg.data"
	case fn.ReturnType == "void":
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		resultExpr = "to_json(true)"
	case shape == fnRows:
		alias := s.alias("t")
		jsonExpr, laterals, err := s.rowJSONAs(s.rowsTypeName(fn, resultField), resultField.SelectionSet, alias, 1)
		if err != nil {
			return "", err
		}
		from = fmt.Sprintf("%s AS %s%s", call, alias, joinLaterals(laterals))
		// NULL row -> result: null, payload row still present.
		resultExpr = fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", alias, jsonExpr)
	case shape == fnComposite:
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		resultExpr, err = s.fnCompositeJSON("__fn.v", fn, comp, resultField.SelectionSet, 1)
		if err != nil {
			return "", err
		}
	default:
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		resultExpr = s.fnValueJSON("__fn.v", fn, resultSel)
	}
	if resultField != nil {
		pairs = append(pairs, quoteLiteral(resultField.Alias), resultExpr)
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM %s", buildJSONObject(pairs), from), nil
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
	ret := fnReturnColumn(fn)
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
	if fn.ReturnsSet {
		elem := s.alias("v")
		value, ok := s.contribJSON(elem+".v", ret, f.SelectionSet)
		if !ok {
			value = s.exprJSON(elem+".v", ret)
		}
		return fmt.Sprintf("(SELECT coalesce(json_agg(%s), '[]'::json) FROM %s AS %s(v))",
			value, call, elem), nil
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
	if expr, ok := s.contribJSON(call, ret, f.SelectionSet); ok {
		return expr, nil
	}
	return s.exprJSON(call, ret), nil
}
