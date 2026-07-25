// Package advancedfilters is a built-in plugin extending the generated
// filter/orderBy surface with two features default generation leaves out:
//
//   - relation filters: filter a table by conditions on related rows — a
//     forward FK exposes the parent's filter directly
//     (posts: {author: {mood: {eq: HAPPY}}}), a reverse FK exposes a
//     {some|none|every} wrapper over the child's filter
//     (users: {postsByAuthorId: {some: {published: {eq: true}}}}). Both
//     compile to EXISTS subqueries inside the single statement. Each relation
//     also gets a Boolean existence variant — <rel>Exists for forward FKs
//     (posts: {authorExists: true}), <rel>Exist for reverse
//     (users: {postsByAuthorIdExist: false}) — compiling to a bare
//     (NOT) EXISTS over the join.
//   - relational ordering: one-level orderBy over related rows, named
//     REL__COLUMN_{ASC,DESC} with a double underscore separating relation and
//     column (posts: orderBy: [AUTHOR__NAME_ASC]). Forward FKs sort by the
//     referenced parent row's column via a correlated scalar subquery;
//     reverse FKs aggregate the referencing rows' column (MIN for ASC, MAX
//     for DESC). The related columns respect the filters.indexed_only policy
//     exactly like the table's own orderBy values. Toggled via
//     plugins.settings.advanced-filters.relation_order (also off when
//     relations is off).
//   - computed-column filtering and ordering: stable/immutable row-type
//     functions without extra arguments become filter fields and orderBy
//     enum values (postCount: {gt: "5"}, orderBy: [POST_COUNT_DESC]),
//     compiled as function calls over the current row.
//
// The plugin only adds schema surface plus catalog bindings; the compiler
// reads the bindings from schema.Built and stays inert when they are absent,
// so disabling the plugin (plugins.disabled: [advanced-filters]) removes the
// feature completely. The two halves toggle independently via
// plugins.settings.advanced-filters.{relations,computed}.
//
// The surface is also tunable per object through smart comments (parsed
// directly from catalog comments, so this works even when the smart-comments
// plugin is disabled):
//
//   - "@omit filter" on a foreign key skips its relation filter (both
//     directions); "@omit filter" / "@omit order" on a computed function
//     skips its filter field / orderBy values.
//   - with plugins.settings.advanced-filters.relations_opt_in (or
//     computed_opt_in) set, the default flips: only foreign keys / computed
//     functions tagged "@filterable" (alias "@sortable") get filter surface.
//   - "@fieldName" / "@foreignFieldName" renames apply automatically (they
//     flow through the shared inflection pipeline).
//
// Note: computed columns are not covered by indexes (unless a matching
// expression index exists), so computed filter/order fields bypass the
// filters.indexed_only policy by nature.
package advancedfilters

import (
	"context"
	"strings"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/smarttags"
)

type Plugin struct {
	relations     bool
	computed      bool
	relationOrder bool
	// *OptIn flip the per-object default: only @filterable-tagged foreign
	// keys / computed functions get filter surface.
	relationsOptIn bool
	computedOptIn  bool
}

// New builds the plugin from its `plugins.settings.advanced-filters` map.
func New(settings map[string]any) *Plugin {
	p := &Plugin{relations: true, computed: true, relationOrder: true}
	if v, ok := settings["relations"].(bool); ok {
		p.relations = v
	}
	if v, ok := settings["computed"].(bool); ok {
		p.computed = v
	}
	if v, ok := settings["relation_order"].(bool); ok {
		p.relationOrder = v
	}
	if v, ok := settings["relations_opt_in"].(bool); ok {
		p.relationsOptIn = v
	}
	if v, ok := settings["computed_opt_in"].(bool); ok {
		p.computedOptIn = v
	}
	return p
}

// filterOmits reports which halves of the filter surface a smart comment
// removes, honoring the opt-in mode.
func filterOmits(comment string, optIn bool) (skipFilter, skipOrder bool) {
	tags, _ := smarttags.Parse(comment)
	omits, everything := tags.Omits()
	optedOut := optIn && !tags.Has("filterable") && !tags.Has("sortable")
	skipFilter = everything || optedOut || omits["filter"]
	skipOrder = everything || optedOut || omits["order"]
	return skipFilter, skipOrder
}

func (p *Plugin) Name() string  { return "advanced-filters" }
func (p *Plugin) Priority() int { return 150 }

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	if p.relations {
		p.addRelationFilters(b)
	}
	if p.relations && p.relationOrder {
		p.addRelationOrder(b)
	}
	// Computed filter/order fields only make sense when the computed fields
	// themselves are generated (schema.functions).
	if p.computed && b.Options.Functions {
		p.addComputed(b)
	}
	return nil
}

// addRelationFilters adds relation fields to every <Type>Filter input:
// forward FKs take the parent's filter, reverse FKs a quantified wrapper
// over the child's filter. Relation names reuse the object-field inflection
// so filter fields line up with selection fields.
func (p *Plugin) addRelationFilters(b *schema.Builder) {
	for _, t := range b.Catalog.Tables {
		in := filterInput(b, t)
		if in == nil {
			continue
		}
		// Forward: FK on t -> filter by the referenced parent row.
		for _, fk := range t.ForeignKeys {
			if skip, _ := filterOmits(fk.Comment, p.relationsOptIn); skip {
				continue
			}
			parent := b.Catalog.Table(fk.RefSchema, fk.RefTable)
			if parent == nil {
				continue
			}
			parentFilter := filterInput(b, parent)
			if parentFilter == nil {
				continue
			}
			relName := b.Inflect(inflect.KindRelationForward, inflect.Input{
				Schema: t.Schema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns,
				Constraint: fk.Name,
			})
			if in.Field(relName) == nil {
				in.AddField(&schema.InputField{
					Name:        relName,
					Type:        parentFilter.Name,
					Description: "Rows whose referenced " + fk.RefTable + " row matches.",
					Relation:    &schema.FilterRelation{Forward: true, FK: fk, Table: parent, FilterType: parentFilter.Name},
				})
			}
			if existsName := relName + "Exists"; in.Field(existsName) == nil {
				in.AddField(&schema.InputField{
					Name:        existsName,
					Type:        "Boolean",
					Description: "True: the referenced " + fk.RefTable + " row exists; false: it does not.",
					Relation:    &schema.FilterRelation{Forward: true, FK: fk, Table: parent, FilterType: parentFilter.Name, Exists: true},
				})
			}
		}
		// Reverse: FKs on other tables pointing at t -> quantified child filter.
		for _, child := range b.Catalog.Tables {
			for _, fk := range child.ForeignKeys {
				if fk.RefSchema != t.Schema || fk.RefTable != t.Name {
					continue
				}
				if skip, _ := filterOmits(fk.Comment, p.relationsOptIn); skip {
					continue
				}
				childFilter := filterInput(b, child)
				if childFilter == nil {
					continue
				}
				relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
					Schema: child.Schema, Table: child.Name, Columns: fk.Columns,
					Constraint: fk.Name,
				})
				childType := b.TypeForTable[child.Schema+"."+child.Name]
				if in.Field(relName) == nil {
					in.AddField(&schema.InputField{
						Name:        relName,
						Type:        ensureToMany(b, childType, childFilter.Name),
						Description: "Quantified filter over referencing " + child.Name + " rows.",
						Relation:    &schema.FilterRelation{Forward: false, FK: fk, Table: child, FilterType: childFilter.Name},
					})
				}
				if existsName := relName + "Exist"; in.Field(existsName) == nil {
					in.AddField(&schema.InputField{
						Name:        existsName,
						Type:        "Boolean",
						Description: "True: at least one referencing " + child.Name + " row exists; false: none do.",
						Relation:    &schema.FilterRelation{Forward: false, FK: fk, Table: child, FilterType: childFilter.Name, Exists: true},
					})
				}
			}
		}
	}
}

// addRelationOrder adds one-level relational orderBy enum values,
// REL__COLUMN_{ASC,DESC} (double underscore separating the relation from the
// column): forward FKs sort by a column of the referenced parent row, reverse
// FKs by the MIN (ASC) / MAX (DESC) of a column over the referencing rows.
// The related table's columns pass through the same indexed-only policy as
// its own orderBy enum, and "@omit order" on the FK skips the values.
func (p *Plugin) addRelationOrder(b *schema.Builder) {
	for _, t := range b.Catalog.Tables {
		orderName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		en := b.Enums[orderName]
		if en == nil {
			continue
		}
		// Forward: FK on t -> sort by the referenced parent row's columns.
		for _, fk := range t.ForeignKeys {
			if _, skip := filterOmits(fk.Comment, p.relationsOptIn); skip {
				continue
			}
			parent := b.Catalog.Table(fk.RefSchema, fk.RefTable)
			if parent == nil {
				continue
			}
			relName := b.Inflect(inflect.KindRelationForward, inflect.Input{
				Schema: t.Schema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns,
				Constraint: fk.Name,
			})
			addRelationOrderValues(b, en, relName, parent, fk, true)
		}
		// Reverse: FKs on other tables pointing at t -> aggregated child columns.
		for _, child := range b.Catalog.Tables {
			for _, fk := range child.ForeignKeys {
				if fk.RefSchema != t.Schema || fk.RefTable != t.Name {
					continue
				}
				if _, skip := filterOmits(fk.Comment, p.relationsOptIn); skip {
					continue
				}
				relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
					Schema: child.Schema, Table: child.Name, Columns: fk.Columns,
					Constraint: fk.Name,
				})
				addRelationOrderValues(b, en, relName, child, fk, false)
			}
		}
	}
}

// addRelationOrderValues appends REL__COLUMN_{ASC,DESC} values for every
// orderable column of the related table.
func addRelationOrderValues(b *schema.Builder, en *schema.Enum, relName string, related *introspect.Table, fk *introspect.ForeignKey, forward bool) {
	prefix := inflect.EnumValue(relName) + "__"
	for _, c := range b.FilterableColumns(related) {
		base := prefix + inflect.EnumValue(c.Name)
		if hasEnumValue(en, base+"_ASC") || hasEnumValue(en, base+"_DESC") {
			continue
		}
		rel := &schema.OrderRelation{Forward: forward, FK: fk, Table: related, Column: c.Name}
		en.Values = append(en.Values,
			schema.EnumValue{Name: base + "_ASC", Relation: rel},
			schema.EnumValue{Name: base + "_DESC", Relation: rel, Desc: true},
		)
	}
}

// addComputed exposes eligible computed columns (single row argument,
// stable/immutable, scalar/enum non-array return) as filter fields and
// orderBy enum values on their table.
func (p *Plugin) addComputed(b *schema.Builder) {
	for _, f := range b.Catalog.Functions {
		if len(f.Args) != 1 || f.ReturnsSet || f.ReturnType == "void" ||
			f.Volatility == introspect.VolatilityVolatile ||
			strings.HasPrefix(f.ReturnType, "_") {
			continue
		}
		skipFilter, skipOrder := filterOmits(f.Comment, p.computedOptIn)
		if skipFilter && skipOrder {
			continue
		}
		t := b.Catalog.Table(f.Args[0].TypeSchema, f.Args[0].PGType)
		if t == nil {
			continue
		}
		// Row/composite returns have no operator set.
		if b.Catalog.Table(f.ReturnTypeSchema, f.ReturnType) != nil ||
			b.Catalog.Composite(f.ReturnTypeSchema, f.ReturnType) != nil {
			continue
		}
		fieldName := b.Inflect(inflect.KindComputedField, inflect.Input{
			Schema: f.Schema, Table: t.Name, Function: f.Name,
		})
		if in := filterInput(b, t); !skipFilter && in != nil && in.Field(fieldName) == nil {
			ret := &introspect.Column{Name: f.Name, PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema}
			in.AddField(&schema.InputField{
				Name:        fieldName,
				Type:        b.FilterOpsInputFor(ret),
				Description: "Filter on the computed column " + fieldName + " (not index-backed).",
				Computed:    f,
			})
		}
		orderName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		if en := b.Enums[orderName]; !skipOrder && en != nil {
			base := inflect.EnumValue(fieldName)
			if !hasEnumValue(en, base+"_ASC") && !hasEnumValue(en, base+"_DESC") {
				en.Values = append(en.Values,
					schema.EnumValue{Name: base + "_ASC", Computed: f},
					schema.EnumValue{Name: base + "_DESC", Computed: f, Desc: true},
				)
			}
		}
	}
}

// filterInput resolves a table's <Type>Filter input, or nil when the table
// has no filterable columns (no filter input was generated).
func filterInput(b *schema.Builder, t *introspect.Table) *schema.Input {
	name := b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	return b.Inputs[name]
}

// ensureToMany creates the shared <ChildType>ToManyFilter wrapper once.
func ensureToMany(b *schema.Builder, childType, childFilter string) string {
	name := childType + "ToManyFilter"
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	b.Inputs[name] = &schema.Input{
		Name:        name,
		Description: "Quantified filter over related " + childType + " rows.",
		Fields: []*schema.InputField{
			{Name: "some", Type: childFilter, Description: "At least one related row matches."},
			{Name: "none", Type: childFilter, Description: "No related row matches."},
			{Name: "every", Type: childFilter, Description: "Every related row matches."},
		},
	}
	return name
}

func hasEnumValue(e *schema.Enum, name string) bool {
	for _, v := range e.Values {
		if v.Name == name {
			return true
		}
	}
	return false
}
