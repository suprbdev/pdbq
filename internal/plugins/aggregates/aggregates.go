// Package aggregates is a built-in plugin adding `@graphile/pg-aggregates`
// style aggregates to connections:
//
//   - aggregates: every <Type>Connection gains an aggregates field of type
//     <Type>Aggregates, whose groups (sum, distinctCount, min, max, average,
//     stddevSample, stddevPopulation, varianceSample, variancePopulation)
//     hold one field per suitable column (<Type>SumAggregates { id: BigInt! }).
//     The aggregates run over the connection's filtered row set (filter,
//     condition, parent relation) and ignore pagination, like totalCount.
//   - grouped aggregates: groupedAggregates(groupBy: [<TYPE>_GROUP_BY!]!,
//     having: <Type>HavingInput) returns one <Type>Aggregates object per
//     group, its keys field holding the stringified group keys. Group-by
//     values are the table's orderable, non-unique columns, plus
//     _TRUNCATED_TO_HOUR / _TRUNCATED_TO_DAY derivatives of timestamps.
//   - relational aggregate ordering: <REL>_COUNT_ASC/_DESC and
//     <REL>_<SPEC>_<COLUMN>_ASC/_DESC orderBy values sort a table by
//     aggregates over a reverse relation (POSTS_BY_AUTHOR_ID_SUM_ID_DESC).
//   - relational aggregate filtering: the advanced_filters
//     <Type>ToManyFilter wrapper gains an aggregates field
//     ({postsByAuthorId: {aggregates: {filter: {...}, sum: {id: {greaterThan:
//     "5"}}}}}), so it needs advanced_filters with relations enabled.
//   - spatial aggregates (a pdbq extension, prefixed st to stay clear of
//     upstream names): stExtent, stUnion, stCollect and stCentroid groups
//     over geometry/geography columns, returned as GeoJSON.
//
// The plugin is opt-in like upstream: with the default
// aggregates.enabled: false it adds nothing unless a table
// opts in with the @aggregates smart tag, so the schema stays byte-identical.
// enabled: true turns aggregates on for every table, and @aggregates off,
// @omit aggregates or @behavior -aggregates opts a table out again. Like the
// rest of the plugins it only adds schema surface plus bindings; the core
// compiler renders every aggregate inside the root field's single statement.
//
// Relational ordering and filtering follow advanced_filters: they aggregate
// only the related table's orderable (respectively filterable) columns, so
// filters.indexed_only and column @omit order / @omit filter apply, and the
// foreign key's @omit order, @omit many (on the key or the referencing
// table) and the relations_opt_in mode gate the ordering values. Columns
// tagged @omit read are never aggregated.
package aggregates

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/omit"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the aggregate surface.
type Plugin struct {
	enabled        bool
	grouped        bool
	order          bool
	filters        bool
	spatial        bool
	unionCollect   bool
	scanCost       int
	relationsOptIn bool
}

// Name is the plugin's name in plugins.disabled.
const Name = "aggregates"

// Settings is the top-level aggregates config section.
type Settings struct {
	Enabled             bool `koanf:"enabled" doc:"Aggregates for every table; otherwise only for tables tagged @aggregates."`
	Grouped             bool `koanf:"grouped" doc:"groupedAggregates, <Type>GroupBy and <Type>HavingInput."`
	Order               bool `koanf:"order" doc:"<REL>_COUNT_* and <REL>_<AGGREGATE>_<COLUMN>_* orderBy values."`
	Filters             bool `koanf:"filters" doc:"The aggregates field of to-many relation filters."`
	Spatial             bool `koanf:"spatial" doc:"The stExtent and stCentroid spatial aggregates."`
	SpatialUnionCollect bool `koanf:"spatial_union_collect" doc:"The stUnion and stCollect spatial aggregates, whose results grow with the row set."`
	ScanCost            int  `koanf:"scan_cost" doc:"Cost-estimate surcharge per aggregate field, aggregate orderBy value and relation-aggregate comparison."`
}

// DefaultSettings returns the documented defaults: off unless a table opts
// in, every part on once enabled except stUnion/stCollect, scan cost 10.
func DefaultSettings() Settings {
	return Settings{Grouped: true, Order: true, Filters: true, Spatial: true, ScanCost: 10}
}

// Validate returns the problems of s, keyed by their config path.
func (s Settings) Validate() []string {
	if s.ScanCost < 0 {
		return []string{"aggregates.scan_cost must be >= 0"}
	}
	return nil
}

// New builds the plugin from its settings. relationsOptIn is
// plugins.settings.advanced_filters.relations_opt_in, which the relational
// aggregate ordering honours.
func New(s Settings, relationsOptIn bool) *Plugin {
	return &Plugin{
		enabled:        s.Enabled,
		grouped:        s.Grouped,
		order:          s.Order,
		filters:        s.Filters,
		spatial:        s.Spatial,
		unionCollect:   s.SpatialUnionCollect,
		scanCost:       s.ScanCost,
		relationsOptIn: relationsOptIn,
	}
}

// Name returns the plugin's config key.
func (p *Plugin) Name() string { return Name }

// Priority 180 runs the plugin after advanced_filters (150), whose to-many
// filter wrappers it extends, and after the postgis and contrib plugins.
func (p *Plugin) Priority() int { return 180 }

// build is the state of one TransformSchema run.
type build struct {
	*Plugin
	b *schema.Builder
	// owned records the shared type names (Having<X>Filter) this run
	// created (true) or found taken by another type (false).
	owned map[string]bool
}

// TransformSchema adds the aggregate surface for every opted-in table.
func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	r := &build{Plugin: p, b: b, owned: map[string]bool{}}
	surfaces := map[*introspect.Table]*surface{}
	for _, t := range b.Catalog.Tables {
		beh := p.tableBehavior(t)
		if !beh.aggregates {
			continue
		}
		if s := r.addTable(t, beh); s != nil {
			surfaces[t] = s
		}
	}
	if len(surfaces) == 0 {
		return nil
	}
	for _, t := range b.Catalog.Tables {
		r.addRelationOrder(t, surfaces)
	}
	for _, t := range b.Catalog.Tables {
		if s := surfaces[t]; s != nil && s.beh.filterBy {
			r.addRelationFilter(t, s)
		}
	}
	return nil
}

// behavior is a table's resolved aggregate surface.
type behavior struct {
	aggregates bool
	grouped    bool
	filterBy   bool
	orderBy    bool
}

// tableBehavior resolves the settings and the table's smart tags: @aggregates
// (or @aggregates on) opts in, @aggregates off and @omit aggregates opt out,
// and @behavior tokens toggle the parts (±aggregates, ±groupedAggregates,
// ±aggregates:filterBy, ±aggregates:orderBy).
func (p *Plugin) tableBehavior(t *introspect.Table) behavior {
	tags, _ := smarttags.Parse(t.Comment)
	on := p.enabled
	if tags.Has("aggregates") {
		switch strings.ToLower(tags.First("aggregates")) {
		case "", "on":
			on = true
		case "off":
			on = false
		}
	}
	if omits, _ := tags.Omits(); omits["aggregates"] {
		on = false
	}
	beh := behavior{aggregates: on, grouped: on, filterBy: on, orderBy: on}
	for _, v := range tags.All("behavior") {
		for _, tok := range smarttags.SplitList(v) {
			plus := !strings.HasPrefix(tok, "-")
			switch strings.TrimLeft(tok, "+-") {
			case "aggregates":
				beh = behavior{aggregates: plus, grouped: plus, filterBy: plus, orderBy: plus}
			case "groupedAggregates", "resource:groupedAggregates":
				beh.grouped = plus
			case "aggregates:filterBy", "resource:aggregates:filterBy":
				beh.filterBy = plus
			case "aggregates:orderBy", "manyRelation:aggregates:orderBy":
				beh.orderBy = plus
			}
		}
	}
	beh.grouped = beh.grouped && beh.aggregates && p.grouped
	beh.filterBy = beh.filterBy && beh.aggregates && p.filters
	beh.orderBy = beh.orderBy && beh.aggregates && p.order
	return beh
}

// columnTags reports whether a column is excluded from aggregates (@omit
// aggregates or read, @aggregates off, @behavior -aggregate) and from
// grouping (additionally @behavior -groupBy).
func columnTags(c *introspect.Column) (omitted, noGroup bool) {
	tags, _ := smarttags.Parse(c.Comment)
	omitted = omit.Column(c, omit.Aggregates) ||
		strings.EqualFold(tags.First("aggregates"), "off")
	for _, v := range tags.All("behavior") {
		for _, tok := range smarttags.SplitList(v) {
			switch tok {
			case "-aggregate", "-aggregates", "-attribute:aggregate":
				omitted = true
			case "-groupBy", "-attribute:groupBy":
				noGroup = true
			}
		}
	}
	return omitted, omitted || noGroup
}

// surface records what addTable generated for a table.
type surface struct {
	beh      behavior
	typeName string
	// cols maps the aggregatable columns to their GraphQL field names.
	cols  map[*introspect.Column]string
	order []*introspect.Column
}

// equality reports whether c's (element) type has a default btree equality:
// an allowlisted type or an enum.
func (r *build) equality(c *introspect.Column) bool {
	if hasBtree(c) || c.EnumRef != "" {
		return true
	}
	return r.b.Catalog.Enum(c.TypeSchema, strings.TrimPrefix(c.PGType, "_")) != nil
}

// bind is sp.bind with the column's equality and the scan cost applied.
func (r *build) bind(sp spec, c *introspect.Column) *schema.Aggregate {
	return sp.bind(c, r.equality(c), r.scanCost)
}

// specEnabled reports whether the settings expose sp at all.
func (r *build) specEnabled(sp spec) bool {
	if sp.bulky {
		return r.spatial && r.unionCollect
	}
	return !sp.spatial || r.spatial
}

// addTable adds <Type>Aggregates and its groups, the aggregates field and
// (grouped) groupedAggregates with its group-by enum and having input.
func (r *build) addTable(t *introspect.Table, beh behavior) *surface {
	b := r.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	obj, conn := b.Objects[typeName], b.Objects[typeName+"Connection"]
	if obj == nil || conn == nil {
		return nil
	}
	s := &surface{beh: beh, typeName: typeName, cols: map[*introspect.Column]string{}}
	for _, c := range t.Columns {
		if omitted, _ := columnTags(c); omitted {
			continue
		}
		if _, composite := b.TypeForComposite[c.TypeSchema+"."+strings.TrimPrefix(c.PGType, "_")]; composite {
			continue
		}
		if name := columnField(obj, c); name != "" {
			s.cols[c] = name
			s.order = append(s.order, c)
		}
	}
	aggType := typeName + "Aggregates"
	if !free(b, aggType) {
		warn(b, "aggregates: type name taken, skipping table", t, aggType)
		return nil
	}
	if conn.Field("aggregates") != nil {
		warn(b, "aggregates: connection field taken, skipping table", t, "aggregates")
		return nil
	}
	container := &schema.Object{
		Name:        aggType,
		Description: "Aggregates over a set of `" + typeName + "` rows.",
		Fields: []*schema.Field{{
			Name:        "keys",
			Type:        "[String]",
			Description: "The stringified groupBy keys of this group (groupedAggregates only).",
			Meta:        &schema.FieldMeta{Kind: schema.KindAggregateKeys, Table: t},
		}},
	}
	for _, sp := range specs {
		if !r.specEnabled(sp) {
			continue
		}
		groupType := typeName + coreinflect.UpperCamel(sp.id) + "Aggregates"
		group := &schema.Object{Name: groupType}
		for _, c := range s.order {
			a := r.bind(sp, c)
			if a == nil {
				continue
			}
			typ := b.GraphQLTypeFor(a.ResultColumn())
			if sp.nonNull {
				typ += "!"
			}
			meta := &schema.FieldMeta{Kind: schema.KindAggregateValue, Table: t, Column: c, Aggregate: a}
			if sp.bulky {
				meta.ScanCost = r.scanCost * bulkyCostFactor
			}
			group.Fields = append(group.Fields, &schema.Field{
				Name:        s.cols[c],
				Type:        typ,
				Description: sp.label + " of " + s.cols[c] + " across the matching connection.",
				Meta:        meta,
			})
		}
		if len(group.Fields) == 0 || !free(b, groupType) {
			continue
		}
		b.Objects[groupType] = group
		container.Fields = append(container.Fields, &schema.Field{
			Name:        sp.id,
			Type:        groupType,
			Description: sp.label + " aggregates across the matching connection (ignoring before/after/first/last/offset).",
			Meta:        &schema.FieldMeta{Kind: schema.KindAggregateGroup, Table: t},
		})
	}
	if len(container.Fields) == 1 {
		return nil
	}
	b.Objects[aggType] = container
	conn.AddField(&schema.Field{
		Name:        "aggregates",
		Type:        aggType,
		Description: "Aggregates across the matching connection (ignoring before/after/first/last/offset).",
		Meta:        &schema.FieldMeta{Kind: schema.KindAggregates, Table: t, ScanCost: r.scanCost},
	})
	if beh.grouped {
		r.addGrouped(t, s, conn, aggType)
	}
	return s
}

// addGrouped adds groupedAggregates when the table has a groupable column.
func (r *build) addGrouped(t *introspect.Table, s *surface, conn *schema.Object, aggType string) {
	if conn.Field("groupedAggregates") != nil {
		return
	}
	groupBy := r.addGroupByEnum(t, s)
	if groupBy == "" {
		return
	}
	args := []schema.Arg{{
		Name:        "groupBy",
		Type:        "[" + groupBy + "!]!",
		Description: "The method to use when grouping `" + s.typeName + "` for these aggregates.",
	}}
	if having := r.addHaving(t, s); having != "" {
		args = append(args, schema.Arg{
			Name:        "having",
			Type:        having,
			Description: "Conditions on the grouped aggregates.",
		})
	}
	conn.AddField(&schema.Field{
		Name:        "groupedAggregates",
		Type:        "[" + aggType + "!]",
		Description: "Grouped aggregates across the matching connection (ignoring before/after/first/last/offset).",
		Args:        args,
		Meta:        &schema.FieldMeta{Kind: schema.KindGroupedAggregates, Table: t, ScanCost: r.scanCost},
	})
}

// orderColumns returns the columns behind t's plain orderBy values, the set
// filters.indexed_only and @omit order leave orderable.
func orderColumns(b *schema.Builder, t *introspect.Table) []orderColumn {
	en := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if en == nil {
		return nil
	}
	var out []orderColumn
	for _, v := range en.Values {
		if v.Column == "" || v.Desc || v.Computed != nil || v.Relation != nil || v.Distance || v.Rank || v.Similarity != "" || v.Vector != "" || v.Contrib != nil {
			continue
		}
		base, ok := strings.CutSuffix(v.Name, "_ASC")
		if c := t.Column(v.Column); ok && c != nil {
			out = append(out, orderColumn{c, base})
		}
	}
	return out
}

// orderColumn is one orderable column and the base of its orderBy values.
type orderColumn struct {
	col  *introspect.Column
	base string
}

// filterColumns returns the columns t's <Type>Filter input filters on, the
// set filters.indexed_only and @omit filter leave filterable.
func filterColumns(b *schema.Builder, t *introspect.Table) map[*introspect.Column]bool {
	out := map[*introspect.Column]bool{}
	if in := b.Inputs[b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]; in != nil {
		for _, f := range in.Fields {
			if c := t.Column(f.Column); f.Column != "" && c != nil {
				out[c] = true
			}
		}
	}
	return out
}

// addGroupByEnum creates <Type>GroupBy from the table's plain orderBy values
// (so filters.indexed_only and @omit order apply), skipping single-column
// unique keys, whose groups would hold one row each. Timestamp derivatives
// whose name a column value already takes are skipped.
func (r *build) addGroupByEnum(t *introspect.Table, s *surface) string {
	b := r.b
	name := s.typeName + "GroupBy"
	if !free(b, name) {
		return ""
	}
	en := &schema.Enum{Name: name, Description: "Grouping methods for `" + s.typeName + "` for usage during aggregation."}
	taken := map[string]bool{}
	var stamps []orderColumn
	for _, oc := range orderColumns(b, t) {
		c := oc.col
		if s.cols[c] == "" || singleUnique(t, c.Name) || !r.equality(c) || taken[oc.base] {
			continue
		}
		if _, noGroup := columnTags(c); noGroup {
			continue
		}
		taken[oc.base] = true
		en.Values = append(en.Values, schema.EnumValue{Name: oc.base, GroupBy: &schema.GroupBy{Column: c}})
		if !c.IsArray && (c.PGType == "timestamp" || c.PGType == "timestamptz") {
			stamps = append(stamps, oc)
		}
	}
	var derived []schema.EnumValue
	for _, oc := range stamps {
		for _, unit := range []string{"hour", "day"} {
			vname := oc.base + "_TRUNCATED_TO_" + strings.ToUpper(unit)
			if taken[vname] {
				continue
			}
			taken[vname] = true
			derived = append(derived, schema.EnumValue{Name: vname, GroupBy: &schema.GroupBy{Column: oc.col, SQL: dateTrunc(unit)}})
		}
	}
	if len(en.Values) == 0 {
		return ""
	}
	en.Values = insertDerived(en.Values, derived)
	b.Enums[name] = en
	return name
}

// insertDerived places each derivative value right after its column's
// value, keeping the enum readable.
func insertDerived(values, derived []schema.EnumValue) []schema.EnumValue {
	out := make([]schema.EnumValue, 0, len(values)+len(derived))
	for _, v := range values {
		out = append(out, v)
		for _, d := range derived {
			if d.GroupBy.Column == v.GroupBy.Column {
				out = append(out, d)
			}
		}
	}
	return out
}

// addHaving creates <Type>HavingInput with one <Type>Having<Spec>Input per
// aggregate group whose results have a comparison filter, over the table's
// filterable columns; returns "" when no group qualifies.
func (r *build) addHaving(t *introspect.Table, s *surface) string {
	b := r.b
	name := s.typeName + "HavingInput"
	if !free(b, name) {
		return ""
	}
	filterable := filterColumns(b, t)
	in := &schema.Input{
		Name:        name,
		Description: "Conditions for `" + s.typeName + "` aggregates.",
		Fields: []*schema.InputField{
			{Name: "AND", Type: "[" + name + "!]"},
			{Name: "OR", Type: "[" + name + "!]"},
		},
	}
	for _, sp := range specs {
		if sp.spatial {
			continue
		}
		groupName := s.typeName + "Having" + coreinflect.UpperCamel(sp.id) + "Input"
		group := &schema.Input{Name: groupName}
		for _, c := range s.order {
			if !filterable[c] {
				continue
			}
			a := r.bind(sp, c)
			if a == nil {
				continue
			}
			filter := r.havingFilter(t, a.ResultType)
			if filter == "" {
				continue
			}
			group.Fields = append(group.Fields, &schema.InputField{Name: s.cols[c], Type: filter, Aggregate: a})
		}
		if len(group.Fields) == 0 || !free(b, groupName) {
			continue
		}
		b.Inputs[groupName] = group
		in.Fields = append(in.Fields, &schema.InputField{Name: sp.id, Type: groupName})
	}
	if len(in.Fields) == 2 {
		return ""
	}
	b.Inputs[name] = in
	return name
}

// havingScalars maps aggregate result types to the having filter family.
var havingScalars = map[string]string{
	"int2": "Int", "int4": "Int",
	"int8":   "Bigint",
	"float4": "Float", "float8": "Float",
	"numeric": "Bigfloat",
	"date":    "Datetime", "timestamp": "Datetime", "timestamptz": "Datetime",
}

// havingFilter returns (creating if needed) the shared Having<X>Filter input
// comparing an aggregate result of pgType, or "" for result types without
// one and when another type already took the name.
func (r *build) havingFilter(t *introspect.Table, pgType string) string {
	family, ok := havingScalars[pgType]
	if !ok {
		return ""
	}
	name := "Having" + family + "Filter"
	if owned, seen := r.owned[name]; seen {
		if owned {
			return name
		}
		return ""
	}
	if !free(r.b, name) {
		warn(r.b, "aggregates: having filter type name taken, skipping", t, name)
		r.owned[name] = false
		return ""
	}
	scalar := map[string]string{"Bigint": "BigInt", "Bigfloat": "BigFloat"}[family]
	if scalar == "" {
		scalar = family
	}
	in := &schema.Input{Name: name}
	for _, op := range []string{"equalTo", "notEqualTo", "greaterThan", "greaterThanOrEqualTo", "lessThan", "lessThanOrEqualTo"} {
		in.Fields = append(in.Fields, &schema.InputField{Name: op, Type: scalar})
	}
	r.b.Inputs[name] = in
	r.owned[name] = true
	return name
}

// addRelationOrder adds <REL>_COUNT_* and <REL>_<SPEC>_<COLUMN>_* values to
// t's orderBy enum for every reverse relation from an opted-in table, unless
// the foreign key opts out the way advanced_filters' relational ordering
// does (@omit order, @omit many on the key or the child table,
// relations_opt_in without @filterable / @sortable).
func (r *build) addRelationOrder(t *introspect.Table, surfaces map[*introspect.Table]*surface) {
	b := r.b
	en := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if en == nil {
		return
	}
	for _, child := range b.Catalog.Tables {
		cs := surfaces[child]
		if cs == nil || !cs.beh.orderBy {
			continue
		}
		var cols []*introspect.Column
		for _, oc := range orderColumns(b, child) {
			if cs.cols[oc.col] != "" {
				cols = append(cols, oc.col)
			}
		}
		for _, fk := range child.ForeignKeys {
			if fk.RefSchema != t.Schema || fk.RefTable != t.Name || r.omitsOrder(fk.Comment) || omit.ManyHidden(child, fk) {
				continue
			}
			relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
				Schema: child.Schema, Table: child.Name, Columns: fk.Columns, Constraint: fk.Name,
			})
			prefix := coreinflect.EnumValue(relName) + "_"
			addOrderPair(en, prefix+"COUNT", &schema.OrderRelation{FK: fk, Table: child, Aggregate: countAggregate(r.scanCost)})
			for _, sp := range specs {
				if sp.spatial {
					continue
				}
				for _, c := range cols {
					a := r.bind(sp, c)
					if a == nil {
						continue
					}
					addOrderPair(en, prefix+coreinflect.EnumValue(sp.id)+"_"+coreinflect.EnumValue(c.Name),
						&schema.OrderRelation{FK: fk, Table: child, Column: c.Name, Aggregate: a})
				}
			}
		}
	}
}

// omitsOrder applies advanced_filters' relational-ordering rules to a
// foreign key comment (@omit order, relations_opt_in without @filterable or
// @sortable).
func (r *build) omitsOrder(comment string) bool {
	tags, _ := smarttags.Parse(comment)
	optedOut := r.relationsOptIn && !tags.Has("filterable") && !tags.Has("sortable")
	return optedOut || omit.Hides(comment, omit.Order)
}

// addOrderPair appends base_ASC/base_DESC unless either name is taken.
func addOrderPair(en *schema.Enum, base string, rel *schema.OrderRelation) {
	if en.HasValue(base+"_ASC") || en.HasValue(base+"_DESC") {
		return
	}
	en.Values = append(en.Values,
		schema.EnumValue{Name: base + "_ASC", Relation: rel},
		schema.EnumValue{Name: base + "_DESC", Relation: rel, Desc: true},
	)
}

// addRelationFilter adds the aggregates field to the advanced_filters
// <Type>ToManyFilter wrapper over t's rows: <Type>AggregatesFilter holds a
// filter narrowing the aggregated rows plus one <Type><Spec>AggregateFilter
// per group, typed with the operator inputs of the aggregate results, over
// t's filterable columns. The wrapper is only reachable through the relation
// filter fields advanced_filters generated, so its per-foreign-key rules
// (@omit filter, relations_opt_in) apply.
func (r *build) addRelationFilter(t *introspect.Table, s *surface) {
	b := r.b
	wrapper := b.Inputs[s.typeName+"ToManyFilter"]
	filterType := b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	name := s.typeName + "AggregatesFilter"
	if wrapper == nil || wrapper.Field("aggregates") != nil || b.Inputs[filterType] == nil || !free(b, name) {
		return
	}
	filterable := filterColumns(b, t)
	in := &schema.Input{
		Name:        name,
		Description: "A filter to be used against aggregates of `" + s.typeName + "` object types.",
		Fields: []*schema.InputField{{
			Name:        "filter",
			Type:        filterType,
			Description: "A filter that must pass for the relevant `" + s.typeName + "` object to be included within the aggregate.",
		}},
	}
	for _, sp := range specs {
		if sp.spatial {
			continue
		}
		groupName := s.typeName + coreinflect.UpperCamel(sp.id) + "AggregateFilter"
		group := &schema.Input{Name: groupName}
		for _, c := range s.order {
			if !filterable[c] {
				continue
			}
			a := r.bind(sp, c)
			if a == nil || postgis.IsSpatial(a.ResultType) {
				continue
			}
			group.Fields = append(group.Fields, &schema.InputField{
				Name: s.cols[c], Type: b.FilterOpsInputFor(a.ResultColumn()), Aggregate: a,
			})
		}
		if len(group.Fields) == 0 || !free(b, groupName) {
			continue
		}
		b.Inputs[groupName] = group
		in.Fields = append(in.Fields, &schema.InputField{
			Name:        sp.id,
			Type:        groupName,
			Description: sp.label + " aggregate over matching `" + s.typeName + "` objects.",
		})
	}
	if len(in.Fields) == 1 {
		return
	}
	b.Inputs[name] = in
	wrapper.AddField(&schema.InputField{
		Name:        "aggregates",
		Type:        name,
		Description: "Aggregates across related `" + s.typeName + "` match the filter criteria.",
	})
}

// columnField returns the name of obj's field reading column c, or "" when
// the column has none (omitted by a smart comment).
func columnField(obj *schema.Object, c *introspect.Column) string {
	for _, f := range obj.Fields {
		if f.Meta != nil && f.Meta.Kind == schema.KindColumn && f.Meta.Column == c {
			return f.Name
		}
	}
	return ""
}

// singleUnique reports whether col alone is a primary key or unique key.
func singleUnique(t *introspect.Table, col string) bool {
	for _, con := range append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...) {
		if con != nil && len(con.Columns) == 1 && con.Columns[0] == col {
			return true
		}
	}
	return false
}

// free reports whether a type name is still unused.
func free(b *schema.Builder, name string) bool {
	if b.Objects[name] != nil || b.Inputs[name] != nil || b.Enums[name] != nil {
		return false
	}
	for _, s := range b.Scalars {
		if s == name {
			return false
		}
	}
	return true
}

func warn(b *schema.Builder, msg string, t *introspect.Table, name string) {
	logger := b.Options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(msg, "table", t.Schema+"."+t.Name, "name", name)
}

func dateTrunc(unit string) func(string) string {
	return func(ref string) string { return fmt.Sprintf("date_trunc('%s', %s)", unit, ref) }
}
