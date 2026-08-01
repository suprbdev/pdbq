package compile

import (
	"fmt"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/introspect"
)

// pagePlan is the Go-side pagination decision for one connection field.
type pagePlan struct {
	// emitKeyset: per-row cursors are nodeId-style (table has a PK).
	emitKeyset bool
	// predKeyset: after/before compile to keyset predicates. False for
	// PK-less tables and when a legacy "o:<n>" cursor was supplied.
	predKeyset bool
	// reversed: `last` pagination — scan flipped, re-reversed in the outer
	// aggregates.
	reversed   bool
	limit      int // <= maxPageSize (larger is rejected); maxPageSize is the default when neither first nor last given
	offset     int // offset arg plus any legacy cursor offset
	hasAfter   bool
	hasBefore  bool
	afterKeys  []any
	beforeKeys []any
}

// planPage resolves first/last/offset/before/after into a pagePlan.
func (s *stmt) planPage(t *introspect.Table, f *ast.Field, typeName string) (pagePlan, error) {
	p := pagePlan{limit: -1, emitKeyset: t.PrimaryKey != nil, predKeyset: t.PrimaryKey != nil}
	firstV, hasFirst := s.argValue(f, "first")
	lastV, hasLast := s.argValue(f, "last")
	if hasFirst && hasLast {
		return p, errUser("first and last cannot be combined")
	}
	if hasFirst {
		if n, ok := toInt(firstV); ok {
			if n < 0 {
				return p, errUser("first must be >= 0")
			}
			if int(n) > s.maxPageSize {
				return p, errPageSize("first", int(n), s.maxPageSize)
			}
			p.limit = int(n)
		}
	}
	if hasLast {
		if n, ok := toInt(lastV); ok {
			if n < 0 {
				return p, errUser("last must be >= 0")
			}
			if int(n) > s.maxPageSize {
				return p, errPageSize("last", int(n), s.maxPageSize)
			}
			p.limit = int(n)
			p.reversed = true
		}
	}
	// Absent first/last defaults to the cap rather than an unbounded scan.
	if p.limit < 0 {
		p.limit = s.maxPageSize
	}
	if v, ok := s.argValue(f, "offset"); ok {
		if n, ok := toInt(v); ok {
			if n < 0 {
				return p, errUser("offset must be >= 0")
			}
			p.offset = int(n)
		}
	}
	decode := func(arg string) (*cursorVal, error) {
		v, ok := s.argValue(f, arg)
		if !ok {
			return nil, nil
		}
		cur, _ := v.(string)
		cv, err := decodeCursorVal(cur)
		if err != nil {
			return nil, err
		}
		if cv.keyset {
			if t.PrimaryKey == nil {
				return nil, errUser("keyset cursor on a table without a primary key")
			}
			if cv.typeName != typeName {
				return nil, errUser("cursor for wrong type")
			}
		}
		return &cv, nil
	}
	after, err := decode("after")
	if err != nil {
		return p, err
	}
	if after != nil {
		p.hasAfter = true
		if after.keyset {
			p.afterKeys = after.keys
		} else {
			// Legacy offset cursor: fold into OFFSET, keep offset-mode math.
			p.offset += after.offset
			p.predKeyset = false
		}
	}
	before, err := decode("before")
	if err != nil {
		return p, err
	}
	if before != nil {
		if !before.keyset {
			return p, errUser("before requires a keyset cursor")
		}
		p.hasBefore = true
		p.beforeKeys = before.keys
	}
	if p.reversed || p.hasBefore {
		if !p.predKeyset {
			return p, errUser("backward pagination requires a primary key")
		}
		if p.offset > 0 {
			return p, errUser("offset cannot be combined with last or before")
		}
	}
	return p, nil
}

// anchorVals renders one SQL value expression per order term for the cursor
// row identified by keys. Terms on primary-key columns take the decoded
// cursor value as a plain parameter — the planner can use it as a btree
// start condition, and no join exists to break the ordered path. Other terms
// become uncorrelated scalar subqueries (InitPlans) fetching the anchor
// row's value; a deleted cursor row makes them yield NULL, so the predicate
// matches nothing and the page comes back empty.
func (s *stmt) anchorVals(t *introspect.Table, terms []orderTerm, keys []any) ([]string, error) {
	pk := t.PrimaryKey.Columns
	if len(pk) != len(keys) {
		return nil, errUser("cursor has %d key values, want %d", len(keys), len(pk))
	}
	vals := make([]string, len(terms))
	subWhere := "" // shared across subqueries: the params are allocated once
	for i, term := range terms {
		if term.fn == nil && term.rel == nil {
			if j := slices.Index(pk, term.col); j >= 0 {
				vals[i] = s.param(s.keyParam(t.Column(term.col), keys[j]))
				continue
			}
		}
		if subWhere == "" {
			conds, err := s.keyCondsFromValues(t, pk, keys, "__a")
			if err != nil {
				return nil, err
			}
			subWhere = strings.Join(conds, " AND ")
		}
		vals[i] = fmt.Sprintf("(SELECT %s FROM %s AS __a WHERE %s)",
			s.orderExprSQL(t, term, "__a"), s.sourceRef(t), subWhere)
	}
	return vals, nil
}

// keysetPredicate renders the expanded lexicographic "strictly after the
// anchor row" comparison against the anchor value expressions from
// anchorVals (parallel to terms). Mixed ASC/DESC directions forbid a tuple
// compare, so terms nest as s1 OR (e1 AND (s2 OR (e2 AND ...))). NULL
// ordering matches renderOrder: NULLs sort last in both directions (ASC is
// the PostgreSQL default, DESC is emitted with NULLS LAST); flip selects the
// "strictly before" form.
func (s *stmt) keysetPredicate(alias string, vals []string, terms []orderTerm, t *introspect.Table, flip bool) string {
	pred := ""
	for i := len(terms) - 1; i >= 0; i-- {
		term := terms[i]
		desc := term.desc != flip
		col := s.orderExprSQL(t, term, alias)
		k := vals[i]
		notNull := !orderTermNullable(t, term)
		op := ">"
		if desc {
			op = "<"
		}
		var strict string
		switch {
		case notNull:
			strict = fmt.Sprintf("%s %s %s", col, op, k)
		case !flip: // NULLs sort last, so NULL is strictly after any value
			strict = fmt.Sprintf("(%s %s %s OR (%s IS NULL AND %s IS NOT NULL))", col, op, k, col, k)
		default: // strictly before: any value is strictly before NULL
			strict = fmt.Sprintf("(%s %s %s OR (%s IS NOT NULL AND %s IS NULL))", col, op, k, col, k)
		}
		if pred == "" {
			pred = strict
			continue
		}
		eq := fmt.Sprintf("%s IS NOT DISTINCT FROM %s", col, k)
		if notNull {
			eq = fmt.Sprintf("%s = %s", col, k)
		}
		pred = fmt.Sprintf("(%s OR (%s AND %s))", strict, eq, pred)
	}
	return pred
}
