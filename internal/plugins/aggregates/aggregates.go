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
//   - relational aggregate filtering: the advanced-filters
//     <Type>ToManyFilter wrapper gains an aggregates field
//     ({postsByAuthorId: {aggregates: {filter: {...}, sum: {id: {greaterThan:
//     "5"}}}}}), so it needs advanced-filters with relations enabled.
//   - spatial aggregates (a pdbq extension, prefixed st to stay clear of
//     upstream names): stExtent, stUnion, stCollect and stCentroid groups
//     over geometry/geography columns, returned as GeoJSON.
//
// The plugin is opt-in like upstream: with the default
// plugins.settings.aggregates.enabled: false it adds nothing unless a table
// opts in with the @aggregates smart tag, so the schema stays byte-identical.
// enabled: true turns aggregates on for every table, and @aggregates off,
// @omit aggregates or @behavior -aggregates opts a table out again. Like the
// rest of the plugins it only adds schema surface plus bindings; the core
// compiler renders every aggregate inside the root field's single statement.
//
// Relational ordering and filtering respect filters.indexed_only exactly
// like advanced-filters: only the related table's orderable columns are
// aggregated there.
package aggregates

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the aggregate surface.
type Plugin struct {
	enabled  bool
	grouped  bool
	order    bool
	filters  bool
	spatial  bool
	scanCost int
}

// New builds the plugin from its `plugins.settings.aggregates` map: enabled
// (default false), grouped, order, filters, spatial (default true) and
// scan_cost (default 10).
func New(settings map[string]any) *Plugin {
	return &Plugin{
		enabled:  boolSetting(settings, "enabled", false),
		grouped:  boolSetting(settings, "grouped", true),
		order:    boolSetting(settings, "order", true),
		filters:  boolSetting(settings, "filters", true),
		spatial:  boolSetting(settings, "spatial", true),
		scanCost: intSetting(settings, "scan_cost", 10),
	}
}

// Name returns the plugin's config key.
func (p *Plugin) Name() string { return "aggregates" }

// Priority 180 runs the plugin after advanced-filters (150), whose to-many
// filter wrappers it extends, and after the postgis and contrib plugins.
func (p *Plugin) Priority() int { return 180 }

// TransformSchema adds the aggregate surface for every opted-in table.
func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	surfaces := map[*introspect.Table]*surface{}
	for _, t := range b.Catalog.Tables {
		beh := p.tableBehavior(t)
		if !beh.aggregates {
			continue
		}
		if s := p.addTable(b, t, beh); s != nil {
			surfaces[t] = s
		}
	}
	if len(surfaces) == 0 {
		return nil
	}
	for _, t := range b.Catalog.Tables {
		p.addRelationOrder(b, t, surfaces)
	}
	for _, t := range b.Catalog.Tables {
		if s := surfaces[t]; s != nil && s.beh.filterBy {
			p.addRelationFilter(b, t, s)
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
// aggregates, @aggregates off, @behavior -aggregate) and from grouping
// (additionally @behavior -groupBy).
func columnTags(c *introspect.Column) (omitted, noGroup bool) {
	tags, _ := smarttags.Parse(c.Comment)
	omits, _ := tags.Omits()
	omitted = omits["aggregates"] || strings.EqualFold(tags.First("aggregates"), "off")
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

// addTable adds <Type>Aggregates and its groups, the aggregates field and
// (grouped) groupedAggregates with its group-by enum and having input.
func (p *Plugin) addTable(b *schema.Builder, t *introspect.Table, beh behavior) *surface {
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
		if sp.spatial && !p.spatial {
			continue
		}
		groupType := typeName + coreinflect.UpperCamel(sp.id) + "Aggregates"
		group := &schema.Object{Name: groupType}
		for _, c := range s.order {
			a := sp.bind(c)
			if a == nil {
				continue
			}
			typ := b.GraphQLTypeFor(a.ResultColumn())
			if sp.nonNull {
				typ += "!"
			}
			group.Fields = append(group.Fields, &schema.Field{
				Name:        s.cols[c],
				Type:        typ,
				Description: sp.label + " of " + s.cols[c] + " across the matching connection.",
				Meta:        &schema.FieldMeta{Kind: schema.KindAggregateValue, Table: t, Column: c, Aggregate: a},
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
		Meta:        &schema.FieldMeta{Kind: schema.KindAggregates, Table: t, ScanCost: p.scanCost},
	})
	if beh.grouped {
		p.addGrouped(b, t, s, conn, aggType)
	}
	return s
}

// addGrouped adds groupedAggregates when the table has a groupable column.
func (p *Plugin) addGrouped(b *schema.Builder, t *introspect.Table, s *surface, conn *schema.Object, aggType string) {
	if conn.Field("groupedAggregates") != nil {
		return
	}
	groupBy := p.addGroupByEnum(b, t, s)
	if groupBy == "" {
		return
	}
	args := []schema.Arg{{
		Name:        "groupBy",
		Type:        "[" + groupBy + "!]!",
		Description: "The method to use when grouping `" + s.typeName + "` for these aggregates.",
	}}
	if having := p.addHaving(b, t, s); having != "" {
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
		Meta:        &schema.FieldMeta{Kind: schema.KindGroupedAggregates, Table: t, ScanCost: p.scanCost},
	})
}

// addGroupByEnum creates <Type>GroupBy from the table's plain orderBy values
// (so filters.indexed_only and @omit order apply), skipping single-column
// unique keys, whose groups would hold one row each.
func (p *Plugin) addGroupByEnum(b *schema.Builder, t *introspect.Table, s *surface) string {
	order := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	name := s.typeName + "GroupBy"
	if order == nil || !free(b, name) {
		return ""
	}
	en := &schema.Enum{Name: name, Description: "Grouping methods for `" + s.typeName + "` for usage during aggregation."}
	for _, v := range order.Values {
		if v.Column == "" || v.Desc || v.Computed != nil || v.Relation != nil || v.Distance || v.Rank || v.Similarity != "" {
			continue
		}
		base, ok := strings.CutSuffix(v.Name, "_ASC")
		c := t.Column(v.Column)
		if !ok || c == nil || s.cols[c] == "" || singleUnique(t, c.Name) || !groupable(c) {
			continue
		}
		if _, noGroup := columnTags(c); noGroup {
			continue
		}
		en.Values = append(en.Values, schema.EnumValue{Name: base, GroupBy: &schema.GroupBy{Column: c}})
		if !c.IsArray && (c.PGType == "timestamp" || c.PGType == "timestamptz") {
			for _, unit := range []string{"hour", "day"} {
				en.Values = append(en.Values, schema.EnumValue{
					Name:    base + "_TRUNCATED_TO_" + strings.ToUpper(unit),
					GroupBy: &schema.GroupBy{Column: c, SQL: dateTrunc(unit)},
				})
			}
		}
	}
	if len(en.Values) == 0 {
		return ""
	}
	b.Enums[name] = en
	return name
}

// addHaving creates <Type>HavingInput with one <Type>Having<Spec>Input per
// aggregate group whose results have a comparison filter; returns "" when
// no group qualifies.
func (p *Plugin) addHaving(b *schema.Builder, t *introspect.Table, s *surface) string {
	name := s.typeName + "HavingInput"
	if !free(b, name) {
		return ""
	}
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
			a := sp.bind(c)
			if a == nil {
				continue
			}
			filter := havingFilter(b, a.ResultType)
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
	"numeric": "Bigfloat", "money": "Bigfloat",
	"date": "Datetime", "timestamp": "Datetime", "timestamptz": "Datetime",
}

// havingFilter returns (creating if needed) the shared Having<X>Filter input
// comparing an aggregate result of pgType, or "" for result types without
// one.
func havingFilter(b *schema.Builder, pgType string) string {
	family, ok := havingScalars[pgType]
	if !ok {
		return ""
	}
	name := "Having" + family + "Filter"
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	scalar := map[string]string{"Bigint": "BigInt", "Bigfloat": "BigFloat"}[family]
	if scalar == "" {
		scalar = family
	}
	in := &schema.Input{Name: name}
	for _, op := range []string{"equalTo", "notEqualTo", "greaterThan", "greaterThanOrEqualTo", "lessThan", "lessThanOrEqualTo"} {
		in.Fields = append(in.Fields, &schema.InputField{Name: op, Type: scalar})
	}
	b.Inputs[name] = in
	return name
}

// addRelationOrder adds <REL>_COUNT_* and <REL>_<SPEC>_<COLUMN>_* values to
// t's orderBy enum for every reverse relation from an opted-in table.
func (p *Plugin) addRelationOrder(b *schema.Builder, t *introspect.Table, surfaces map[*introspect.Table]*surface) {
	en := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if en == nil {
		return
	}
	for _, child := range b.Catalog.Tables {
		cs := surfaces[child]
		if cs == nil || !cs.beh.orderBy {
			continue
		}
		for _, fk := range child.ForeignKeys {
			if fk.RefSchema != t.Schema || fk.RefTable != t.Name || omitsOrder(fk.Comment) {
				continue
			}
			relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
				Schema: child.Schema, Table: child.Name, Columns: fk.Columns, Constraint: fk.Name,
			})
			prefix := coreinflect.EnumValue(relName) + "_"
			addOrderPair(en, prefix+"COUNT", &schema.OrderRelation{FK: fk, Table: child, Aggregate: countAggregate})
			for _, sp := range specs {
				if sp.spatial {
					continue
				}
				for _, c := range relatedColumns(b, child, cs) {
					a := sp.bind(c)
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

// addOrderPair appends base_ASC/base_DESC unless either name is taken.
func addOrderPair(en *schema.Enum, base string, rel *schema.OrderRelation) {
	for _, v := range en.Values {
		if v.Name == base+"_ASC" || v.Name == base+"_DESC" {
			return
		}
	}
	en.Values = append(en.Values,
		schema.EnumValue{Name: base + "_ASC", Relation: rel},
		schema.EnumValue{Name: base + "_DESC", Relation: rel, Desc: true},
	)
}

// addRelationFilter adds the aggregates field to the advanced-filters
// <Type>ToManyFilter wrapper over t's rows: <Type>AggregatesFilter holds a
// filter narrowing the aggregated rows plus one <Type><Spec>AggregateFilter
// per group, typed with the operator inputs of the aggregate results.
func (p *Plugin) addRelationFilter(b *schema.Builder, t *introspect.Table, s *surface) {
	wrapper := b.Inputs[s.typeName+"ToManyFilter"]
	filterType := b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	name := s.typeName + "AggregatesFilter"
	if wrapper == nil || wrapper.Field("aggregates") != nil || b.Inputs[filterType] == nil || !free(b, name) {
		return
	}
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
		for _, c := range relatedColumns(b, t, s) {
			a := sp.bind(c)
			if a == nil || a.ResultType == "interval" || postgis.IsSpatial(a.ResultType) {
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

// relatedColumns returns the aggregatable columns of a related table that
// pass its filter policy (filters.indexed_only), the columns relational
// ordering and filtering may aggregate.
func relatedColumns(b *schema.Builder, t *introspect.Table, s *surface) []*introspect.Column {
	var out []*introspect.Column
	for _, c := range b.FilterableColumns(t) {
		if s.cols[c] != "" {
			out = append(out, c)
		}
	}
	return out
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

// omitsOrder reports "@omit order" (or bare @omit) on a foreign key, the tag
// advanced-filters honours for relational ordering.
func omitsOrder(comment string) bool {
	tags, _ := smarttags.Parse(comment)
	omits, everything := tags.Omits()
	return everything || omits["order"]
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

func boolSetting(settings map[string]any, key string, def bool) bool {
	switch v := settings[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func intSetting(settings map[string]any, key string, def int) int {
	switch v := settings[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func dateTrunc(unit string) func(string) string {
	return func(ref string) string { return fmt.Sprintf("date_trunc('%s', %s)", unit, ref) }
}
