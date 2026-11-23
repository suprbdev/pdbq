package compile

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/contribtype"
)

var intervalMakeArgs = []struct{ field, arg, cast string }{
	{"years", "years", "int"},
	{"months", "months", "int"},
	{"days", "days", "int"},
	{"hours", "hours", "int"},
	{"minutes", "mins", "int"},
	{"seconds", "secs", "float8"},
}

var intervalExtract = map[string]struct{ part, cast string }{
	"years":   {"year", "bigint"},
	"months":  {"month", "bigint"},
	"days":    {"day", "bigint"},
	"hours":   {"hour", "bigint"},
	"minutes": {"minute", "bigint"},
	"seconds": {"second", "float8"},
}

// intervalNumber reads one IntervalInput field as a number; missing and null
// fields are zero.
func intervalNumber(m map[string]any, field string, integral bool) (float64, error) {
	var f float64
	switch n := m[field].(type) {
	case nil:
		return 0, nil
	case int:
		f = float64(n)
	case int32:
		f = float64(n)
	case int64:
		f = float64(n)
	case float64:
		f = n
	case json.Number:
		var err error
		if f, err = n.Float64(); err != nil {
			return 0, errUser("interval %s must be a number", field)
		}
	default:
		return 0, errUser("interval %s must be a number", field)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, errUser("interval %s must be finite", field)
	}
	if integral && (f != math.Trunc(f) || math.Abs(f) > math.MaxInt32) {
		return 0, errUser("interval %s must be a 32-bit integer", field)
	}
	return f, nil
}

// intervalMap normalizes an IntervalInput value to its six numeric fields.
func intervalMap(v any) (map[string]any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errUser("interval must be an object")
	}
	out := make(map[string]any, len(intervalMakeArgs))
	for _, a := range intervalMakeArgs {
		integral := a.field != "seconds"
		f, err := intervalNumber(m, a.field, integral)
		if err != nil {
			return nil, err
		}
		if integral {
			out[a.field] = int64(f)
		} else {
			out[a.field] = f
		}
	}
	return out, nil
}

// intervalScalarSQL renders one interval input value: make_interval over
// six bound parameters. A string (a decoded nodeId key, which PostgreSQL
// renders as ISO 8601) is cast instead.
func (s *stmt) intervalScalarSQL(v any) (string, error) {
	if str, ok := v.(string); ok {
		return s.param(str) + "::text::interval", nil
	}
	m, err := intervalMap(v)
	if err != nil {
		return "", err
	}
	args := make([]string, len(intervalMakeArgs))
	for i, a := range intervalMakeArgs {
		args[i] = fmt.Sprintf("%s => %s::%s", a.arg, s.param(m[a.field]), a.cast)
	}
	return "make_interval(" + strings.Join(args, ", ") + ")", nil
}

// intervalArraySQL renders a list of interval input values as an interval[]
// built from one jsonb parameter, keeping the SQL independent of the list
// length.
func (s *stmt) intervalArraySQL(v any) (string, error) {
	list, ok := v.([]any)
	if !ok {
		return "", errUser("interval list must be a list")
	}
	items := make([]any, len(list))
	for i, item := range list {
		if item == nil {
			continue
		}
		m, err := intervalMap(item)
		if err != nil {
			return "", err
		}
		items[i] = m
	}
	e := s.alias("e")
	args := make([]string, len(intervalMakeArgs))
	for i, a := range intervalMakeArgs {
		args[i] = fmt.Sprintf("%s => (%s.v->>'%s')::%s", a.arg, e, a.field, a.cast)
	}
	return fmt.Sprintf("coalesce((SELECT array_agg(make_interval(%s) ORDER BY %s.o) FROM jsonb_array_elements(%s::jsonb) WITH ORDINALITY AS %s(v, o)), ARRAY[]::interval[])",
		strings.Join(args, ", "), e, s.param(toJSONParam(items)), e), nil
}

// intervalValue renders an input value of an interval column: a scalar or a
// list.
func (s *stmt) intervalValue(ci contribtype.Column, v any) (string, error) {
	if ci.Array {
		return s.intervalArraySQL(v)
	}
	return s.intervalScalarSQL(v)
}

// intervalOp compiles a filter operator of an interval column.
func (s *stmt) intervalOp(ci contribtype.Column, ref, op string, val any) (string, error) {
	if sym, ok := comparisonOps[op]; ok {
		v, err := s.intervalValue(ci, val)
		return ref + " " + sym + " " + v, err
	}
	switch op {
	case "isNull":
		if b, _ := val.(bool); b {
			return ref + " IS NULL", nil
		}
		return ref + " IS NOT NULL", nil
	case "in", "notIn":
		arr, err := s.intervalArraySQL(val)
		if err != nil {
			return "", err
		}
		if op == "notIn" {
			return ref + " <> ALL(" + arr + ")", nil
		}
		return ref + " = ANY(" + arr + ")", nil
	case "contains", "containedBy", "overlaps":
		if !ci.Array {
			break
		}
		arr, err := s.intervalArraySQL(val)
		if err != nil {
			return "", err
		}
		sym := map[string]string{"contains": "@>", "containedBy": "<@", "overlaps": "&&"}[op]
		return ref + " " + sym + " " + arr, nil
	}
	return "", errUser("unknown filter operator %q", op)
}

// intervalObject renders the selection of an Interval over a non-null
// interval reference, decomposed field by field as PostgreSQL stores it
// (no justification between units).
func (s *stmt) intervalObject(ref string, sel ast.SelectionSet) string {
	var pairs []string
	for _, f := range s.expandFor(sel, contribtype.IntervalType) {
		key := quoteLiteral(f.Alias)
		if f.Name == "__typename" {
			pairs = append(pairs, key, quoteLiteral(contribtype.IntervalType)+"::text")
			continue
		}
		if e, ok := intervalExtract[f.Name]; ok {
			pairs = append(pairs, key, fmt.Sprintf("extract(%s FROM %s)::%s", e.part, ref, e.cast))
		}
	}
	return buildJSONObject(pairs)
}

// intervalJSON renders an interval or interval[] value as Interval objects;
// the reference is evaluated once.
func (s *stmt) intervalJSON(ci contribtype.Column, ref string, sel ast.SelectionSet) string {
	v := s.alias("iv")
	if !ci.Array {
		return fmt.Sprintf("(SELECT %s FROM (SELECT %s AS i) AS %s WHERE %s.i IS NOT NULL)",
			s.intervalObject(v+".i", sel), ref, v, v)
	}
	e := s.alias("e")
	return fmt.Sprintf("(SELECT CASE WHEN %s.a IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN %s.v IS NULL THEN NULL ELSE %s END ORDER BY %s.o), '[]'::json) FROM unnest(%s.a) WITH ORDINALITY AS %s(v, o)) END FROM (SELECT %s AS a) AS %s)",
		v, e, s.intervalObject(e+".v", sel), e, v, e, ref, v)
}

// intervalLiteral renders an interval input value as the text PostgreSQL's
// interval input function parses (jsonb_populate_record reads composite
// attributes from JSON strings); a list becomes an array literal.
func intervalLiteral(ci contribtype.Column, v any) (any, error) {
	if str, ok := v.(string); ok {
		return str, nil
	}
	one := func(item any) (string, error) {
		if str, ok := item.(string); ok {
			return str, nil
		}
		m, err := intervalMap(item)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%d years %d months %d days %d hours %d minutes %s seconds",
			m["years"], m["months"], m["days"], m["hours"], m["minutes"],
			strconv.FormatFloat(m["seconds"].(float64), 'f', -1, 64)), nil
	}
	if !ci.Array {
		return one(v)
	}
	list, ok := v.([]any)
	if !ok {
		return nil, errUser("interval list must be a list")
	}
	parts := make([]string, len(list))
	for i, item := range list {
		if item == nil {
			parts[i] = "NULL"
			continue
		}
		lit, err := one(item)
		if err != nil {
			return nil, err
		}
		parts[i] = quoteLiteralValue(lit)
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}
