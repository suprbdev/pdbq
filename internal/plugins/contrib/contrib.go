// Package contrib is a built-in plugin exposing the operator vocabularies of
// PostgreSQL's non-scalar built-in types and the common contrib extensions
// (pdbcore's contrib package) as GraphQL filter and ordering surface.
//
// Default generation already maps these column types (see package
// contribtype) and reads and writes them: tsvector as the FullText scalar,
// hstore as the KeyValueHash JSON object, ranges as PostGraphile-shaped
// IntRange { start { value inclusive } end { ... } } objects, inet/cidr as
// InternetAddress/CidrAddress, ltree and citext as String, with generic
// comparison operators that schema-qualify extension operators and cast
// operands so they work when the extension's schema is not on the
// search_path. This plugin adds everything built on top of that:
//
//   - vocabulary filter operators on each type's operator input:
//     FullTextFilterOps (matches, matchesPlain, matchesPhrase,
//     matchesWebsearch, plus a sibling config: naming the text search
//     configuration), KeyValueHashFilterOps (containsKey, containsAllKeys,
//     containsAnyKeys, contains, containedBy), LtreeFilterOps and
//     LtreeListFilterOps (ancestorOf, descendantOf, matchesLquery, ...),
//     <Range>FilterOps and <Range>MultirangeFilterOps (overlaps, contains,
//     containsElement, containedBy, strictlyLeftOf, ...), and
//     InternetAddressFilterOps (contains, containsOrEqualTo, containedBy, ...);
//
//   - pg_trgm operators on text columns when the extension is installed:
//     the column's filter takes StringTrgmFilterOps, StringFilterOps plus
//     similarTo, wordSimilarTo and strictWordSimilarTo;
//
//   - full-text rank: a <field>Rank: Float sibling of every filterable
//     tsvector column and <COLUMN>_RANK_ASC/_DESC orderBy values, both
//     computed (ts_rank) against the first matches* operator on that column
//     in the list field's filter;
//
//   - trigram ordering: <COLUMN>_SIMILARITY_ASC/_DESC (and _WORD_SIMILARITY_,
//     _STRICT_WORD_SIMILARITY_) orderBy values plus a similarTo:
//     <Type>SimilarToInput argument naming the reference text per column,
//     compiled to the index-assisted pg_trgm distance operators (<->, <->>,
//     <->>>).
//
// Under filters.indexed_only, operators no index can serve (hstore
// containedBy, the ltree *Any forms) are left out, the trigram operators
// are offered only on columns with a GIN or GiST index, and trigram
// ordering only on columns with a GiST index (columns listed in
// filters.allow_columns get everything). Operator classes are not
// introspected; a matching access method is taken to carry the right one.
//
// Like postgis, the plugin only adds schema surface plus bindings; the
// compiler reads them and stays inert when they are absent, so disabling
// the plugin (plugins.disabled: [contrib]) removes the surface while plain
// reads, writes and comparisons keep working. "@omit filter" / "@omit order"
// on a column skips its filter / ordering surface; "@omit read" on a
// tsvector column also skips its rank field and rank ordering.
package contrib

import (
	"context"
	"slices"
	"strings"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the contrib surface.
type Plugin struct {
	// filters adds the vocabulary operators, pg_trgm operators and rank
	// fields.
	filters bool
	// ordering adds the rank and similarity orderBy values and similarTo:.
	ordering bool
}

// New builds the plugin from its `plugins.settings.contrib` map: the
// booleans filters and ordering, both on by default.
func New(settings map[string]any) *Plugin {
	p := &Plugin{filters: true, ordering: true}
	for key, target := range map[string]*bool{
		"filters":  &p.filters,
		"ordering": &p.ordering,
	} {
		if v, ok := settings[key].(bool); ok {
			*target = v
		}
	}
	return p
}

// Name implements plugin.Plugin.
func (p *Plugin) Name() string { return "contrib" }

// Priority 170 places the plugin after advanced-filters (150) and postgis
// (160), so the filter inputs and orderBy enums it extends exist.
func (p *Plugin) Priority() int { return 170 }

// column is one table column the plugin has surface for.
type column struct {
	col *introspect.Column
	ci  contribtype.Column
}

// build is the per-TransformSchema state; the plugin value itself is shared
// across schema rebuilds.
type build struct {
	b        *schema.Builder
	extended map[string]bool
	orderBy  map[string][]*schema.Field
}

// TransformSchema implements plugin.SchemaHook.
func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	st := &build{b: b, extended: map[string]bool{}, orderBy: b.FieldsByOrderBy()}
	for _, t := range b.Catalog.Tables {
		var cols []column
		for _, c := range t.Columns {
			ci := b.ContribColumn(c)
			if len(ci.Vocabs) > 0 {
				cols = append(cols, column{c, ci})
			}
		}
		if len(cols) == 0 {
			continue
		}
		if p.filters {
			st.addFilters(t, cols)
		}
		if p.ordering {
			st.addOrdering(t, cols)
		}
	}
	return nil
}

// omits reports which halves of the surface a smart comment removes.
func omits(comment string) (skipFilter, skipOrder bool) {
	tags, _ := smarttags.Parse(comment)
	om, everything := tags.Omits()
	return everything || om["filter"], everything || om["order"]
}

// unreadable reports whether a smart comment hides the column's value
// (@omit read), which a rank field or rank ordering would leak.
func unreadable(comment string) bool {
	tags, _ := smarttags.Parse(comment)
	om, everything := tags.Omits()
	return everything || om["read"]
}

// served reports whether the indexed-only policy admits surface backed by
// the given index hints on column c of t.
func (st *build) served(t *introspect.Table, c *introspect.Column, hints []pgcontrib.IndexHint) bool {
	if !st.b.Options.FilterIndexedOnly {
		return true
	}
	if slices.Contains(st.b.Options.AllowColumns[t.Schema+"."+t.Name], c.Name) {
		return true
	}
	return contribtype.IndexServes(contribtype.IndexMethods(t, c.Name), hints)
}

// filterField returns the table filter's field bound to column c, or nil
// when the column is not filterable.
func (st *build) filterField(t *introspect.Table, c *introspect.Column) *schema.InputField {
	in := st.b.Inputs[st.b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	if in == nil {
		return nil
	}
	for _, f := range in.Fields {
		if f.Column == c.Name && f.Relation == nil && f.Computed == nil {
			return f
		}
	}
	return nil
}

func (st *build) addFilters(t *introspect.Table, cols []column) {
	for _, cc := range cols {
		if skip, _ := omits(cc.col.Comment); skip {
			continue
		}
		f := st.filterField(t, cc.col)
		if f == nil {
			continue
		}
		if cc.ci.Owned() && f.Type == cc.ci.OpsInput {
			st.extendOps(cc.ci)
			if cc.ci.Kind == contribtype.FullText && !unreadable(cc.col.Comment) {
				st.addRankField(t, cc.col)
			}
		}
		if trgm, ok := cc.ci.Trgm(); ok && f.Type == "StringFilterOps" && st.served(t, cc.col, trgm.Ops[0].Index) {
			if st.ensureTrgmOps(trgm) {
				f.Type = contribtype.StringTrgmOps
			}
		}
	}
}

// operandType is the GraphQL type of a vocabulary operator's operand on a
// column.
func operandType(ci contribtype.Column, o pgcontrib.Op) string {
	switch o.Arg {
	case pgcontrib.ArgTextList:
		return "[String!]"
	case pgcontrib.ArgHstore:
		return contribtype.ScalarKeyValueHash
	case pgcontrib.ArgRange:
		return ci.Input
	case pgcontrib.ArgElement:
		return ci.ElemScalar
	case pgcontrib.ArgText:
		if ci.Kind == contribtype.Inet {
			return ci.Scalar
		}
	}
	return "String"
}

// extendOps adds the owning vocabulary's operators to the type's shared
// operator input once per build.
func (st *build) extendOps(ci contribtype.Column) {
	if st.extended[ci.OpsInput] {
		return
	}
	st.extended[ci.OpsInput] = true
	in := st.b.Inputs[ci.OpsInput]
	if in == nil {
		return
	}
	for _, o := range ci.FilterOps() {
		if st.b.Options.FilterIndexedOnly && !o.Indexable() {
			continue
		}
		in.AddField(&schema.InputField{
			Name:        contribtype.OpName(ci.Own, o),
			Type:        operandType(ci, o),
			Description: o.Description,
		})
	}
	if ci.Kind == contribtype.FullText {
		in.AddField(&schema.InputField{
			Name: "config",
			Type: "String",
			Description: "Text search configuration (regconfig, e.g. english) the matches* operators of this " +
				"object parse their query with; default_text_search_config when omitted.",
		})
	}
}

// ensureTrgmOps declares StringTrgmFilterOps from StringFilterOps plus the
// pg_trgm operators, reporting false when the name is taken.
func (st *build) ensureTrgmOps(trgm pgcontrib.Vocabulary) bool {
	if st.extended[contribtype.StringTrgmOps] {
		return true
	}
	base := st.b.Inputs["StringFilterOps"]
	if base == nil {
		return false
	}
	if _, taken := st.b.Inputs[contribtype.StringTrgmOps]; taken {
		st.b.Options.Logger.Warn("contrib: type name already taken, trigram operators disabled", "type", contribtype.StringTrgmOps)
		return false
	}
	st.extended[contribtype.StringTrgmOps] = true
	in := &schema.Input{
		Name: contribtype.StringTrgmOps,
		Description: "String operators plus pg_trgm similarity; the similarity predicates compare against the " +
			"pg_trgm.*similarity_threshold settings and can use a GIN or GiST trigram index.",
	}
	for _, f := range base.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	for _, o := range trgm.Ops {
		in.AddField(&schema.InputField{Name: contribtype.OpName(trgm, o), Type: "String", Description: o.Description})
	}
	st.b.Inputs[contribtype.StringTrgmOps] = in
	return true
}

// addRankField adds <field>Rank: Float next to a tsvector column.
func (st *build) addRankField(t *introspect.Table, c *introspect.Column) {
	obj := st.b.Objects[st.b.TypeForTable[t.Schema+"."+t.Name]]
	if obj == nil {
		return
	}
	name := st.b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}) + "Rank"
	if obj.Field(name) != nil {
		return
	}
	obj.AddField(&schema.Field{
		Name: name,
		Type: "Float",
		Description: "Full-text rank (ts_rank) of " + c.Name + " against the first matches* operator on it in " +
			"the filter of the list this row was read through; null without one.",
		Meta: &schema.FieldMeta{Kind: schema.KindFullTextRank, Table: t, Column: c},
	})
}

func (st *build) addOrdering(t *introspect.Table, cols []column) {
	b := st.b
	enumName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en, ok := b.Enums[enumName]
	if !ok {
		return
	}
	var similar []*introspect.Column
	for _, cc := range cols {
		if _, skip := omits(cc.col.Comment); skip || !orderable(en, cc.col.Name) {
			continue
		}
		if cc.ci.Kind == contribtype.FullText {
			if unreadable(cc.col.Comment) {
				continue
			}
			if f := st.filterField(t, cc.col); f != nil && f.Type == cc.ci.OpsInput && b.Inputs[f.Type].Field("matches") != nil {
				addValue(en, coreinflect.EnumValue(cc.col.Name)+"_RANK_ASC", schema.EnumValue{
					Column: cc.col.Name, Rank: true,
					Description: "Order by full-text rank against the matches* filter on this column (least relevant first). Requires that filter.",
				})
				addValue(en, coreinflect.EnumValue(cc.col.Name)+"_RANK_DESC", schema.EnumValue{
					Column: cc.col.Name, Rank: true, Desc: true,
					Description: "Order by full-text rank against the matches* filter on this column (most relevant first). Requires that filter.",
				})
			}
			continue
		}
		trgm, ok := cc.ci.Trgm()
		if !ok {
			continue
		}
		added := false
		for _, so := range contribtype.SimilarityOrders {
			e, ok := trgm.Expr(so.Expr)
			if !ok || !st.served(t, cc.col, e.Index) {
				continue
			}
			base := coreinflect.EnumValue(cc.col.Name) + so.Infix
			desc := "Order by pg_trgm " + strings.ToLower(strings.TrimPrefix(strings.ReplaceAll(so.Infix, "_", " "), " ")) +
				" to the similarTo: reference text for this column"
			addValue(en, base+"_ASC", schema.EnumValue{
				Column: cc.col.Name, Similarity: so.Expr, Desc: true,
				Description: desc + " (least similar first). Requires similarTo:.",
			})
			addValue(en, base+"_DESC", schema.EnumValue{
				Column: cc.col.Name, Similarity: so.Expr,
				Description: desc + " (most similar first; index-assisted with a GiST trigram index). Requires similarTo:.",
			})
			added = true
		}
		if added {
			similar = append(similar, cc.col)
		}
	}
	if len(similar) > 0 {
		st.addSimilarToArg(t, enumName, similar)
	}
}

// orderable reports whether the orderBy enum orders by the column, i.e. the
// column passed the indexed-only policy.
func orderable(en *schema.Enum, col string) bool {
	for _, v := range en.Values {
		if v.Column == col && v.Computed == nil && v.Relation == nil && !v.Distance {
			return true
		}
	}
	return false
}

func addValue(en *schema.Enum, name string, v schema.EnumValue) {
	if en.HasValue(name) {
		return
	}
	v.Name = name
	en.Values = append(en.Values, v)
}

// addSimilarToArg declares <Type>SimilarToInput (one String per column with
// similarity ordering) and adds similarTo: to every field taking the
// table's orderBy enum.
func (st *build) addSimilarToArg(t *introspect.Table, enumName string, cols []*introspect.Column) {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	inputName := typeName + "SimilarToInput"
	_, obj := b.Objects[inputName]
	_, in := b.Inputs[inputName]
	_, en := b.Enums[inputName]
	if obj || in || en {
		b.Options.Logger.Warn("contrib: type name already taken, similarTo: argument skipped", "type", inputName)
		return
	}
	input := &schema.Input{
		Name:        inputName,
		Description: "Reference texts for the pg_trgm similarity orderBy values of " + typeName + ", per column.",
	}
	for _, c := range cols {
		input.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        "String",
			Description: "Reference text for the " + c.Name + " similarity orderBy values.",
			Column:      c.Name,
		})
	}
	b.Inputs[inputName] = input
	for _, f := range st.orderBy[enumName] {
		if !hasArg(f, "similarTo") {
			f.Args = append(f.Args, schema.Arg{Name: "similarTo", Type: inputName})
		}
	}
}

func hasArg(f *schema.Field, name string) bool {
	for _, a := range f.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}
