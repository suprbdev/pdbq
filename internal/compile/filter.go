package compile

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// filterCond compiles a <Type>Filter input value into a boolean SQL
// expression over alias, or "" when the filter is empty.
func (s *stmt) filterCond(t *introspect.Table, filterType string, v any, alias string) (string, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compile: filter must be an object")
	}
	cols := s.c.Built.InputColumns[filterType]
	var conds []string
	for _, key := range sortedFilterKeys(m) {
		val := m[key]
		if val == nil {
			continue
		}
		switch key {
		case "and", "or":
			items, ok := val.([]any)
			if !ok {
				return "", fmt.Errorf("compile: filter %s must be a list", key)
			}
			var subs []string
			for _, item := range items {
				sub, err := s.filterCond(t, filterType, item, alias)
				if err != nil {
					return "", err
				}
				if sub != "" {
					subs = append(subs, sub)
				}
			}
			if len(subs) == 0 {
				continue
			}
			op := " AND "
			if key == "or" {
				op = " OR "
			}
			conds = append(conds, "("+strings.Join(subs, op)+")")
		case "not":
			sub, err := s.filterCond(t, filterType, val, alias)
			if err != nil {
				return "", err
			}
			if sub != "" {
				conds = append(conds, "NOT ("+sub+")")
			}
		default:
			colName, ok := cols[key]
			if !ok {
				// Plugin-added filter fields (advanced-filters): relations
				// compile to EXISTS subqueries, computed columns to function
				// calls over the current row.
				if rel := s.c.Built.FilterRelations[filterType][key]; rel != nil {
					cond, err := s.relationCond(rel, val, alias)
					if err != nil {
						return "", err
					}
					if cond != "" {
						conds = append(conds, cond)
					}
					continue
				}
				if fn := s.c.Built.FilterComputed[filterType][key]; fn != nil {
					cond, err := s.computedCond(t, fn, val, alias)
					if err != nil {
						return "", err
					}
					if cond != "" {
						conds = append(conds, cond)
					}
					continue
				}
				return "", fmt.Errorf("compile: filter field %q has no column mapping", key)
			}
			col := t.Column(colName)
			if col == nil {
				return "", fmt.Errorf("compile: filter column %q not found", colName)
			}
			cond, err := s.columnOps(col, val, alias)
			if err != nil {
				return "", err
			}
			if cond != "" {
				conds = append(conds, cond)
			}
		}
	}
	return strings.Join(conds, " AND "), nil
}

// sortedFilterKeys yields deterministic condition order for golden tests.
func sortedFilterKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// insertion order is lost in maps; sort lexicographically.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// relationCond compiles a relation filter field. Forward relations take the
// related table's filter directly; backward relations take a
// {some|none|every} wrapper over it.
func (s *stmt) relationCond(rel *schema.FilterRelation, v any, alias string) (string, error) {
	if rel.Forward {
		return s.relationExists(rel, v, alias, "some")
	}
	m, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compile: relation filter must be an object")
	}
	var conds []string
	for _, quant := range sortedFilterKeys(m) {
		val := m[quant]
		if val == nil {
			continue
		}
		switch quant {
		case "some", "none", "every":
			cond, err := s.relationExists(rel, val, alias, quant)
			if err != nil {
				return "", err
			}
			if cond != "" {
				conds = append(conds, cond)
			}
		default:
			return "", fmt.Errorf("compile: unknown relation quantifier %q", quant)
		}
	}
	return strings.Join(conds, " AND "), nil
}

// relationExists renders one (NOT) EXISTS subquery joining the related table
// to the current row: some = EXISTS(match), none = NOT EXISTS(match),
// every = NOT EXISTS(NOT match).
func (s *stmt) relationExists(rel *schema.FilterRelation, v any, alias, quant string) (string, error) {
	sub := s.alias("r")
	var joins []string
	for i, col := range rel.FK.Columns {
		if rel.Forward {
			joins = append(joins, fmt.Sprintf("%s.%s = %s.%s",
				sub, quoteIdent(rel.FK.RefColumns[i]), alias, quoteIdent(col)))
		} else {
			joins = append(joins, fmt.Sprintf("%s.%s = %s.%s",
				sub, quoteIdent(col), alias, quoteIdent(rel.FK.RefColumns[i])))
		}
	}
	cond, err := s.filterCond(rel.Table, rel.FilterType, v, sub)
	if err != nil {
		return "", err
	}
	where := strings.Join(joins, " AND ")
	if quant == "every" {
		if cond == "" {
			return "", nil // every row trivially matches an empty filter
		}
		where += " AND NOT (" + cond + ")"
	} else if cond != "" {
		where += " AND (" + cond + ")"
	}
	prefix := "EXISTS"
	if quant == "none" || quant == "every" {
		prefix = "NOT EXISTS"
	}
	return fmt.Sprintf("%s (SELECT 1 FROM %s AS %s WHERE %s)",
		prefix, s.sourceRef(rel.Table), sub, where), nil
}

// computedCond compiles operator conditions over a computed column: the
// bound row-type function called on the current row.
func (s *stmt) computedCond(t *introspect.Table, fn *introspect.Function, v any, alias string) (string, error) {
	ret := &introspect.Column{Name: fn.Name, PGType: fn.ReturnType, TypeSchema: fn.ReturnTypeSchema}
	return s.refOps(ret, computedCallSQL(fn, t, alias), v)
}

// columnOps compiles one column's operator object ({eq: .., gt: ..}).
func (s *stmt) columnOps(col *introspect.Column, v any, alias string) (string, error) {
	return s.refOps(col, alias+"."+quoteIdent(col.Name), v)
}

// refOps compiles an operator object against an arbitrary SQL value
// expression; col carries the type information driving coercion.
func (s *stmt) refOps(col *introspect.Column, ref string, v any) (string, error) {
	ops, ok := v.(map[string]any)
	if !ok {
		return "", fmt.Errorf("compile: filter for column %q must be an operator object", col.Name)
	}
	var conds []string
	for _, op := range sortedFilterKeys(ops) {
		val := ops[op]
		if val == nil {
			continue
		}
		cond, err := s.oneOp(col, ref, op, val)
		if err != nil {
			return "", err
		}
		conds = append(conds, cond)
	}
	return strings.Join(conds, " AND "), nil
}

func (s *stmt) oneOp(col *introspect.Column, ref, op string, val any) (string, error) {
	switch op {
	case "eq":
		return ref + " = " + s.param(s.coerce(col, val)), nil
	case "ne":
		return ref + " <> " + s.param(s.coerce(col, val)), nil
	case "in":
		return ref + " = ANY(" + s.param(s.coerceList(col, val)) + ")", nil
	case "notIn":
		return ref + " <> ALL(" + s.param(s.coerceList(col, val)) + ")", nil
	case "isNull":
		if b, _ := val.(bool); b {
			return ref + " IS NULL", nil
		}
		return ref + " IS NOT NULL", nil
	case "lt":
		return ref + " < " + s.param(s.coerce(col, val)), nil
	case "lte":
		return ref + " <= " + s.param(s.coerce(col, val)), nil
	case "gt":
		return ref + " > " + s.param(s.coerce(col, val)), nil
	case "gte":
		return ref + " >= " + s.param(s.coerce(col, val)), nil
	case "like":
		return ref + " LIKE " + s.param(val), nil
	case "ilike":
		return ref + " ILIKE " + s.param(val), nil
	case "startsWith":
		str, _ := val.(string)
		return ref + " LIKE " + s.param(escapeLike(str)+"%"), nil
	case "endsWith":
		str, _ := val.(string)
		return ref + " LIKE " + s.param("%"+escapeLike(str)), nil
	case "contains":
		if col.PGType == "jsonb" || col.PGType == "json" {
			return ref + " @> " + s.param(toJSONParam(val)) + "::jsonb", nil
		}
		return ref + " @> " + s.param(s.coerceList(col, val)), nil
	case "containedBy":
		if col.PGType == "jsonb" || col.PGType == "json" {
			return ref + " <@ " + s.param(toJSONParam(val)) + "::jsonb", nil
		}
		return ref + " <@ " + s.param(s.coerceList(col, val)), nil
	case "overlaps":
		return ref + " && " + s.param(s.coerceList(col, val)), nil
	case "hasKey":
		return ref + " ? " + s.param(val), nil
	case "pathExists":
		return jsonbExpr(col, ref) + " @? " + s.param(val) + "::jsonpath", nil
	case "pathMatch":
		return jsonbExpr(col, ref) + " @@ " + s.param(val) + "::jsonpath", nil
	}
	return "", fmt.Errorf("compile: unknown filter operator %q", op)
}

// jsonbExpr renders a column reference as jsonb; json columns are cast because
// the jsonpath operators are defined only for jsonb.
func jsonbExpr(col *introspect.Column, ref string) string {
	if col.PGType == "json" {
		return ref + "::jsonb"
	}
	return ref
}

func escapeLike(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `%`, `\%`)
	return strings.ReplaceAll(v, `_`, `\_`)
}

// coerce converts a GraphQL input value into a driver-friendly parameter for
// the given column: enum names map to their PostgreSQL labels, JSON values
// are re-encoded as text for jsonb params.
func (s *stmt) coerce(col *introspect.Column, v any) any {
	return coerceInput(s.c.Built, col, v)
}

func coerceInput(built *schema.Built, col *introspect.Column, v any) any {
	if col == nil || v == nil {
		return v
	}
	base := strings.TrimPrefix(col.PGType, "_")
	if e := built.Catalog.Enum(col.TypeSchema, base); e != nil {
		if name, ok := v.(string); ok {
			enumType := built.EnumTypeForPG[col.TypeSchema+"."+base]
			if vals, ok := built.EnumValues[enumType]; ok {
				if pg, ok := vals[name]; ok {
					return pg
				}
			}
		}
		return v
	}
	switch base {
	case "json", "jsonb":
		return toJSONParam(v)
	}
	if col.IsArray {
		if list, ok := v.([]any); ok {
			return coerceInputSlice(built, col, list)
		}
	}
	return v
}

// coerceList coerces a list-valued input ([X!] for in/notIn/array ops).
func (s *stmt) coerceList(col *introspect.Column, v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	return s.coerceSlice(col, list)
}

func (s *stmt) coerceSlice(col *introspect.Column, list []any) []any {
	return coerceInputSlice(s.c.Built, col, list)
}

func coerceInputSlice(built *schema.Built, col *introspect.Column, list []any) []any {
	elem := *col
	elem.IsArray = false
	out := make([]any, len(list))
	for i, item := range list {
		out[i] = coerceInput(built, &elem, item)
	}
	return out
}

// valueExpr renders an input value as a SQL parameter expression for the
// given column; composite columns are populated from a jsonb parameter.
func (s *stmt) valueExpr(col *introspect.Column, v any) string {
	if comp := s.compositeFor(col); comp != nil && v != nil {
		return s.compositeParam(comp, col, v)
	}
	return s.param(s.coerce(col, v))
}

// compositeParam renders a composite (or composite-array) input value as a
// jsonb parameter populated into the PostgreSQL composite type. The whole
// value collapses into a single parameter, keeping the SQL text independent
// of the input's field set beyond the column list.
func (s *stmt) compositeParam(comp *introspect.Composite, col *introspect.Column, v any) string {
	typeRef := quoteIdent(comp.Schema) + "." + quoteIdent(comp.Name)
	if col.IsArray {
		list, _ := v.([]any)
		out := make([]any, len(list))
		for i, item := range list {
			out[i] = s.compositeValue(comp, item)
		}
		elem := s.alias("e")
		return fmt.Sprintf("coalesce((SELECT array_agg(jsonb_populate_record(NULL::%s, %s.v) ORDER BY %s.o) FROM jsonb_array_elements(%s::jsonb) WITH ORDINALITY AS %s(v, o)), ARRAY[]::%s[])",
			typeRef, elem, elem, s.param(toJSONParam(out)), elem, typeRef)
	}
	return fmt.Sprintf("jsonb_populate_record(NULL::%s, %s::jsonb)", typeRef, s.param(toJSONParam(s.compositeValue(comp, v))))
}

// compositeValue maps a GraphQL composite input object to a jsonb-encodable
// map keyed by PostgreSQL attribute names, coercing attribute values.
func (s *stmt) compositeValue(comp *introspect.Composite, v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	inputName := s.c.Built.CompositeInputForPG[comp.Schema+"."+comp.Name]
	binding := s.c.Built.InputColumns[inputName]
	out := map[string]any{}
	for name, val := range m {
		attrName, ok := binding[name]
		if !ok {
			continue
		}
		var attr *introspect.Column
		for _, f := range comp.Fields {
			if f.Name == attrName {
				attr = f
				break
			}
		}
		if attr == nil {
			continue
		}
		out[attrName] = s.compositeFieldValue(attr, val)
	}
	return out
}

// compositeFieldValue coerces one composite attribute value for embedding in
// the jsonb parameter fed to jsonb_populate_record.
func (s *stmt) compositeFieldValue(c *introspect.Column, v any) any {
	if v == nil {
		return nil
	}
	if comp := s.compositeFor(c); comp != nil {
		if c.IsArray {
			if list, ok := v.([]any); ok {
				out := make([]any, len(list))
				for i, item := range list {
					out[i] = s.compositeValue(comp, item)
				}
				return out
			}
			return v
		}
		return s.compositeValue(comp, v)
	}
	base := strings.TrimPrefix(c.PGType, "_")
	if base == "json" || base == "jsonb" {
		return v // keep the raw value: coerce would double-encode it as text
	}
	return s.coerce(c, v)
}

// toJSONParam renders any GraphQL value as JSON text (jsonb parameter).
func toJSONParam(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}
