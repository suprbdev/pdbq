package compile

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// connectionAggregate compiles an aggregates or groupedAggregates field of a
// connection over t into a scalar subquery. The subquery scans the
// connection's filtered row set — the filter and condition arguments of conn
// plus the parent correlation extraCond (%%ALIAS%% placeholder) — and never
// its page, like totalCount.
func (s *stmt) connectionAggregate(t *introspect.Table, conn, f *ast.Field, meta *schema.FieldMeta, extraCond string, depth int) (string, error) {
	if depth > s.maxDepth {
		return "", errUser("selection depth exceeds limit (%d)", s.maxDepth)
	}
	alias := s.alias("a")
	where, err := s.whereClause(t, conn, alias, strings.ReplaceAll(extraCond, "%%ALIAS%%", alias))
	if err != nil {
		return "", err
	}
	if meta.Kind == schema.KindGroupedAggregates {
		return s.groupedAggregates(t, f, alias, where)
	}
	obj, err := s.aggregatesJSON(f, alias, "NULL::json")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("(SELECT %s FROM %s AS %s%s)", obj, s.sourceRef(t, alias), alias, where), nil
}

// groupedAggregates renders groupedAggregates: one aggregates object per
// group of the filtered row set, groups ordered by their keys and capped at
// the page-size limit. where is the WHERE clause over alias.
func (s *stmt) groupedAggregates(t *introspect.Table, f *ast.Field, alias, where string) (string, error) {
	gv, _ := s.argValue(f, "groupBy")
	vals, _ := gv.([]any)
	if sv, ok := gv.(string); ok {
		vals = []any{sv}
	}
	if len(vals) == 0 {
		return "", errUser("groupBy needs at least one value")
	}
	specs := s.c.Built.GroupBy[argTypeName(f, "groupBy")]
	exprs := make([]string, len(vals))
	keys := make([]string, len(vals))
	cols := make([]string, len(vals))
	inOrder := make([]string, len(vals))
	order := make([]string, len(vals))
	g := s.alias("g")
	for i, v := range vals {
		name, _ := v.(string)
		gb := specs[name]
		if gb == nil {
			return "", errUser("unknown groupBy value %q", name)
		}
		expr := alias + "." + quoteIdent(gb.Column.Name)
		if gb.SQL != nil {
			expr = gb.SQL(expr)
		}
		key := "__k" + strconv.Itoa(i+1)
		exprs[i] = expr
		keys[i] = "min((" + s.groupKeyText(expr, gb.Column) + `) COLLATE "C")`
		cols[i] = expr + " AS " + key
		inOrder[i] = key
		order[i] = g + "." + key
	}
	having := ""
	if hv, ok := s.argValue(f, "having"); ok {
		cond, err := s.havingCond(argTypeName(f, "having"), hv, alias)
		if err != nil {
			return "", err
		}
		if cond != "" {
			having = "\nHAVING " + cond
		}
	}
	obj, err := s.aggregatesJSON(f, alias, "json_build_array("+strings.Join(keys, ", ")+")")
	if err != nil {
		return "", err
	}
	inner := fmt.Sprintf("SELECT %s AS data, %s\nFROM %s AS %s%s\nGROUP BY %s%s\nORDER BY %s\nLIMIT %s",
		obj, strings.Join(cols, ", "), s.sourceRef(t, alias), alias, where,
		strings.Join(exprs, ", "), having, strings.Join(inOrder, ", "), s.param(s.maxPageSize))
	return fmt.Sprintf("(SELECT coalesce(json_agg(%s.data ORDER BY %s), '[]'::json) FROM (\n%s\n) AS %s)",
		g, strings.Join(order, ", "), indent(inner), g), nil
}

// groupKeyText renders one row's group key as text, like the column's own
// output: scalars by their JSON string or number text, json/jsonb values
// (and arrays) as JSON text, so a JSON null, a JSON string "1" and a number
// 1 stay distinct from each other and from SQL NULL. The caller takes the
// group's min under the C collation: values that compare equal without
// being identical (citext case variants, numeric scales, nondeterministic
// collations) then yield the same representative on every run.
func (s *stmt) groupKeyText(expr string, c *introspect.Column) string {
	if c.IsArray {
		if mapped, ok := s.enumArrayJSON(expr, c); ok {
			return mapped + "::text"
		}
		return "to_jsonb(" + expr + ")::text"
	}
	if base := strings.TrimPrefix(c.PGType, "_"); base == "json" || base == "jsonb" {
		return "to_jsonb(" + expr + ")::text"
	}
	return "to_jsonb(" + s.exprJSON(expr, c) + ") #>> '{}'"
}

// aggregatesJSON renders the selection of an aggregates object (the
// <Type>Aggregates type) over the rows at alias as one json object; keys is
// the expression of its keys field.
func (s *stmt) aggregatesJSON(f *ast.Field, alias, keys string) (string, error) {
	if f.Definition == nil {
		return "", fmt.Errorf("compile: field %s: missing definition", f.Name)
	}
	typeName := f.Definition.Type.Name()
	var pairs []string
	for _, sub := range s.expandFor(f.SelectionSet, typeName) {
		key := quoteLiteral(sub.Alias)
		if sub.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(typeName)+"::text")
			continue
		}
		meta := s.fieldMeta(typeName, sub.Name)
		if meta == nil || sub.Definition == nil {
			return "", fmt.Errorf("compile: no metadata for %s.%s", typeName, sub.Name)
		}
		switch meta.Kind {
		case schema.KindAggregateKeys:
			pairs = append(pairs, key, keys)
		case schema.KindAggregateGroup:
			groupType := sub.Definition.Type.Name()
			var inner []string
			for _, vf := range s.expandFor(sub.SelectionSet, groupType) {
				vkey := quoteLiteral(vf.Alias)
				if vf.Name == "__typename" {
					inner = append(inner, vkey, quoteLiteral(groupType)+"::text")
					continue
				}
				vm := s.fieldMeta(groupType, vf.Name)
				if vm == nil || vm.Kind != schema.KindAggregateValue || vm.Aggregate == nil {
					return "", fmt.Errorf("compile: no metadata for %s.%s", groupType, vf.Name)
				}
				inner = append(inner, vkey, s.exprJSON(aggregateExpr(vm.Aggregate, alias), vm.Aggregate.ResultColumn()))
			}
			pairs = append(pairs, key, buildJSONObject(inner))
		default:
			return "", fmt.Errorf("compile: field %s.%s (kind %s) is not selectable here", typeName, sub.Name, meta.Kind)
		}
	}
	return buildJSONObject(pairs), nil
}

// aggregateExpr renders the aggregate a over the rows at alias.
func aggregateExpr(a *schema.Aggregate, alias string) string {
	ref := ""
	if a.Column != nil {
		ref = alias + "." + quoteIdent(a.Column.Name)
	}
	return a.SQL(ref)
}

// havingCond compiles a <Type>HavingInput value into a boolean expression
// over the aggregates of the group at alias: AND/OR lists combine nested
// having inputs (an empty one matches every group, so it makes an OR
// true), every other field is an aggregate group of column operator
// objects.
func (s *stmt) havingCond(typeName string, v any, alias string) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", errUser("having must be an object")
	}
	var conds []string
	for _, key := range sortedFilterKeys(m) {
		val := m[key]
		if val == nil {
			continue
		}
		switch key {
		case "AND", "OR":
			items, ok := val.([]any)
			if !ok {
				return "", errUser("having %s must be a list", key)
			}
			var subs []string
			mark := len(s.args)
			for _, item := range items {
				sub, err := s.havingCond(typeName, item, alias)
				if err != nil {
					return "", err
				}
				if sub != "" {
					subs = append(subs, sub)
				}
			}
			if key == "OR" && len(subs) < len(items) {
				s.args = s.args[:mark]
				continue
			}
			if len(subs) > 0 {
				conds = append(conds, "("+strings.Join(subs, " "+key+" ")+")")
			}
		default:
			sub, err := s.aggregateGroupConds(s.inputFieldType(typeName, key), val, alias)
			if err != nil {
				return "", err
			}
			conds = append(conds, sub...)
		}
	}
	return strings.Join(conds, " AND "), nil
}

// aggregateGroupConds compiles one aggregate group of a having or
// relation-aggregate filter ({id: {greaterThan: 5}}) into one condition per
// column, comparing the bound aggregate over the rows at alias.
func (s *stmt) aggregateGroupConds(groupType string, v any, alias string) ([]string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errUser("aggregate filter must be an object")
	}
	bindings := s.c.Built.InputAggregates[groupType]
	var conds []string
	for _, key := range sortedFilterKeys(m) {
		if m[key] == nil {
			continue
		}
		a := bindings[key]
		if a == nil {
			return nil, fmt.Errorf("compile: aggregate filter field %s.%s has no binding", groupType, key)
		}
		cond, err := s.refOps(a.ResultColumn(), aggregateExpr(a, alias), m[key])
		if err != nil {
			return nil, err
		}
		if cond != "" {
			conds = append(conds, cond)
		}
	}
	return conds, nil
}

// relationAggregateCond compiles the aggregates field of a to-many relation
// filter ({aggregates: {filter: {...}, sum: {id: {greaterThan: 5}}}}): a
// scalar subquery over the related rows (narrowed by filter) whose boolean
// result ANDs the aggregate comparisons. An empty set of related rows
// aggregates to NULL (0 for sum), so comparisons other than on sum fail; a
// NULL comparison counts as false, keeping not: {...} an exact complement.
func (s *stmt) relationAggregateCond(rel *schema.FilterRelation, filterType string, v any, alias string) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", errUser("aggregates filter must be an object")
	}
	sub := s.alias("r")
	var where string
	if rel.Forward {
		where = s.fkJoinSQL(rel.FK, alias, sub, true)
	} else {
		where = s.fkJoinSQL(rel.FK, sub, alias, false)
	}
	var conds []string
	for _, key := range sortedFilterKeys(m) {
		val := m[key]
		if val == nil {
			continue
		}
		if key == "filter" {
			scope := s.rankScope
			s.rankScope = false
			cond, err := s.filterCond(rel.Table, rel.FilterType, val, sub)
			s.rankScope = scope
			if err != nil {
				return "", err
			}
			if cond != "" {
				where += " AND (" + cond + ")"
			}
			continue
		}
		group, err := s.aggregateGroupConds(s.inputFieldType(filterType, key), val, sub)
		if err != nil {
			return "", err
		}
		conds = append(conds, group...)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return fmt.Sprintf("coalesce((SELECT %s FROM %s AS %s WHERE %s), false)",
		strings.Join(conds, " AND "), s.sourceRef(rel.Table, sub), sub, where), nil
}

// inputFieldType returns the named type of field on the input object
// typeName, or "" when either is unknown.
func (s *stmt) inputFieldType(typeName, field string) string {
	def := s.c.Built.Schema.Types[typeName]
	if def == nil {
		return ""
	}
	fd := def.Fields.ForName(field)
	if fd == nil {
		return ""
	}
	return fd.Type.Name()
}
