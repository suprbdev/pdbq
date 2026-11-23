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
//   - unaccent operators on text columns when the extension is installed
//     and the filter policy admits unindexable operators on the column:
//     StringUnaccentFilterOps (or StringTrgmUnaccentFilterOps on top of the
//     trigram input) adds unaccentEqualTo, unaccentLike, unaccentIncludes
//     and unaccentStartsWith;
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
//     <->>>);
//
//   - pgvector ordering: <COLUMN>_L2_DISTANCE_ASC/_DESC (and
//     _NEGATIVE_INNER_PRODUCT_, _COSINE_DISTANCE_, _L1_DISTANCE_) orderBy
//     values on vector, halfvec and sparsevec columns, and
//     _HAMMING_DISTANCE_ / _JACCARD_DISTANCE_ on bit and varbit columns,
//     plus a nearVector: <Type>NearVectorInput argument holding the
//     reference vector per column, compiled to pgvector's distance operators
//     (<->, <#>, <=>, <+>, <~>, <%>), which an HNSW or IVFFlat index answers
//     nearest-first;
//
//   - score fields: <field>Similarity, <field>WordSimilarity and
//     <field>StrictWordSimilarity on text columns with trigram surface,
//     measured against similarTo:, and <field>L2Distance,
//     <field>NegativeInnerProduct, <field>CosineDistance, <field>L1Distance
//     (<field>HammingDistance, <field>JaccardDistance on bit columns)
//     measured against nearVector:, null without the reference; they come
//     with the ordering setting and ignore @omit order and index coverage;
//
//   - distance thresholds: VectorDistanceFilterOps,
//     SparseVectorDistanceFilterOps and BitDistanceFilterOps add
//     <distance>LessThan: {vector, distance} operators, which no index
//     serves, on columns the indexed-only policy admits them for.
//
// Under filters.indexed_only, operators no index can serve (hstore
// containedBy, the ltree *Any forms, the unaccent operators, the vector and
// bit distance thresholds) are left out,
// the trigram operators are offered only on columns with a GIN or GiST
// index, trigram ordering only on columns with a GiST index, and each
// vector distance ordering only on columns leading an index of a method
// that serves it (HNSW, or IVFFlat except for L1 and sparsevec); columns
// listed in filters.allow_columns get everything except the hstore and ltree
// operators, which live on shared inputs. Operator classes are not
// introspected; a matching access method is taken to carry the right one.
//
// Like postgis, the plugin only adds schema surface plus bindings; the
// compiler reads them and stays inert when they are absent, so disabling
// the plugin (plugins.disabled: [contrib]) removes the surface while plain
// reads, writes and comparisons keep working. "@omit filter" / "@omit order"
// on a column skips its filter / ordering surface; "@omit read" also skips
// its rank field, its score fields and its rank, similarity and vector
// distance orderings, which read the value back against a client-chosen
// reference (package omit).
package contrib

import (
	"context"
	"slices"
	"strings"

	pgcontrib "github.com/suprbdev/pdbcore/contrib"
	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/omit"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin implements the SchemaHook adding the contrib surface.
type Plugin struct {
	// filters adds the vocabulary operators, pg_trgm, unaccent and
	// distance-threshold operators and rank fields.
	filters bool
	// ordering adds the rank, similarity and vector distance orderBy values,
	// the score fields, similarTo: and nearVector:.
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
		if omit.Column(cc.col, omit.Filter) {
			continue
		}
		f := st.filterField(t, cc.col)
		if f == nil {
			continue
		}
		if cc.ci.Owned() && f.Type == cc.ci.OpsInput {
			st.extendOps(cc.ci)
			if cc.ci.Kind == contribtype.FullText && !omit.Column(cc.col, omit.Read) {
				st.addRankField(t, cc.col)
			}
		}
		if trgm, ok := cc.ci.Trgm(); ok && f.Type == "StringFilterOps" && st.served(t, cc.col, trgm.Ops[0].Index) {
			if st.ensureTrgmOps(trgm) {
				f.Type = contribtype.StringTrgmOps
			}
		}
		if ua, ok := cc.ci.Unaccent(); ok && len(ua.Ops) > 0 && st.served(t, cc.col, ua.Ops[0].Index) {
			if name := st.ensureUnaccentOps(f.Type, ua); name != "" {
				f.Type = name
			}
		}
		if dv, ok := cc.ci.Distances(); ok && len(dv.Ops) > 0 && st.served(t, cc.col, nil) {
			if name := st.ensureDistanceOps(f.Type, cc.ci, dv); name != "" {
				f.Type = name
			}
		}
	}
}

// distanceInputs maps the operator inputs of vector and bit columns to the
// inputs adding the distance-threshold operators to them, with their
// operand input, the reference vector's GraphQL type and descriptions.
var distanceInputs = map[string]struct{ name, operand, vecType, desc string }{
	contribtype.VectorOps: {contribtype.VectorDistanceOps, contribtype.VectorDistanceInput, "[Float!]!",
		"pgvector operators plus distance thresholds against a reference vector; no index serves the thresholds."},
	"StringFilterOps": {contribtype.BitDistanceOps, contribtype.BitDistanceInput, "String!",
		"String operators plus pgvector Hamming and Jaccard distance thresholds against a reference bit string; no index serves the thresholds."},
}

// ensureDistanceOps declares the input adding the distance-threshold
// operators of dv to the operator input base of a vector or bit column (and
// their operand input), returning its name, or "" when base takes none or
// a name is taken.
func (st *build) ensureDistanceOps(base string, ci contribtype.Column, dv pgcontrib.Vocabulary) string {
	target, ok := distanceInputs[base]
	if !ok || (ci.Kind == contribtype.Vector) != (base == contribtype.VectorOps) {
		return ""
	}
	if ci.Kind == contribtype.Vector && !ci.List() {
		target.name, target.operand, target.vecType = contribtype.SparseVectorDistanceOps, contribtype.SparseVectorDistanceInput, "String!"
	}
	if st.extended[target.name] {
		return target.name
	}
	src := st.b.Inputs[base]
	if src == nil {
		return ""
	}
	for _, n := range []string{target.name, target.operand} {
		_, in := st.b.Inputs[n]
		_, obj := st.b.Objects[n]
		_, en := st.b.Enums[n]
		if in || obj || en {
			st.b.Options.Logger.Warn("contrib: type name already taken, distance thresholds disabled", "type", n)
			return ""
		}
	}
	st.extended[target.name] = true
	st.b.Inputs[target.operand] = &schema.Input{
		Name:        target.operand,
		Description: "A distance threshold against a reference: rows whose distance is less than distance match.",
		Fields: []*schema.InputField{
			{Name: "vector", Type: target.vecType, Description: "Reference value; it must have the column's dimensions."},
			{Name: "distance", Type: "Float!", Description: "Exclusive upper bound of the distance."},
		},
	}
	in := &schema.Input{Name: target.name, Description: target.desc}
	for _, f := range src.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	for _, o := range dv.Ops {
		in.AddField(&schema.InputField{Name: contribtype.OpName(dv, o), Type: target.operand, Description: o.Description})
	}
	st.b.Inputs[target.name] = in
	return target.name
}

// unaccentInputs maps the operator inputs of text columns to the inputs
// adding the unaccent operators to them, with their descriptions.
var unaccentInputs = map[string]struct{ name, desc string }{
	"StringFilterOps": {contribtype.StringUnaccentOps,
		"String operators plus unaccent comparisons, which ignore accents and case; no index can serve them."},
	contribtype.StringTrgmOps: {contribtype.StringTrgmUnaccentOps,
		"String operators plus pg_trgm similarity and unaccent comparisons. The similarity predicates compare " +
			"against the pg_trgm.*similarity_threshold settings and can use a GIN or GiST trigram index; the " +
			"unaccent comparisons ignore accents and case, and no index can serve them."},
}

// ensureUnaccentOps declares the input adding the unaccent operators to the
// text operator input base, returning its name, or "" when base takes none
// or the name is taken.
func (st *build) ensureUnaccentOps(base string, ua pgcontrib.Vocabulary) string {
	target, ok := unaccentInputs[base]
	if !ok {
		return ""
	}
	if st.extended[target.name] {
		return target.name
	}
	src := st.b.Inputs[base]
	if src == nil {
		return ""
	}
	if _, taken := st.b.Inputs[target.name]; taken {
		st.b.Options.Logger.Warn("contrib: type name already taken, unaccent operators disabled", "type", target.name)
		return ""
	}
	st.extended[target.name] = true
	in := &schema.Input{Name: target.name, Description: target.desc}
	for _, f := range src.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	for _, o := range ua.Ops {
		in.AddField(&schema.InputField{Name: contribtype.OpName(ua, o), Type: "String", Description: o.Description})
	}
	st.b.Inputs[target.name] = in
	return target.name
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
	var vectors []vectorColumn
	var filterable map[string]bool
	for _, cc := range cols {
		if omit.Column(cc.col, omit.Read) {
			continue
		}
		ordered := !omit.Column(cc.col, omit.Order)
		if dv, ok := cc.ci.Distances(); ok {
			if filterable == nil {
				filterable = map[string]bool{}
				for _, c := range b.FilterableColumns(t) {
					filterable[c.Name] = true
				}
			}
			var hints []pgcontrib.IndexHint
			for _, e := range dv.Exprs {
				hints = append(hints, e.Index...)
			}
			vectors = append(vectors, vectorColumn{
				column: cc, vocab: dv,
				ordered: ordered && filterable[cc.col.Name],
				scored:  cc.ci.Kind == contribtype.Vector || st.served(t, cc.col, hints),
			})
			continue
		}
		ordered = ordered && orderable(en, cc.col.Name)
		if cc.ci.Kind == contribtype.FullText {
			if !ordered {
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
			if !ordered || !ok || !st.served(t, cc.col, e.Index) {
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
		if len(trgm.Ops) > 0 && st.served(t, cc.col, trgm.Ops[0].Index) && st.addScores(t, cc.col, trgm, "similarTo") {
			added = true
		}
		if added {
			similar = append(similar, cc.col)
		}
	}
	if len(similar) > 0 && !st.addSimilarToArg(t, enumName, similar) {
		st.dropScores(t, "similarTo")
	}
	if len(vectors) > 0 {
		st.addVectorOrdering(t, enumName, en, vectors)
	}
}

// vectorColumn is a vector or bit column with pgvector distances: its
// distance vocabulary, whether the policy admits its ordering, and whether
// it gets distance scores (every vector column; a bit column only where an
// index or the policy admits pgvector's surface, so installing pgvector
// leaves unrelated bit columns alone).
type vectorColumn struct {
	column
	vocab   pgcontrib.Vocabulary
	ordered bool
	scored  bool
}

// addScores adds a score field per [pgcontrib.Expr.Score] expression of
// vocab on column c (<field>Similarity, <field>CosineDistance, ...),
// measured against the list's arg reference, reporting whether any was
// added.
func (st *build) addScores(t *introspect.Table, c *introspect.Column, vocab pgcontrib.Vocabulary, arg string) bool {
	obj := st.b.Objects[st.b.TypeForTable[t.Schema+"."+t.Name]]
	if obj == nil {
		return false
	}
	field := st.b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name})
	added := false
	for _, e := range vocab.Exprs {
		if !e.Score {
			continue
		}
		name := field + coreinflect.UpperCamel(e.Name)
		if obj.Field(name) != nil {
			continue
		}
		obj.AddField(&schema.Field{
			Name: name,
			Type: "Float",
			Description: strings.TrimSuffix(e.Description, ".") + "; " + c.Name + " measured against the " + arg +
				": reference of the list this row was read through, null without one.",
			Meta: &schema.FieldMeta{Kind: schema.KindScore, Table: t, Column: c, Score: &schema.Score{Arg: arg, Expr: e.Name}},
		})
		added = true
	}
	return added
}

// dropScores removes the score fields reading arg from t's object type, for
// a table whose reference input could not be declared.
func (st *build) dropScores(t *introspect.Table, arg string) {
	obj := st.b.Objects[st.b.TypeForTable[t.Schema+"."+t.Name]]
	if obj == nil {
		return
	}
	var keep []*schema.Field
	for _, f := range obj.Fields {
		if f.Meta == nil || f.Meta.Kind != schema.KindScore || f.Meta.Score == nil || f.Meta.Score.Arg != arg {
			keep = append(keep, f)
		}
	}
	obj.Fields = keep
}

// addVectorOrdering adds the pgvector distance orderBy values of the vector
// and bit columns the indexed-only policy admits them for and the distance
// score fields of every readable one, declares <Type>NearVectorInput (one
// reference vector per such column) and adds nearVector: to every field
// taking the table's orderBy enum. Nothing is added when the input's name
// is taken.
func (st *build) addVectorOrdering(t *introspect.Table, enumName string, en *schema.Enum, cols []vectorColumn) {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	inputName := typeName + "NearVectorInput"
	_, obj := b.Objects[inputName]
	_, in := b.Inputs[inputName]
	_, ene := b.Enums[inputName]
	if obj || in || ene {
		b.Options.Logger.Warn("contrib: type name already taken, vector distance ordering skipped", "type", inputName)
		return
	}
	input := &schema.Input{
		Name:        inputName,
		Description: "Reference vectors for the pgvector distance orderBy values and distance fields of " + typeName + ", per column.",
	}
	for _, vc := range cols {
		added := false
		for _, vo := range contribtype.VectorOrders {
			e, ok := vc.vocab.Expr(vo.Expr)
			if !vc.ordered || !ok || !st.served(t, vc.col, e.Index) {
				continue
			}
			base := coreinflect.EnumValue(vc.col.Name) + vo.Infix
			desc := "Order by pgvector " + vo.Label + " from the nearVector: reference vector for this column"
			addValue(en, base+"_ASC", schema.EnumValue{
				Column: vc.col.Name, Vector: vo.Expr,
				Description: desc + " (nearest first; index-assisted with an HNSW or IVFFlat index built with the matching operator class). Requires nearVector:.",
			})
			addValue(en, base+"_DESC", schema.EnumValue{
				Column: vc.col.Name, Vector: vo.Expr, Desc: true,
				Description: desc + " (farthest first). Requires nearVector:.",
			})
			added = true
		}
		if vc.scored && st.addScores(t, vc.col, vc.vocab, "nearVector") {
			added = true
		}
		if !added {
			continue
		}
		input.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: vc.col.Name}),
			Type:        vc.ci.DistanceInput(),
			Description: "Reference vector for the " + vc.col.Name + " distance orderBy values and distance fields; it must have the column's dimensions.",
			Column:      vc.col.Name,
		})
	}
	if len(input.Fields) == 0 {
		return
	}
	b.Inputs[inputName] = input
	for _, f := range st.orderBy[enumName] {
		if !hasArg(f, "nearVector") {
			f.Args = append(f.Args, schema.Arg{Name: "nearVector", Type: inputName})
		}
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
// similarity ordering or scores) and adds similarTo: to every field taking
// the table's orderBy enum, reporting false when the input's name is taken.
func (st *build) addSimilarToArg(t *introspect.Table, enumName string, cols []*introspect.Column) bool {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	inputName := typeName + "SimilarToInput"
	_, obj := b.Objects[inputName]
	_, in := b.Inputs[inputName]
	_, en := b.Enums[inputName]
	if obj || in || en {
		b.Options.Logger.Warn("contrib: type name already taken, similarTo: argument skipped", "type", inputName)
		return false
	}
	input := &schema.Input{
		Name:        inputName,
		Description: "Reference texts for the pg_trgm similarity orderBy values and similarity fields of " + typeName + ", per column.",
	}
	for _, c := range cols {
		input.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        "String",
			Description: "Reference text for the " + c.Name + " similarity orderBy values and similarity fields.",
			Column:      c.Name,
		})
	}
	b.Inputs[inputName] = input
	for _, f := range st.orderBy[enumName] {
		if !hasArg(f, "similarTo") {
			f.Args = append(f.Args, schema.Arg{Name: "similarTo", Type: inputName})
		}
	}
	return true
}

func hasArg(f *schema.Field, name string) bool {
	for _, a := range f.Args {
		if a.Name == name {
			return true
		}
	}
	return false
}
