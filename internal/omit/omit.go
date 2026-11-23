// Package omit is the single decision point for the omit smart comment
// (and its @behavior aliases): given the comment of a table, column,
// constraint or function, it reports which generated surface the object is
// hidden from. The schema builder, the smart-comments plugin and every
// plugin adding surface consult it instead of parsing tags themselves, so
// one rule set covers the whole API.
//
// A bare @omit hides an object from every action. The actions and the
// surface each one gates for a column are:
//
//   - [Read]: the output field and everything returning a function of the
//     value: PostGIS derived accessors, full-text rank fields, aggregates
//     and group-by keys, distance, rank and similarity orderings (whose
//     client-chosen reference turns ordering into a read), distinctOn,
//     and every key the column is part of (by-key lookups and mutations,
//     upserts, nested-mutation targets, nodeId and keyset cursors).
//   - [Filter]: filter and condition fields, distinctOn, having and
//     relation-aggregate filters.
//   - [Order]: orderBy values, relational orderBy values, distinctOn.
//   - [Create] / [Update]: the create input / update patch field,
//     including nested-mutation inputs and upsert's DO UPDATE SET list.
//
// For a column, [Read] implies [Filter], [Order] and [Aggregates]: a column
// that can be compared or ordered reveals its value to a client that
// bisects with range operators, so @omit read removes it from filters,
// conditions, orderBy, distinctOn, aggregates and every key surface. This
// deliberately differs from PostGraphile, where @omit read leaves the
// column filterable and sortable.
//
// For tables the actions are [Read] (root query fields), [All] (the
// connection query), [Many] (one-to-many relation surface), [Create],
// [Update], [Delete], [Filter] and [Order]; a bare @omit removes the table
// from the catalog. Foreign keys take [Many] and [Filter], functions
// [Filter] and [Order] (computed columns).
package omit

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
)

// Action is a surface the omit smart comment can remove an object from.
type Action string

// The omit actions.
const (
	Read       Action = "read"
	Create     Action = "create"
	Update     Action = "update"
	Delete     Action = "delete"
	Filter     Action = "filter"
	Order      Action = "order"
	Many       Action = "many"
	All        Action = "all"
	Aggregates Action = "aggregates"
)

// Hides reports whether the smart tags in comment hide their object from
// action a: a bare @omit, an @omit listing a, or the @behavior token
// mapping to a.
func Hides(comment string, a Action) bool {
	if !strings.Contains(comment, "@") {
		return false
	}
	tags, _ := smarttags.Parse(comment)
	actions, everything := tags.Omits()
	return everything || actions[string(a)]
}

// Everything reports whether comment carries a bare @omit.
func Everything(comment string) bool {
	if !strings.Contains(comment, "@") {
		return false
	}
	tags, _ := smarttags.Parse(comment)
	_, everything := tags.Omits()
	return everything
}

// Column reports whether column c is hidden from action a. A column hidden
// from [Read] is also hidden from [Filter], [Order] and [Aggregates].
func Column(c *introspect.Column, a Action) bool {
	if c == nil {
		return false
	}
	if Hides(c.Comment, a) {
		return true
	}
	switch a {
	case Filter, Order, Aggregates:
		return Hides(c.Comment, Read)
	}
	return false
}

// Distinct reports whether c is left out of the distinctOn enum: DISTINCT ON
// groups rows by equal values and orders by them, so it needs the column
// readable, filterable and orderable.
func Distinct(c *introspect.Column) bool {
	return Column(c, Filter) || Column(c, Order)
}

// HiddenKey reports whether any of t's columns named in cols is hidden from
// [Read], which rules out every surface keyed by those columns: a lookup or
// mutation by the key would be an exact-match oracle on the hidden value,
// and nodeIds and keyset cursors would encode it.
func HiddenKey(t *introspect.Table, cols []string) bool {
	for _, cn := range cols {
		if Column(t.Column(cn), Read) {
			return true
		}
	}
	return false
}

// Table reports whether table t is hidden from action a.
func Table(t *introspect.Table, a Action) bool {
	return t != nil && Hides(t.Comment, a)
}

// ManyHidden reports whether the one-to-many side of fk (relation fields,
// reverse relation filters and orderings, nested inputs and aggregates over
// the referencing rows) is hidden: @omit many on the foreign key, or on the
// referencing table child.
func ManyHidden(child *introspect.Table, fk *introspect.ForeignKey) bool {
	return Hides(fk.Comment, Many) || Table(child, Many)
}

// ReferencesHidden reports whether fk references a column of parent hidden
// from [Read]. Operations copying the referenced key into the referencing
// row (nested connect and create) would hand the hidden value to a client
// that can read the foreign key column.
func ReferencesHidden(parent *introspect.Table, fk *introspect.ForeignKey) bool {
	return parent != nil && HiddenKey(parent, fk.RefColumns)
}
