package compile

import (
	"strconv"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/introspect"
)

// windowDirective marks a field copy made by StreamWindow. The name cannot
// come from a client: validation rejects directives the schema does not
// declare, and names starting with __ are reserved.
const windowDirective = "__pdbqStreamWindow"

// streamWindow is the slice of a streamed list one statement returns.
type streamWindow struct {
	id, start, count int
}

// StreamWindow returns a copy of f, a list field, that compiles to the items
// of its list at positions start to start+count-1 (0-based, in the list's
// order) instead of the whole list. The compiler applies the window only to
// the nodes and edges lists of a forward connection over a table with a
// primary key, the lists it can page through in a stable order; it reports
// id in Statement.Streamed when it did. Otherwise the field compiles to its
// whole list.
func StreamWindow(f *ast.Field, id, start, count int) *ast.Field {
	c := *f
	c.Directives = append(ast.DirectiveList{{
		Name: windowDirective,
		Arguments: ast.ArgumentList{
			{Name: "id", Value: intValue(id)},
			{Name: "start", Value: intValue(start)},
			{Name: "count", Value: intValue(count)},
		},
	}}, f.Directives...)
	return &c
}

func intValue(n int) *ast.Value {
	return &ast.Value{Kind: ast.IntValue, Raw: strconv.Itoa(n)}
}

// windowOf returns the window StreamWindow put on f.
func windowOf(f *ast.Field) (streamWindow, bool) {
	d := f.Directives.ForName(windowDirective)
	if d == nil {
		return streamWindow{}, false
	}
	arg := func(name string) int {
		a := d.Arguments.ForName(name)
		if a == nil {
			return 0
		}
		n, _ := strconv.Atoi(a.Value.Raw)
		return n
	}
	return streamWindow{id: arg("id"), start: arg("start"), count: arg("count")}, true
}

// applyWindow narrows a forward page to w: the rows at positions w.start to
// w.start+w.count-1 of the page.
func (p *pagePlan) applyWindow(w streamWindow) {
	p.offset += w.start
	p.hasOffset = true
	p.limit = max(0, min(w.count, p.limit-w.start))
}

// windowConnection applies the StreamWindow of a nodes or edges field among
// fields, the merged selections of the connection field f over t. When
// every other selection is __typename, the window narrows plan, the page the
// connection scans. Otherwise the windowed list compiles to a subquery of its
// own, returned keyed by its field, so the connection's other selections
// (pageInfo, totalCount, aggregates, an unwindowed list) still see the whole
// page. A backward page or a table without a primary key keeps the whole
// list.
func (s *stmt) windowConnection(t *introspect.Table, f *ast.Field, fields []*ast.Field, plan *pagePlan, extraCond string, depth int) (map[*ast.Field]string, error) {
	if plan.reversed || !plan.emitKeyset {
		return nil, nil
	}
	for _, sub := range fields {
		if sub.Name != "nodes" && sub.Name != "edges" {
			continue
		}
		w, ok := windowOf(sub)
		if !ok {
			continue
		}
		if onlyTypename(fields, sub) {
			plan.applyWindow(w)
			s.streamed = append(s.streamed, w.id)
			return nil, nil
		}
		c := *f
		c.SelectionSet = ast.SelectionSet{sub}
		body, err := s.connectionJSON(t, &c, extraCond, depth)
		if err != nil {
			return nil, err
		}
		return map[*ast.Field]string{sub: "(" + body + ")->" + quoteLiteral(sub.Alias)}, nil
	}
	return nil, nil
}

// onlyTypename reports whether every field but keep is __typename.
func onlyTypename(fields []*ast.Field, keep *ast.Field) bool {
	for _, f := range fields {
		if f != keep && f.Name != "__typename" {
			return false
		}
	}
	return true
}
