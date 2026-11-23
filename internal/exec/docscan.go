package exec

import (
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Defaults of the document limits, used when the corresponding Options field
// is 0.
const (
	DefaultMaxQueryTokens        = 15000
	DefaultMaxInputDepth         = 32
	DefaultMaxSelections         = 10000
	DefaultMaxIntrospectionBytes = 16 << 20
	DefaultMaxFieldComparisons   = 100000
)

// docLimits bounds what a document may cost before it is validated.
type docLimits struct {
	inputDepth  int
	selections  int
	comparisons int
}

func (e *Executor) docLimits() docLimits {
	return docLimits{
		inputDepth:  orDefault(e.Opts.MaxInputDepth, DefaultMaxInputDepth),
		selections:  orDefault(e.Opts.MaxSelections, DefaultMaxSelections),
		comparisons: orDefault(e.Opts.MaxFieldComparisons, DefaultMaxFieldComparisons),
	}
}

func orDefault(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

// scanDocument checks a parsed document against lim before validation and
// returns the estimated heap its parsed and validated AST retains. It
// rejects input value literals nested deeper than lim.inputDepth, documents
// whose
// selections, with every fragment spread expanded, number more than
// lim.selections, and documents in which the OverlappingFieldsCanBeMerged
// rule would compare more than lim.comparisons field pairs. Its cost is
// linear in the document and bounded by lim.selections.
func scanDocument(doc *ast.QueryDocument, lim docLimits) (int, error) {
	s := docScanner{lim: lim}
	for _, op := range doc.Operations {
		for _, vd := range op.VariableDefinitions {
			s.bytes += astNodeBytes
			s.value(vd.DefaultValue, 0)
			s.directives(vd.Directives)
		}
		s.directives(op.Directives)
		s.selections(op.SelectionSet)
	}
	for _, f := range doc.Fragments {
		s.bytes += astNodeBytes
		s.directives(f.Directives)
		s.selections(f.SelectionSet)
	}
	if s.err != nil {
		return 0, s.err
	}

	x := expander{
		frags:     make(map[string]*ast.FragmentDefinition, len(doc.Fragments)),
		used:      map[string]bool{},
		active:    map[string]bool{},
		groups:    map[groupKey]int32{},
		remaining: lim.selections,
		maxSel:    lim.selections,
		maxPairs:  lim.comparisons,
	}
	for _, f := range doc.Fragments {
		if _, dup := x.frags[f.Name]; !dup {
			x.frags[f.Name] = f
		}
	}
	for _, op := range doc.Operations {
		if !x.walk(op.SelectionSet, x.root()) {
			return 0, x.err()
		}
	}
	for _, f := range doc.Fragments {
		if !x.used[f.Name] && !x.walk(f.SelectionSet, x.root()) {
			return 0, x.err()
		}
	}
	return s.bytes, nil
}

// Estimated heap a parsed and validated document retains per field (the
// validator gives every __typename selection a definition of its own) and
// per other AST node (argument, value, directive, ...), positions included.
const (
	astFieldBytes = 512
	astNodeBytes  = 256
)

// docScanner walks the document as written (fragments not expanded),
// estimating its size and measuring input value nesting.
type docScanner struct {
	lim   docLimits
	bytes int
	err   error
}

func (s *docScanner) selections(sel ast.SelectionSet) {
	for _, item := range sel {
		if s.err != nil {
			return
		}
		switch v := item.(type) {
		case *ast.Field:
			s.bytes += astFieldBytes
			for _, a := range v.Arguments {
				s.bytes += astNodeBytes
				s.value(a.Value, 0)
			}
			s.directives(v.Directives)
			s.selections(v.SelectionSet)
		case *ast.InlineFragment:
			s.bytes += astNodeBytes
			s.directives(v.Directives)
			s.selections(v.SelectionSet)
		case *ast.FragmentSpread:
			s.bytes += astNodeBytes
			s.directives(v.Directives)
		}
	}
}

func (s *docScanner) directives(list ast.DirectiveList) {
	for _, d := range list {
		s.bytes += astNodeBytes
		for _, a := range d.Arguments {
			s.bytes += astNodeBytes
			s.value(a.Value, 0)
		}
	}
}

func (s *docScanner) value(v *ast.Value, depth int) {
	if v == nil || s.err != nil {
		return
	}
	s.bytes += astNodeBytes
	if v.Kind != ast.ListValue && v.Kind != ast.ObjectValue {
		return
	}
	depth++
	if depth > s.lim.inputDepth {
		s.err = gqlerror.ErrorPosf(v.Position, "input value nesting exceeds limit %d", s.lim.inputDepth)
		return
	}
	for _, c := range v.Children {
		s.value(c.Value, depth)
	}
}

// groupKey identifies the fields that OverlappingFieldsCanBeMerged compares
// with each other: those sharing a response key under the same group of
// parent fields (or the same root).
type groupKey struct {
	parent int32
	key    string
}

// expander walks a document with its fragment spreads expanded, as the
// executor and compiler resolve them, within a selection budget. It counts
// the fields of each group: validation compares every pair of fields of one
// group (and no other pairs), so the pair count bounds that rule's work even
// when fragments repeat it for every place they are spread.
type expander struct {
	frags map[string]*ast.FragmentDefinition
	used  map[string]bool
	// active holds the fragments being expanded; a spread of one of them is
	// a cycle, which validation reports, and is not followed.
	active    map[string]bool
	groups    map[groupKey]int32
	counts    []int32
	pairs     int
	remaining int
	maxSel    int
	maxPairs  int
	overSel   bool
}

func (x *expander) root() int32 {
	x.counts = append(x.counts, 0)
	return int32(len(x.counts) - 1)
}

func (x *expander) walk(sel ast.SelectionSet, parent int32) bool {
	for _, item := range sel {
		x.remaining--
		if x.remaining < 0 {
			x.overSel = true
			return false
		}
		switch v := item.(type) {
		case *ast.Field:
			k := groupKey{parent: parent, key: v.Alias}
			if k.key == "" {
				k.key = v.Name
			}
			g, ok := x.groups[k]
			if !ok {
				g = x.root()
				x.groups[k] = g
			}
			x.pairs += int(x.counts[g])
			x.counts[g]++
			if x.pairs > x.maxPairs {
				return false
			}
			if !x.walk(v.SelectionSet, g) {
				return false
			}
		case *ast.InlineFragment:
			if !x.walk(v.SelectionSet, parent) {
				return false
			}
		case *ast.FragmentSpread:
			x.used[v.Name] = true
			def := x.frags[v.Name]
			if def == nil || x.active[v.Name] {
				continue
			}
			x.active[v.Name] = true
			ok := x.walk(def.SelectionSet, parent)
			delete(x.active, v.Name)
			if !ok {
				return false
			}
		}
	}
	return true
}

func (x *expander) err() error {
	if x.overSel {
		return gqlerror.Errorf("document exceeds %d selections with its fragments expanded", x.maxSel)
	}
	return gqlerror.Errorf("document selects the same response keys too often: more than %d field comparisons", x.maxPairs)
}

// checkVariableDepth rejects variable values whose list and input object
// nesting, following the variables' declared types, exceeds the input depth
// limit. Values of scalar types (JSON included) are not looked into.
func (e *Executor) checkVariableDepth(op *ast.OperationDefinition, vars map[string]any) error {
	limit := orDefault(e.Opts.MaxInputDepth, DefaultMaxInputDepth)
	for _, vd := range op.VariableDefinitions {
		v, ok := vars[vd.Variable]
		if !ok {
			continue
		}
		if inputDepthExceeds(e.Built.Schema, vd.Type, v, 0, limit) {
			return gqlerror.ErrorPathf(ast.Path{ast.PathName("variable"), ast.PathName(vd.Variable)}, "input value nesting exceeds limit %d", limit)
		}
	}
	return nil
}

// inputDepthExceeds reports whether v, a value of type t at nesting depth,
// nests lists and input objects more than limit levels deep.
func inputDepthExceeds(schema *ast.Schema, t *ast.Type, v any, depth, limit int) bool {
	if v == nil || t == nil {
		return false
	}
	if t.Elem != nil {
		depth++
		if depth > limit {
			return true
		}
		items, ok := v.([]any)
		if !ok {
			return inputDepthExceeds(schema, t.Elem, v, depth, limit)
		}
		for _, item := range items {
			if inputDepthExceeds(schema, t.Elem, item, depth, limit) {
				return true
			}
		}
		return false
	}
	def := schema.Types[t.NamedType]
	m, ok := v.(map[string]any)
	if def == nil || def.Kind != ast.InputObject || !ok {
		return false
	}
	depth++
	if depth > limit {
		return true
	}
	for k, fv := range m {
		if fd := def.Fields.ForName(k); fd != nil && inputDepthExceeds(schema, fd.Type, fv, depth, limit) {
			return true
		}
	}
	return false
}
