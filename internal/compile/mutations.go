package compile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// mutation compiles create/update/delete into a single statement: the DML
// runs in a `WITH __mut AS (... RETURNING *)` CTE and the payload selection
// is compiled against __mut like a regular table, so relations still work.
func (s *stmt) mutation(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	t := meta.Table
	var cte string
	var err error
	switch meta.Kind {
	case schema.KindCreate:
		cte, err = s.insertCTE(t, f)
	case schema.KindUpdate:
		cte, err = s.updateCTE(t, meta, f)
	case schema.KindDelete:
		cte, err = s.deleteCTE(t, meta, f)
	case schema.KindUpsert:
		cte, err = s.upsertCTE(t, meta, f)
	case schema.KindCreateMany:
		cte, err = s.createManyCTE(t, f)
	case schema.KindUpdateMany:
		cte, err = s.updateManyCTE(t, f)
	case schema.KindDeleteMany:
		cte, err = s.deleteManyCTE(t, f)
	}
	if err != nil {
		return "", err
	}
	bulk := meta.Kind == schema.KindCreateMany || meta.Kind == schema.KindUpdateMany || meta.Kind == schema.KindDeleteMany
	var payload string
	if bulk {
		payload, err = s.payloadSelectMany(t, f)
	} else {
		payload, err = s.payloadSelect(t, f)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("WITH __mut AS (\n%s\n)\n%s", indent(cte), payload), nil
}

// clientMutationID extracts the clientMutationId sent by the client, if any.
// For create/update it's nested inside the input/patch object; for delete
// (which has no input object) it's a plain top-level argument.
func (s *stmt) clientMutationID(f *ast.Field) (string, bool) {
	for _, argName := range [...]string{"input", "patch"} {
		if v, ok := s.argValue(f, argName); ok {
			if obj, ok := v.(map[string]any); ok {
				if id, ok := obj["clientMutationId"].(string); ok {
					return id, true
				}
			}
		}
	}
	if v, ok := s.argValue(f, "clientMutationId"); ok {
		if id, ok := v.(string); ok {
			return id, true
		}
	}
	return "", false
}

// payloadSelect compiles the payload selection (the row field, __typename,
// and clientMutationId passthrough) against the __mut CTE.
func (s *stmt) payloadSelect(t *introspect.Table, f *ast.Field) (string, error) {
	payloadType := ""
	if f.Definition != nil {
		payloadType = f.Definition.Type.Name()
	}
	cmid, hasCmid := s.clientMutationID(f)
	var rowField *ast.Field
	var pairs []string
	for _, sub := range s.expandFor(f.SelectionSet, payloadType) {
		if sub.Name == "__typename" {
			pairs = append(pairs, quoteLiteral(sub.Alias), quoteLiteral(payloadType)+"::text")
			continue
		}
		meta := s.fieldMeta(payloadType, sub.Name)
		if meta == nil {
			continue
		}
		switch meta.Kind {
		case schema.KindPayloadRow:
			rowField = sub
			pairs = append(pairs, quoteLiteral(sub.Alias), "__row.data")
		case schema.KindClientMutationID:
			expr := "NULL::text"
			if hasCmid {
				expr = s.param(cmid) + "::text"
			}
			pairs = append(pairs, quoteLiteral(sub.Alias), expr)
		}
	}
	if rowField == nil {
		// Payload selected no row — still execute the DML CTE.
		return fmt.Sprintf("SELECT %s AS data FROM __mut", buildJSONObject(pairs)), nil
	}
	alias := s.alias("t")
	jsonExpr, laterals, err := s.rowJSONFromCTE(t, rowField.SelectionSet, alias)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM (\n%s\n) AS __row",
		buildJSONObject(pairs),
		indent(fmt.Sprintf("SELECT %s AS data\nFROM __mut AS %s%s", jsonExpr, alias, joinLaterals(laterals))),
	), nil
}

// payloadSelectMany compiles a bulk-mutation payload against the __mut CTE:
// the rows field aggregates every mutated row, affectedCount counts them.
// Aggregates without GROUP BY always yield one row, so a bulk statement never
// maps to "no row matched" even when nothing was affected.
func (s *stmt) payloadSelectMany(t *introspect.Table, f *ast.Field) (string, error) {
	payloadType := ""
	if f.Definition != nil {
		payloadType = f.Definition.Type.Name()
	}
	cmid, hasCmid := s.clientMutationID(f)
	var pairs []string
	for _, sub := range s.expandFor(f.SelectionSet, payloadType) {
		if sub.Name == "__typename" {
			pairs = append(pairs, quoteLiteral(sub.Alias), quoteLiteral(payloadType)+"::text")
			continue
		}
		meta := s.fieldMeta(payloadType, sub.Name)
		if meta == nil {
			continue
		}
		switch meta.Kind {
		case schema.KindPayloadRows:
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(t, sub.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub2 := s.alias("s")
			pairs = append(pairs, quoteLiteral(sub.Alias), fmt.Sprintf(
				"(SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) FROM (\n%s\n) AS %s)",
				sub2,
				indent(fmt.Sprintf("SELECT %s AS data\nFROM __mut AS %s%s", jsonExpr, alias, joinLaterals(laterals))),
				sub2))
		case schema.KindPayloadCount:
			pairs = append(pairs, quoteLiteral(sub.Alias), "(SELECT count(*) FROM __mut)")
		case schema.KindClientMutationID:
			expr := "NULL::text"
			if hasCmid {
				expr = s.param(cmid) + "::text"
			}
			pairs = append(pairs, quoteLiteral(sub.Alias), expr)
		}
	}
	return fmt.Sprintf("SELECT %s AS data", buildJSONObject(pairs)), nil
}

// createManyCTE compiles a multi-row INSERT. The column list is the union of
// columns provided across rows; rows missing a column insert DEFAULT there.
func (s *stmt) createManyCTE(t *introspect.Table, f *ast.Field) (string, error) {
	input, ok := s.argValue(f, "input")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing input", f.Name)
	}
	rows, ok := input.([]any)
	if !ok || len(rows) == 0 {
		return "", fmt.Errorf("compile: %s: input must be a non-empty list", f.Name)
	}
	inputType := argTypeName(f, "input")
	binding := s.c.Built.InputColumns[inputType]
	if binding == nil {
		return "", fmt.Errorf("compile: unknown input type %q", inputType)
	}
	var fieldNames []string
	for name := range binding {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)
	// Union of provided columns, in sorted field order.
	provided := map[string]bool{}
	objs := make([]map[string]any, len(rows))
	for i, r := range rows {
		obj, ok := r.(map[string]any)
		if !ok {
			return "", fmt.Errorf("compile: %s: input rows must be objects", f.Name)
		}
		objs[i] = obj
		for name := range obj {
			if binding[name] != "" {
				provided[name] = true
			}
		}
	}
	var useFields []string
	for _, name := range fieldNames {
		if provided[name] {
			useFields = append(useFields, name)
		}
	}
	if len(useFields) == 0 {
		return "", fmt.Errorf("compile: %s: input rows set no columns", f.Name)
	}
	cols := make([]string, len(useFields))
	for i, name := range useFields {
		cols[i] = quoteIdent(binding[name])
	}
	var valueRows []string
	for _, obj := range objs {
		exprs := make([]string, len(useFields))
		for i, name := range useFields {
			v, ok := obj[name]
			if !ok {
				exprs[i] = "DEFAULT"
				continue
			}
			col := t.Column(binding[name])
			if col == nil {
				return "", fmt.Errorf("compile: input field %q: column %q not found", name, binding[name])
			}
			exprs[i] = s.valueExpr(col, v)
		}
		valueRows = append(valueRows, "("+strings.Join(exprs, ", ")+")")
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s\nRETURNING *",
		tableRef(t), strings.Join(cols, ", "), strings.Join(valueRows, ",\n       ")), nil
}

func (s *stmt) updateManyCTE(t *introspect.Table, f *ast.Field) (string, error) {
	patchVal, ok := s.argValue(f, "patch")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing patch", f.Name)
	}
	patch, ok := patchVal.(map[string]any)
	if !ok || len(patch) == 0 {
		return "", fmt.Errorf("compile: %s: patch must set at least one column", f.Name)
	}
	cols, params, err := s.inputColumns(t, argTypeName(f, "patch"), patch)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("compile: %s: patch must set at least one column", f.Name)
	}
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = quoteIdent(c) + " = " + params[i]
	}
	cond, err := s.bulkFilterCond(t, f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s\nRETURNING *",
		tableRef(t), strings.Join(sets, ", "), cond), nil
}

func (s *stmt) deleteManyCTE(t *introspect.Table, f *ast.Field) (string, error) {
	cond, err := s.bulkFilterCond(t, f)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DELETE FROM %s\nWHERE %s\nRETURNING *", tableRef(t), cond), nil
}

// bulkFilterCond resolves the required filter argument of a bulk mutation to
// a WHERE condition over the bare table reference. An empty filter object
// deliberately matches every row — the argument itself is non-null so the
// caller must always write one.
func (s *stmt) bulkFilterCond(t *introspect.Table, f *ast.Field) (string, error) {
	fv, ok := s.argValue(f, "filter")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing filter", f.Name)
	}
	cond, err := s.filterCond(t, argTypeName(f, "filter"), fv, tableRef(t))
	if err != nil {
		return "", err
	}
	if cond == "" {
		cond = "TRUE"
	}
	return cond, nil
}

// rowJSONFromCTE is rowJSON but with the row source being the __mut CTE.
func (s *stmt) rowJSONFromCTE(t *introspect.Table, sel ast.SelectionSet, alias string) (string, []string, error) {
	return s.rowJSON(t, sel, alias, 1)
}

func (s *stmt) insertCTE(t *introspect.Table, f *ast.Field) (string, error) {
	input, ok := s.argValue(f, "input")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing input", f.Name)
	}
	vals, ok := input.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compile: %s: input must be an object", f.Name)
	}
	inputType := argTypeName(f, "input")
	cols, params, err := s.inputColumns(t, inputType, vals)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING *", tableRef(t)), nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES (%s)\nRETURNING *",
		tableRef(t), strings.Join(quoted, ", "), strings.Join(params, ", ")), nil
}

// inputColumns maps a GraphQL input object to (columns, placeholders) using
// the input type's field->column bindings, in schema field order for
// deterministic SQL.
func (s *stmt) inputColumns(t *introspect.Table, inputType string, vals map[string]any) ([]string, []string, error) {
	binding := s.c.Built.InputColumns[inputType]
	if binding == nil {
		return nil, nil, fmt.Errorf("compile: unknown input type %q", inputType)
	}
	var fieldNames []string
	for name := range vals {
		fieldNames = append(fieldNames, name)
	}
	// Deterministic order.
	for i := 1; i < len(fieldNames); i++ {
		for j := i; j > 0 && fieldNames[j] < fieldNames[j-1]; j-- {
			fieldNames[j], fieldNames[j-1] = fieldNames[j-1], fieldNames[j]
		}
	}
	var cols, params []string
	for _, name := range fieldNames {
		colName, ok := binding[name]
		if !ok {
			continue // plugin-owned field (e.g. nested input) — not a plain column
		}
		col := t.Column(colName)
		if col == nil {
			return nil, nil, fmt.Errorf("compile: input field %q: column %q not found", name, colName)
		}
		cols = append(cols, colName)
		params = append(params, s.valueExpr(col, vals[name]))
	}
	return cols, params, nil
}

// upsertCTE compiles INSERT ... ON CONFLICT (KeyColumns) DO UPDATE. Every
// provided input column except the conflict target is updated from EXCLUDED;
// when only target columns are provided, the target updates to itself so the
// statement still returns the existing row instead of DO NOTHING's zero rows.
func (s *stmt) upsertCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (string, error) {
	input, ok := s.argValue(f, "input")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing input", f.Name)
	}
	vals, ok := input.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compile: %s: input must be an object", f.Name)
	}
	inputType := argTypeName(f, "input")
	cols, params, err := s.inputColumns(t, inputType, vals)
	if err != nil {
		return "", err
	}
	target := map[string]bool{}
	for _, kc := range meta.KeyColumns {
		target[kc] = true
	}
	provided := map[string]bool{}
	for _, c := range cols {
		provided[c] = true
	}
	for _, kc := range meta.KeyColumns {
		if !provided[kc] {
			return "", fmt.Errorf("compile: %s: input must set conflict column %q", f.Name, kc)
		}
	}
	var sets []string
	for _, c := range cols {
		if !target[c] {
			sets = append(sets, quoteIdent(c)+" = EXCLUDED."+quoteIdent(c))
		}
	}
	if len(sets) == 0 {
		for _, kc := range meta.KeyColumns {
			sets = append(sets, quoteIdent(kc)+" = EXCLUDED."+quoteIdent(kc))
		}
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	targetCols := make([]string, len(meta.KeyColumns))
	for i, kc := range meta.KeyColumns {
		targetCols[i] = quoteIdent(kc)
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES (%s)\nON CONFLICT (%s) DO UPDATE\nSET %s\nRETURNING *",
		tableRef(t), strings.Join(quoted, ", "), strings.Join(params, ", "),
		strings.Join(targetCols, ", "), strings.Join(sets, ", ")), nil
}

func (s *stmt) updateCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (string, error) {
	patchVal, ok := s.argValue(f, "patch")
	if !ok {
		return "", fmt.Errorf("compile: %s: missing patch", f.Name)
	}
	patch, ok := patchVal.(map[string]any)
	if !ok || len(patch) == 0 {
		return "", fmt.Errorf("compile: %s: patch must set at least one column", f.Name)
	}
	patchType := argTypeName(f, "patch")
	cols, params, err := s.inputColumns(t, patchType, patch)
	if err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return "", fmt.Errorf("compile: %s: patch must set at least one column", f.Name)
	}
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = quoteIdent(c) + " = " + params[i]
	}
	conds, err := s.keyConds(t, meta.KeyColumns, f, tableRef(t))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s\nRETURNING *",
		tableRef(t), strings.Join(sets, ", "), strings.Join(conds, " AND ")), nil
}

func (s *stmt) deleteCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (string, error) {
	conds, err := s.keyConds(t, meta.KeyColumns, f, tableRef(t))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("DELETE FROM %s\nWHERE %s\nRETURNING *",
		tableRef(t), strings.Join(conds, " AND ")), nil
}

// function compiles a PostgreSQL function field. Scalar returns become
// to_jsonb(fn(...)); setof-table returns compile like list queries with the
// function as the row source.
func (s *stmt) function(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	fn := meta.Function
	if fn.Volatility == introspect.VolatilityVolatile {
		return s.functionMutation(meta, f)
	}
	fnRef := quoteIdent(fn.Schema) + "." + quoteIdent(fn.Name)
	var params []string
	for _, a := range fn.Args {
		argDef := f.Arguments.ForName(inflect.LowerCamel(a.Name))
		if argDef == nil {
			params = append(params, "NULL")
			continue
		}
		v, err := argDef.Value.Value(s.req.Vars)
		if err != nil {
			return "", err
		}
		params = append(params, s.param(v)+"::"+quoteIdent(a.PGType))
	}
	call := fnRef + "(" + strings.Join(params, ", ") + ")"

	// setof <table>: compile the selection like a table row source.
	if fn.ReturnsSet {
		if t := s.tableForTypeName(fn.ReturnType); t != nil && len(f.SelectionSet) > 0 {
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(t, f.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			return fmt.Sprintf("SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) AS data\nFROM (\n%s\n) AS %s",
				sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, alias, joinLaterals(laterals))), sub), nil
		}
		return fmt.Sprintf("SELECT coalesce(jsonb_agg(to_jsonb(__v)), '[]'::jsonb) AS data FROM %s AS __v", call), nil
	}
	if t := s.tableForTypeName(fn.ReturnType); t != nil && len(f.SelectionSet) > 0 {
		alias := s.alias("t")
		jsonExpr, laterals, err := s.rowJSON(t, f.SelectionSet, alias, 1)
		if err != nil {
			return "", err
		}
		// A strict function returning NULL yields one all-null row; surface
		// that as JSON null rather than an object of null fields.
		return fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s\nWHERE NOT (%s IS NULL)\nLIMIT 1",
			jsonExpr, call, alias, joinLaterals(laterals), alias), nil
	}
	if fn.ReturnType == "void" {
		return fmt.Sprintf("SELECT to_jsonb(true) AS data FROM (SELECT %s) AS __v", call), nil
	}
	return fmt.Sprintf("SELECT to_jsonb(%s) AS data", call), nil
}

// functionMutation compiles a volatile function with the Relay-classic shape
// fn(input: FnInput!): FnPayload! { result clientMutationId }. The call sits
// in the FROM clause so it executes exactly once, whether or not `result` is
// selected.
func (s *stmt) functionMutation(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	fn := meta.Function
	fnRef := quoteIdent(fn.Schema) + "." + quoteIdent(fn.Name)

	input, _ := s.argValue(f, "input")
	inputObj, _ := input.(map[string]any)
	var params []string
	for _, a := range fn.Args {
		v, ok := inputObj[inflect.LowerCamel(a.Name)]
		if !ok || v == nil {
			params = append(params, "NULL::"+quoteIdent(a.PGType))
			continue
		}
		params = append(params, s.param(v)+"::"+quoteIdent(a.PGType))
	}
	call := fnRef + "(" + strings.Join(params, ", ") + ")"

	payloadType := ""
	if f.Definition != nil {
		payloadType = f.Definition.Type.Name()
	}
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

	// Pick the row source (always containing the call) and the result
	// expression per return shape.
	from := ""
	resultExpr := ""
	switch {
	case fn.ReturnsSet:
		t := s.tableForTypeName(fn.ReturnType)
		if t != nil && resultField != nil && len(resultField.SelectionSet) > 0 {
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(t, resultField.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub := s.alias("s")
			from = fmt.Sprintf("(SELECT coalesce(jsonb_agg(%s.data), '[]'::jsonb) AS data FROM (\n%s\n) AS %s) AS __agg",
				sub, indent(fmt.Sprintf("SELECT %s AS data\nFROM %s AS %s%s", jsonExpr, call, alias, joinLaterals(laterals))), sub)
		} else {
			from = fmt.Sprintf("(SELECT coalesce(jsonb_agg(to_jsonb(__v)), '[]'::jsonb) AS data FROM %s AS __v) AS __agg", call)
		}
		resultExpr = "__agg.data"
	case fn.ReturnType == "void":
		from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
		resultExpr = "to_jsonb(true)"
	default:
		if t := s.tableForTypeName(fn.ReturnType); t != nil && resultField != nil && len(resultField.SelectionSet) > 0 {
			alias := s.alias("t")
			jsonExpr, laterals, err := s.rowJSON(t, resultField.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			from = fmt.Sprintf("%s AS %s%s", call, alias, joinLaterals(laterals))
			// NULL composite -> result: null, payload row still present.
			resultExpr = fmt.Sprintf("CASE WHEN %s IS NULL THEN NULL ELSE %s END", alias, jsonExpr)
		} else {
			from = fmt.Sprintf("(SELECT %s AS v) AS __fn", call)
			resultExpr = "to_jsonb(__fn.v)"
		}
	}
	if resultField != nil {
		pairs = append(pairs, quoteLiteral(resultField.Alias), resultExpr)
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM %s", buildJSONObject(pairs), from), nil
}

// tableForTypeName finds a catalog table whose name matches a function's
// declared return type.
func (s *stmt) tableForTypeName(typeName string) *introspect.Table {
	for _, t := range s.c.Built.Catalog.Tables {
		if t.Name == typeName {
			return t
		}
	}
	return nil
}
