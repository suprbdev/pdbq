package compile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// mutation compiles create/update/delete into a single statement: the DML
// runs in a `WITH __mut AS (... RETURNING ...)` CTE and the payload selection
// is compiled against __mut like a regular table, so relations still work.
// The RETURNING list projects only the columns the payload references:
// RETURNING requires SELECT privilege per projected column, so RETURNING *
// would break every mutation on tables using column-level grants.
//
// Every table scan of the statement shares its snapshot, so a payload
// relation leading back to the mutated table would read it as it stood
// before the DML. When the payload reads the table that way, those reads go
// through a visibility CTE instead (see visibilityCTE).
func (s *stmt) mutation(meta *schema.FieldMeta, f *ast.Field) (string, error) {
	t := meta.Table
	var d dml
	var err error
	switch meta.Kind {
	case schema.KindCreate:
		d.sql, err = s.insertCTE(t, meta, f)
	case schema.KindUpdate:
		d, err = s.updateCTE(t, meta, f)
	case schema.KindDelete:
		d, err = s.deleteCTE(t, meta, f)
	case schema.KindUpsert:
		d, err = s.upsertCTE(t, meta, f)
	case schema.KindCreateMany:
		d.sql, err = s.createManyCTE(t, meta, f)
	case schema.KindUpdateMany:
		d, err = s.updateManyCTE(t, meta, f)
	case schema.KindDeleteMany:
		d, err = s.deleteManyCTE(t, f)
	}
	if err != nil {
		return "", err
	}
	s.overrides = map[string]string{t.Schema + "." + t.Name: visCTE}
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
	returning := referencedColumns(t, payload, s.mutAliases)
	var vis string
	if aliases := s.overrideAliases[visCTE]; len(aliases) > 0 {
		cols := referencedColumns(t, payload, aliases)
		var keyCols []string
		vis, keyCols = s.visibilityCTE(t, meta.Kind, d, cols)
		if !removes(meta.Kind) {
			returning = append(returning, cols...)
		}
		returning = append(returning, keyCols...)
	}
	ctes := fmt.Sprintf("__mut AS (\n%s\n)", indent(d.sql+"\nRETURNING "+columnList(t, returning)))
	if vis != "" {
		ctes += ",\n" + vis
	}
	return fmt.Sprintf("WITH %s\n%s", ctes, payload), nil
}

// visCTE names the visibility CTE of a mutation statement.
const visCTE = "__vis"

// dml is the data-modifying statement of a mutation's __mut CTE.
type dml struct {
	sql string
	// replaced matches, over the bare table reference, the rows the
	// statement updates or deletes as they stood before it ran: the key
	// lookup of a single-row update or delete, the conflict key of an
	// upsert, the filter of a bulk update or delete. Empty for an insert.
	replaced string
	// patched holds the columns an update sets.
	patched map[string]bool
}

// removes reports whether a mutation kind deletes its rows.
func removes(kind schema.FieldKind) bool {
	return kind == schema.KindDelete || kind == schema.KindDeleteMany
}

// visibilityCTE renders the __vis CTE standing in for the mutated table in
// the payload: the table as it stands once the statement completes,
// projected to cols. It is the table itself (still subject to the role's RLS
// policies) minus the pre-statement versions of the rows the DML replaced,
// UNION ALL the RETURNING rows of __mut unless the DML deleted them.
// RETURNING rows already passed the table's SELECT policies: INSERT and
// UPDATE ... RETURNING raise an error for a new row they reject.
//
// The replaced rows are excluded by the lookup that found them (d.replaced),
// which still matches their old versions when the update changed the key.
// A bulk update or delete may have skipped rows its filter matched (an
// UPDATE or DELETE policy narrower than the SELECT one), so it excludes the
// rows whose key __mut returned instead, by a NOT NULL key the patch leaves
// alone; only when every such key is patched does it fall back to the
// filter. It returns the key columns that exclusion reads from __mut, which
// the RETURNING list must project.
//
// NOT MATERIALIZED inlines the CTE at every reference, so the correlated
// relation predicates reach the table's indexes instead of a materialized
// copy of the whole table.
func (s *stmt) visibilityCTE(t *introspect.Table, kind schema.FieldKind, d dml, cols []string) (string, []string) {
	proj := columnList(t, cols)
	table := "SELECT " + proj + " FROM " + tableRef(t)
	var keyCols []string
	bulk := kind == schema.KindUpdateMany || kind == schema.KindDeleteMany
	if key := stableKey(t, d.patched); bulk && key != nil {
		m := s.alias("m")
		conds := make([]string, len(key))
		for i, cn := range key {
			q := quoteIdent(cn)
			conds[i] = fmt.Sprintf("%s.%s %s %s.%s", m, q, s.contrib(t.Column(cn)).Operator("="), tableRef(t), q)
		}
		table += fmt.Sprintf(" WHERE NOT EXISTS (SELECT 1 FROM __mut AS %s WHERE %s)", m, strings.Join(conds, " AND "))
		keyCols = key
	} else if d.replaced != "" {
		table += " WHERE (" + d.replaced + ") IS NOT TRUE"
	}
	parts := []string{table}
	if !removes(kind) {
		parts = append(parts, "SELECT "+proj+" FROM __mut")
	}
	return fmt.Sprintf("%s AS NOT MATERIALIZED (\n  %s\n)", visCTE, strings.Join(parts, "\n  UNION ALL ")), keyCols
}

// stableKey returns the columns of the first row key of t (primary key, then
// unique constraints) that are all NOT NULL and untouched by patched, or nil.
func stableKey(t *introspect.Table, patched map[string]bool) []string {
	keys := append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...)
	for _, con := range keys {
		if con == nil || len(con.Columns) == 0 {
			continue
		}
		ok := true
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil || patched[cn] || (!c.NotNull && con != t.PrimaryKey) {
				ok = false
				break
			}
		}
		if ok {
			return con.Columns
		}
	}
	return nil
}

// referencedColumns returns, in table order, the columns of t that sql
// references through one of aliases. Every column reference is rendered as
// `<alias>.<quoted column>` (columnJSON, nodeIDSQL, rowParamSQL, relation
// correlation conditions), so a substring scan is exact.
func referencedColumns(t *introspect.Table, sql string, aliases []string) []string {
	var cols []string
	for _, c := range t.Columns {
		q := quoteIdent(c.Name)
		for _, a := range aliases {
			if strings.Contains(sql, a+"."+q) {
				cols = append(cols, c.Name)
				break
			}
		}
	}
	return cols
}

// columnList renders the distinct columns of t named in cols as a select
// list in table order. An empty list falls back to a bare literal, so a
// DML whose payload reads no columns (e.g. only clientMutationId or
// affectedCount) still yields its row count.
func columnList(t *introspect.Table, cols []string) string {
	want := make(map[string]bool, len(cols))
	for _, c := range cols {
		want[c] = true
	}
	var out []string
	for _, c := range t.Columns {
		if want[c.Name] {
			out = append(out, quoteIdent(c.Name))
		}
	}
	if len(out) == 0 {
		return "1"
	}
	return strings.Join(out, ", ")
}

// clientMutationID extracts the clientMutationId sent by the client in the
// input argument of mutation field f, if any.
func (s *stmt) clientMutationID(f *ast.Field) (string, bool) {
	if v, ok := s.argValue(f, "input"); ok {
		if obj, ok := v.(map[string]any); ok {
			if id, ok := obj["clientMutationId"].(string); ok {
				return id, true
			}
		}
	}
	return "", false
}

// mutationInput returns the input argument of table mutation field f and
// the name of its input type.
func (s *stmt) mutationInput(f *ast.Field) (map[string]any, string, error) {
	return MutationInput(f, s.req.Vars)
}

// mutationValues returns the row values of table mutation field f (the
// meta.Values field of its input) and the name of their input type; ok is
// false when the input leaves them out.
func (s *stmt) mutationValues(meta *schema.FieldMeta, f *ast.Field) (v any, typeName string, ok bool, err error) {
	input, inputType, err := s.mutationInput(f)
	if err != nil {
		return nil, "", false, err
	}
	v, ok = input[meta.Values]
	return v, inputFieldType(s.c.Built, inputType, meta.Values), ok && v != nil, nil
}

// inputFieldType returns the named type of field of input type inputType,
// or "".
func inputFieldType(built *schema.Built, inputType, field string) string {
	if built.Schema == nil {
		return ""
	}
	def := built.Schema.Types[inputType]
	if def == nil {
		return ""
	}
	if fd := def.Fields.ForName(field); fd != nil {
		return fd.Type.Name()
	}
	return ""
}

// inputKeyConds builds `alias."col" = $n` for each key column of a keyed
// update or delete, reading the field of its input bound to the column.
func (s *stmt) inputKeyConds(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field, alias string) ([]string, error) {
	input, inputType, err := s.mutationInput(f)
	if err != nil {
		return nil, err
	}
	vals, err := InputKey(s.c.Built, inputType, input, meta.KeyColumns)
	if err != nil {
		return nil, err
	}
	conds := make([]string, len(vals))
	for i, col := range meta.KeyColumns {
		c := t.Column(col)
		conds[i] = fmt.Sprintf("%s.%s %s %s", alias, quoteIdent(col), s.contrib(c).Operator("="), s.valueExpr(c, vals[i]))
	}
	return conds, nil
}

// payloadSelect compiles a single-row payload selection against the __mut
// CTE: the row field, its forward relations and edge all read the mutated
// row under one alias, so __mut yielding no row (an update or delete that
// matched nothing) yields no payload row either.
func (s *stmt) payloadSelect(t *introspect.Table, f *ast.Field) (string, error) {
	payloadType := fieldTypeName(f)
	alias := s.alias("t")
	s.mutAliases = append(s.mutAliases, alias)
	var pairs, laterals []string
	for _, sub := range s.expandFor(f.SelectionSet, payloadType) {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(payloadType)+"::text")
			continue
		}
		meta := s.fieldMeta(payloadType, sub.Name)
		if meta == nil {
			continue
		}
		var expr string
		var lats []string
		var err error
		switch meta.Kind {
		case schema.KindPayloadRow:
			expr, lats, err = s.rowJSON(t, sub.SelectionSet, alias, 1)
		case schema.KindPayloadEdge:
			expr, lats, err = s.payloadEdge(t, sub, alias)
		case schema.KindPayloadDeletedNodeID:
			expr = nodeIDSQL(s.c.Built.TypeForTable[t.Schema+"."+t.Name], t, alias)
		default:
			var ok bool
			expr, lats, ok, err = s.payloadCommon(meta, f, sub, alias)
			if !ok {
				continue
			}
		}
		if err != nil {
			return "", err
		}
		pairs = append(pairs, key, expr)
		laterals = append(laterals, lats...)
	}
	return fmt.Sprintf("SELECT %s AS data\nFROM __mut AS %s%s", buildJSONObject(pairs), alias, joinLaterals(laterals)), nil
}

// payloadCommon compiles the field f of the payload of mutation field root
// when its kind does not depend on the mutation: clientMutationId, query,
// and the forward relations of the payload row read at alias. ok is false
// for any other field.
func (s *stmt) payloadCommon(meta *schema.FieldMeta, root, f *ast.Field, alias string) (string, []string, bool, error) {
	switch meta.Kind {
	case schema.KindClientMutationID:
		return s.clientMutationIDExpr(root), nil, true, nil
	case schema.KindPayloadQuery:
		expr, err := s.payloadQuery(f)
		return expr, nil, true, err
	case schema.KindRelationForward:
		expr, lat, err := s.forwardRelation(meta, f, alias, 2)
		return expr, []string{lat}, true, err
	}
	return "", nil, false, nil
}

// payloadEdge compiles a payload's <row>Edge over the row of t at alias.
// The cursor is the row's nodeId, which is the keyset cursor of every
// ordering of t's connections, so the edge's orderBy argument does not
// change it.
func (s *stmt) payloadEdge(t *introspect.Table, f *ast.Field, alias string) (string, []string, error) {
	typeName := s.c.Built.TypeForTable[t.Schema+"."+t.Name]
	edgeType := typeName + "Edge"
	var pairs, laterals []string
	for _, ef := range s.expandFor(f.SelectionSet, edgeType) {
		key := quoteLiteral(ef.Alias)
		switch ef.Name {
		case "__typename":
			pairs = append(pairs, key, quoteLiteral(edgeType)+"::text")
		case "cursor":
			pairs = append(pairs, key, nodeIDSQL(typeName, t, alias))
		case "node":
			expr, lats, err := s.rowJSON(t, ef.SelectionSet, alias, 2)
			if err != nil {
				return "", nil, err
			}
			pairs = append(pairs, key, expr)
			laterals = append(laterals, lats...)
		}
	}
	return buildJSONObject(pairs), laterals, nil
}

// payloadQuery compiles a payload's query field: each Query field it
// selects compiles as it would at the root and is embedded as a scalar
// subquery (a lookup matching no row yields null), so the payload reads it
// in the mutation's own statement. Those reads honour the statement's
// overrides, so a table mutation's query field sees the mutated table as
// it stands after the DML, like the payload's relations do.
func (s *stmt) payloadQuery(f *ast.Field) (string, error) {
	var pairs []string
	for _, sub := range s.expandFor(f.SelectionSet, "Query") {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, "'Query'::text")
			continue
		}
		meta := s.fieldMeta("Query", sub.Name)
		if meta == nil {
			return "", errUser("%s.%s cannot be selected in a mutation payload", f.Alias, sub.Name)
		}
		sql, err := s.rootField(meta, sub)
		if err != nil {
			return "", err
		}
		q := s.alias("q")
		pairs = append(pairs, key, fmt.Sprintf("(SELECT %s.data FROM (\n%s\n) AS %s)", q, indent(sql), q))
	}
	return buildJSONObject(pairs), nil
}

// clientMutationIDExpr renders the clientMutationId the client sent with
// mutation field f (see clientMutationID) as a text parameter, or a NULL
// literal.
func (s *stmt) clientMutationIDExpr(f *ast.Field) string {
	id, ok := s.clientMutationID(f)
	if !ok {
		return "NULL::text"
	}
	return s.param(id) + "::text"
}

// payloadSelectMany compiles a bulk-mutation payload against the __mut CTE:
// the rows field aggregates every mutated row, affectedCount counts them.
// Aggregates without GROUP BY always yield one row, so a bulk statement never
// maps to "no row matched" even when nothing was affected.
func (s *stmt) payloadSelectMany(t *introspect.Table, f *ast.Field) (string, error) {
	payloadType := fieldTypeName(f)
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
			s.mutAliases = append(s.mutAliases, alias)
			jsonExpr, laterals, err := s.rowJSON(t, sub.SelectionSet, alias, 1)
			if err != nil {
				return "", err
			}
			sub2 := s.alias("s")
			pairs = append(pairs, quoteLiteral(sub.Alias), fmt.Sprintf(
				"(SELECT coalesce(json_agg(%s.data), '[]'::json) FROM (\n%s\n) AS %s)",
				sub2,
				indent(fmt.Sprintf("SELECT %s AS data\nFROM __mut AS %s%s", jsonExpr, alias, joinLaterals(laterals))),
				sub2))
		case schema.KindPayloadCount:
			pairs = append(pairs, quoteLiteral(sub.Alias), "(SELECT count(*) FROM __mut)")
		default:
			expr, _, ok, err := s.payloadCommon(meta, f, sub, "")
			if err != nil {
				return "", err
			}
			if ok {
				pairs = append(pairs, quoteLiteral(sub.Alias), expr)
			}
		}
	}
	return fmt.Sprintf("SELECT %s AS data", buildJSONObject(pairs)), nil
}

// createManyCTE compiles a multi-row INSERT. The column list is the union of
// columns provided across rows; rows missing a column insert DEFAULT there.
func (s *stmt) createManyCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (string, error) {
	v, inputType, _, err := s.mutationValues(meta, f)
	if err != nil {
		return "", err
	}
	rows, ok := v.([]any)
	if !ok || len(rows) == 0 {
		return "", errUser("%s: %s must be a non-empty list", f.Name, meta.Values)
	}
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
			return "", errUser("%s: %s must be objects", f.Name, meta.Values)
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
		return "", errUser("%s: %s set no columns", f.Name, meta.Values)
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
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES %s",
		tableRef(t), strings.Join(cols, ", "), strings.Join(valueRows, ",\n       ")), nil
}

func (s *stmt) updateManyCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (dml, error) {
	cols, params, err := s.patchColumns(t, meta, f)
	if err != nil {
		return dml{}, err
	}
	sets := make([]string, len(cols))
	patched := make(map[string]bool, len(cols))
	for i, c := range cols {
		sets[i] = quoteIdent(c) + " = " + params[i]
		patched[c] = true
	}
	cond, err := s.bulkFilterCond(t, f)
	if err != nil {
		return dml{}, err
	}
	return dml{
		sql:      fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s", tableRef(t), strings.Join(sets, ", "), cond),
		replaced: cond,
		patched:  patched,
	}, nil
}

func (s *stmt) deleteManyCTE(t *introspect.Table, f *ast.Field) (dml, error) {
	cond, err := s.bulkFilterCond(t, f)
	if err != nil {
		return dml{}, err
	}
	return dml{sql: fmt.Sprintf("DELETE FROM %s\nWHERE %s", tableRef(t), cond), replaced: cond}, nil
}

// bulkFilterCond resolves the required filter argument of a bulk mutation to
// a WHERE condition over the bare table reference. An empty filter object
// deliberately matches every row — the argument itself is non-null so the
// caller must always write one.
func (s *stmt) bulkFilterCond(t *introspect.Table, f *ast.Field) (string, error) {
	input, inputType, err := s.mutationInput(f)
	if err != nil {
		return "", err
	}
	fv, ok := input["filter"]
	if !ok || fv == nil {
		return "", errUser("%s: missing filter", f.Name)
	}
	cond, err := s.filterCond(t, inputFieldType(s.c.Built, inputType, "filter"), fv, tableRef(t))
	if err != nil {
		return "", err
	}
	if cond == "" {
		cond = "TRUE"
	}
	return cond, nil
}

func (s *stmt) insertCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (string, error) {
	var cols, params []string
	if meta.Values != "" {
		v, inputType, ok, err := s.mutationValues(meta, f)
		if err != nil {
			return "", err
		}
		vals, isObj := v.(map[string]any)
		if !ok || !isObj {
			return "", errUser("%s: missing %s", f.Name, meta.Values)
		}
		if cols, params, err = s.inputColumns(t, inputType, vals); err != nil {
			return "", err
		}
	} else if _, _, err := s.mutationInput(f); err != nil {
		return "", err
	}
	if len(cols) == 0 {
		return fmt.Sprintf("INSERT INTO %s DEFAULT VALUES", tableRef(t)), nil
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quoteIdent(c)
	}
	return fmt.Sprintf("INSERT INTO %s (%s)\nVALUES (%s)",
		tableRef(t), strings.Join(quoted, ", "), strings.Join(params, ", ")), nil
}

// inputFieldFor returns the field of input type inputType bound to column
// col, or col when no field binds it.
func inputFieldFor(built *schema.Built, inputType, col string) string {
	for field, c := range built.InputColumns[inputType] {
		if c == col {
			return field
		}
	}
	return col
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
// provided input column the table's update patch (meta.Patch) binds, except
// the conflict target, is updated from EXCLUDED, so a column left out of the
// patch (`@omit update`) is only ever set on insert; when no such column is
// provided, the target updates to itself so the statement still returns the
// existing row instead of DO NOTHING's zero rows.
func (s *stmt) upsertCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (dml, error) {
	v, inputType, ok, err := s.mutationValues(meta, f)
	if err != nil {
		return dml{}, err
	}
	vals, isObj := v.(map[string]any)
	if !ok || !isObj {
		return dml{}, errUser("%s: missing %s", f.Name, meta.Values)
	}
	cols, params, err := s.inputColumns(t, inputType, vals)
	if err != nil {
		return dml{}, err
	}
	target := map[string]bool{}
	for _, kc := range meta.KeyColumns {
		target[kc] = true
	}
	provided := map[string]string{}
	for i, c := range cols {
		provided[c] = params[i]
	}
	keyConds := make([]string, len(meta.KeyColumns))
	for i, kc := range meta.KeyColumns {
		p, ok := provided[kc]
		if !ok {
			return dml{}, errUser("%s: %s must set %s", f.Name, meta.Values, inputFieldFor(s.c.Built, inputType, kc))
		}
		keyConds[i] = fmt.Sprintf("%s.%s %s %s", tableRef(t), quoteIdent(kc), s.contrib(t.Column(kc)).Operator("="), p)
	}
	updatable := map[string]bool{}
	for _, col := range s.c.Built.InputColumns[meta.Patch] {
		updatable[col] = true
	}
	var sets []string
	for _, c := range cols {
		if !target[c] && updatable[c] {
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
	return dml{
		sql: fmt.Sprintf("INSERT INTO %s (%s)\nVALUES (%s)\nON CONFLICT (%s) DO UPDATE\nSET %s",
			tableRef(t), strings.Join(quoted, ", "), strings.Join(params, ", "),
			strings.Join(targetCols, ", "), strings.Join(sets, ", ")),
		replaced: strings.Join(keyConds, " AND "),
	}, nil
}

func (s *stmt) updateCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (dml, error) {
	cols, params, err := s.patchColumns(t, meta, f)
	if err != nil {
		return dml{}, err
	}
	sets := make([]string, len(cols))
	for i, c := range cols {
		sets[i] = quoteIdent(c) + " = " + params[i]
	}
	conds, err := s.inputKeyConds(t, meta, f, tableRef(t))
	if err != nil {
		return dml{}, err
	}
	cond := strings.Join(conds, " AND ")
	return dml{
		sql:      fmt.Sprintf("UPDATE %s\nSET %s\nWHERE %s", tableRef(t), strings.Join(sets, ", "), cond),
		replaced: cond,
	}, nil
}

// patchColumns maps the patch of update mutation field f to (columns,
// placeholders); a patch setting no column is an error.
func (s *stmt) patchColumns(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) ([]string, []string, error) {
	v, patchType, _, err := s.mutationValues(meta, f)
	if err != nil {
		return nil, nil, err
	}
	patch, _ := v.(map[string]any)
	cols, params, err := s.inputColumns(t, patchType, patch)
	if err != nil {
		return nil, nil, err
	}
	if len(cols) == 0 {
		return nil, nil, errUser("%s: %s must set at least one column", f.Name, meta.Values)
	}
	return cols, params, nil
}

func (s *stmt) deleteCTE(t *introspect.Table, meta *schema.FieldMeta, f *ast.Field) (dml, error) {
	conds, err := s.inputKeyConds(t, meta, f, tableRef(t))
	if err != nil {
		return dml{}, err
	}
	cond := strings.Join(conds, " AND ")
	return dml{sql: fmt.Sprintf("DELETE FROM %s\nWHERE %s", tableRef(t), cond), replaced: cond}, nil
}
