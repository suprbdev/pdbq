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
//   - fuzzystrmatch operators on text columns when the extension is
//     installed and the filter policy admits unindexable operators on the
//     column: the String[Trgm][Unaccent]FuzzyFilterOps inputs add
//     levenshteinDistanceLessThan ({text, distance}), soundexEqualTo,
//     metaphoneEqualTo and doubleMetaphoneEqualTo, and
//     <COLUMN>_LEVENSHTEIN_DISTANCE_ASC/_DESC orderBy values and a
//     <field>LevenshteinDistance: Int score measure against similarTo:;
//
//   - intarray's matchesQueryInt (query_int) on int4[] columns led by a GIN
//     or GiST index (or allowlisted), through IntListIntarrayFilterOps;
//
//   - value fields: <field>Nlevel, <field>Parent, <field>Lca(path:) and
//     <field>Subpath(offset:, length:) on ltree columns, <field>Lca on
//     ltree[] columns, <field>Value(key:) on hstore columns and
//     <field>Icount on int4[] columns with intarray, with <field>Nlevel,
//     <field>Icount and <field>Value table-filter fields and
//     <COLUMN>_NLEVEL_, _ICOUNT_ and _VALUE_ orderBy values (the last
//     against a hstoreKey: <Type>HstoreKeyInput argument) on columns the
//     policy admits unindexed surface on;
//
//   - full-text rank: a <field>Rank: Float sibling of every filterable
//     tsvector column and <COLUMN>_RANK_ASC/_DESC orderBy values, both
//     computed (ts_rank) against the first matches* operator on that column
//     in the list field's filter, and a <field>Headline(document:, options:)
//     snippet (ts_headline of a text column of the table, named by the
//     <Type>HeadlineDocument enum, with FullTextHeadlineOptions) against the
//     same match;
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
//   - cube: CubeFilterOps (contains, containedBy, overlaps) on cube columns,
//     _L2_DISTANCE_, _L1_DISTANCE_ and _CHEBYSHEV_DISTANCE_ orderBy values
//     against nearVector:, <distance>LessThan thresholds on
//     CubeDistanceFilterOps, all served by a GiST index;
//
//   - earthdistance: on earth columns the earthDistanceLessThan radius filter
//     (LatLonDistanceFilterOps) and <COLUMN>_EARTH_DISTANCE_ASC/_DESC ordered
//     by cube's <-> chord distance (a GiST KNN scan, which keyset cursors
//     compare too) against a nearPoint: <Type>NearPointInput argument; on
//     point columns the policy admits, the <@> statute-mile
//     _EARTH_DISTANCE_MILES_ ordering and earthDistanceMilesLessThan filter
//     (PointDistanceFilterOps);
//
//   - score fields: <field>Similarity, <field>WordSimilarity and
//     <field>StrictWordSimilarity on text columns with trigram surface,
//     measured against similarTo:, and <field>L2Distance,
//     <field>NegativeInnerProduct, <field>CosineDistance, <field>L1Distance
//     (<field>HammingDistance, <field>JaccardDistance on bit columns,
//     <field>ChebyshevDistance on cube columns) measured against
//     nearVector:, <field>EarthDistance (metres, earth columns) and
//     <field>EarthDistanceMiles (point columns) measured against nearPoint:,
//     null without the reference; they come
//     with the ordering setting and ignore @omit order and index coverage;
//
//   - distance thresholds: VectorDistanceFilterOps,
//     SparseVectorDistanceFilterOps and BitDistanceFilterOps add
//     <distance>LessThan: {vector, distance} operators, which no index
//     serves, CubeDistanceFilterOps, LatLonDistanceFilterOps and
//     PointDistanceFilterOps the cube and earthdistance ones, on columns the
//     indexed-only policy admits them for.
//
// Under filters.indexed_only, operators no index can serve (hstore
// containedBy and containsDefinedKey, the ltree *Any forms, the unaccent and
// fuzzystrmatch operators, the vector and bit distance thresholds, the value
// filters and orderings) are left out,
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
// its rank and headline fields, its score and value fields and its rank,
// similarity, distance and value orderings, which read the value back against a
// client-chosen reference (package omit). The value fields come with the
// plugin regardless of the filters and ordering settings.
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
	// ordering adds the rank, similarity, vector, cube and earth distance
	// orderBy values, the score fields, similarTo:, nearVector: and
	// nearPoint:.
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
	// documents maps each declared <Type>HeadlineDocument enum to the
	// columns its values name.
	documents map[string]map[string]*introspect.Column
}

// TransformSchema implements plugin.SchemaHook.
func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	st := &build{b: b, extended: map[string]bool{}, orderBy: b.FieldsByOrderBy(), documents: map[string]map[string]*introspect.Column{}}
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
		st.addValueFields(t, cols)
		if p.filters {
			st.addFilters(t, cols)
		}
		if p.ordering {
			st.addOrdering(t, cols)
			st.addValueOrdering(t, cols)
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
	return contribtype.IndexServes(t, c.Name, hints)
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
				st.addHeadlineField(t, cc.col)
			}
		}
		if trgm, ok := cc.ci.Trgm(); ok && f.Type == "StringFilterOps" && st.served(t, cc.col, trgm.Ops[0].Index) {
			if st.ensureTrgmOps(trgm) {
				f.Type = contribtype.StringTrgmOps
			}
		}
		var extra []pgcontrib.Vocabulary
		for _, v := range []func() (pgcontrib.Vocabulary, bool){cc.ci.Unaccent, cc.ci.Fuzzy} {
			if voc, ok := v(); ok && len(voc.Ops) > 0 && st.served(t, cc.col, voc.Ops[0].Index) {
				extra = append(extra, voc)
			}
		}
		if len(extra) > 0 {
			if name := st.ensureTextOps(f.Type, extra); name != "" {
				f.Type = name
			}
		}
		if ia, ok := cc.ci.Intarray(); ok && len(ia.Ops) > 0 && f.Type == "IntListFilterOps" && st.served(t, cc.col, ia.Ops[0].Index) {
			if st.ensureIntarrayOps(ia) {
				f.Type = contribtype.IntListIntarrayOps
			}
		}
		if st.served(t, cc.col, nil) {
			st.addValueFilters(t, cc)
		}
		if dops := cc.ci.DistanceOps(); len(dops) > 0 && st.served(t, cc.col, dops[0].Index) {
			if name := st.ensureDistanceOps(f.Type, cc.ci, dops); name != "" {
				f.Type = name
			}
		}
	}
}

// distanceTarget is the distance-threshold input of one kind of column: its
// name, the operator input it extends, its operand input with that input's
// fields, and its description.
type distanceTarget struct {
	name, base, operand, desc string
	fields                    []*schema.InputField
}

// distanceField is the distance member of a threshold operand.
func distanceField() *schema.InputField {
	return &schema.InputField{Name: "distance", Type: "Float!", Description: "Exclusive upper bound of the distance."}
}

// latLonDistanceFields are the members of LatLonDistanceInput, shared by
// earth and point columns.
func latLonDistanceFields() []*schema.InputField {
	return []*schema.InputField{
		{Name: "lat", Type: "Float!", Description: "Latitude of the reference location in degrees."},
		{Name: "lon", Type: "Float!", Description: "Longitude of the reference location in degrees."},
		{Name: "distance", Type: "Float!", Description: "Exclusive upper bound of the distance: metres on earth columns " +
			"(earthDistanceLessThan), statute miles on point columns (earthDistanceMilesLessThan)."},
	}
}

// distanceTargetFor returns the distance-threshold input of column ci.
func distanceTargetFor(ci contribtype.Column) (distanceTarget, bool) {
	vector := func(typ string) []*schema.InputField {
		return []*schema.InputField{
			{Name: "vector", Type: typ, Description: "Reference value; it must have the column's dimensions."},
			distanceField(),
		}
	}
	const vectorDesc = "pgvector operators plus distance thresholds against a reference vector; no index serves the thresholds."
	switch ci.Kind {
	case contribtype.Vector:
		if !ci.List() {
			return distanceTarget{contribtype.SparseVectorDistanceOps, contribtype.VectorOps, contribtype.SparseVectorDistanceInput,
				vectorDesc, vector("String!")}, true
		}
		return distanceTarget{contribtype.VectorDistanceOps, contribtype.VectorOps, contribtype.VectorDistanceInput,
			vectorDesc, vector("[Float!]!")}, true
	case contribtype.Cube:
		return distanceTarget{contribtype.CubeDistanceOps, ci.OpsInput, contribtype.CubeDistanceInput,
			"cube operators plus distance thresholds against a reference cube, which a GiST index serves through an overlap prefilter.",
			[]*schema.InputField{{Name: "cube", Type: contribtype.ScalarCube + "!", Description: "Reference cube."}, distanceField()}}, true
	case contribtype.Earth:
		return distanceTarget{contribtype.LatLonDistanceOps, ci.OpsInput, contribtype.LatLonDistanceInput,
			"Earth location operators plus the earthdistance radius filter, which a GiST index serves.", latLonDistanceFields()}, true
	}
	dv, ok := ci.Distances()
	switch {
	case !ok:
	case dv.Name == pgcontrib.VocabBit:
		return distanceTarget{contribtype.BitDistanceOps, "StringFilterOps", contribtype.BitDistanceInput,
			"String operators plus pgvector Hamming and Jaccard distance thresholds against a reference bit string; no index serves the thresholds.",
			vector("String!")}, true
	case dv.Name == pgcontrib.VocabEarthPoint:
		return distanceTarget{contribtype.PointDistanceOps, "StringFilterOps", contribtype.LatLonDistanceInput,
			"Point operators plus the earthdistance <@> radius filter in statute miles, the point being (longitude, latitude); no index serves it.",
			latLonDistanceFields()}, true
	}
	return distanceTarget{}, false
}

// ensureDistanceOps declares the input adding the distance-threshold
// operators ops to the operator input base of a column with distances (and
// their operand input, which earth and point columns share), returning its
// name, or "" when base is not the input the column's thresholds extend or
// a name is taken.
func (st *build) ensureDistanceOps(base string, ci contribtype.Column, ops []pgcontrib.Op) string {
	target, ok := distanceTargetFor(ci)
	if !ok || base != target.base {
		return ""
	}
	if st.extended[target.name] {
		return target.name
	}
	src := st.b.Inputs[base]
	if src == nil {
		return ""
	}
	for _, n := range []string{target.name, target.operand} {
		if !st.extended[n] && st.nameTaken(n) {
			st.b.Options.Logger.Warn("contrib: type name already taken, distance thresholds disabled", "type", n)
			return ""
		}
	}
	st.extended[target.name] = true
	if !st.extended[target.operand] {
		st.extended[target.operand] = true
		st.b.Inputs[target.operand] = &schema.Input{
			Name:        target.operand,
			Description: "A distance threshold against a reference: rows whose distance is less than distance match.",
			Fields:      target.fields,
		}
	}
	in := &schema.Input{Name: target.name, Description: target.desc}
	for _, f := range src.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	dv, _ := ci.Distances()
	for _, o := range ops {
		in.AddField(&schema.InputField{Name: contribtype.OpName(dv, o), Type: target.operand, Description: o.Description})
	}
	st.b.Inputs[target.name] = in
	return target.name
}

// textOpsDescriptions describe the text operator inputs by the
// vocabularies they add to StringFilterOps.
var textOpsDescriptions = map[string]string{
	contribtype.StringUnaccentOps: "String operators plus unaccent comparisons, which ignore accents and case; no index can serve them.",
	contribtype.StringTrgmUnaccentOps: "String operators plus pg_trgm similarity and unaccent comparisons. The similarity predicates compare " +
		"against the pg_trgm.*similarity_threshold settings and can use a GIN or GiST trigram index; the " +
		"unaccent comparisons ignore accents and case, and no index can serve them.",
}

// textOpsDescription describes a text operator input adding the given
// vocabularies to StringFilterOps.
func textOpsDescription(name string, trgm, unaccent, fuzzy bool) string {
	if d, ok := textOpsDescriptions[name]; ok {
		return d
	}
	var parts []string
	if trgm {
		parts = append(parts, "pg_trgm similarity (against the pg_trgm.*similarity_threshold settings; a GIN or GiST trigram index can serve it)")
	}
	if unaccent {
		parts = append(parts, "unaccent comparisons ignoring accents and case")
	}
	if fuzzy {
		parts = append(parts, "fuzzystrmatch Levenshtein distance thresholds and soundex, metaphone and double metaphone equality")
	}
	desc := "String operators plus " + strings.Join(parts, ", ") + "."
	if unaccent || fuzzy {
		desc += " No index can serve the unaccent and fuzzystrmatch operators."
	}
	return desc
}

// ensureTextOps declares the input adding the operators of extra (unaccent,
// fuzzystrmatch) to the text operator input base (StringFilterOps or
// StringTrgmFilterOps), returning its name, or "" when base takes none or a
// name is taken.
func (st *build) ensureTextOps(base string, extra []pgcontrib.Vocabulary) string {
	if base != "StringFilterOps" && base != contribtype.StringTrgmOps {
		return ""
	}
	has := map[string]bool{}
	for _, v := range extra {
		has[v.Name] = true
	}
	trgm := base == contribtype.StringTrgmOps
	name := contribtype.StringOps(trgm, has[pgcontrib.VocabUnaccent], has[pgcontrib.VocabFuzzy])
	if st.extended[name] {
		return name
	}
	src := st.b.Inputs[base]
	if src == nil {
		return ""
	}
	if _, taken := st.b.Inputs[name]; taken {
		st.b.Options.Logger.Warn("contrib: type name already taken, text operators disabled", "type", name)
		return ""
	}
	if has[pgcontrib.VocabFuzzy] && !st.ensureTextDistanceInput() {
		return ""
	}
	st.extended[name] = true
	in := &schema.Input{Name: name, Description: textOpsDescription(name, trgm, has[pgcontrib.VocabUnaccent], has[pgcontrib.VocabFuzzy])}
	for _, f := range src.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	for _, v := range extra {
		for _, o := range v.Ops {
			typ := "String"
			if o.Arg == pgcontrib.ArgTextDistance {
				typ = contribtype.TextDistanceInput
			}
			in.AddField(&schema.InputField{Name: contribtype.OpName(v, o), Type: typ, Description: o.Description})
		}
	}
	st.b.Inputs[name] = in
	return name
}

// ensureTextDistanceInput declares TextDistanceInput, the operand of
// levenshteinDistanceLessThan, reporting false when the name is taken.
func (st *build) ensureTextDistanceInput() bool {
	name := contribtype.TextDistanceInput
	if st.extended[name] {
		return true
	}
	if st.nameTaken(name) {
		st.b.Options.Logger.Warn("contrib: type name already taken, fuzzystrmatch operators disabled", "type", name)
		return false
	}
	st.extended[name] = true
	st.b.Inputs[name] = &schema.Input{
		Name:        name,
		Description: "An edit-distance threshold against a reference text: rows whose Levenshtein distance is less than distance match.",
		Fields: []*schema.InputField{
			{Name: "text", Type: "String!", Description: "Reference text, at most 255 characters."},
			{Name: "distance", Type: "Int!", Description: "Exclusive upper bound of the edit distance."},
		},
	}
	return true
}

// ensureIntarrayOps declares IntListIntarrayFilterOps from IntListFilterOps
// plus the intarray operators, reporting false when the name is taken.
func (st *build) ensureIntarrayOps(ia pgcontrib.Vocabulary) bool {
	name := contribtype.IntListIntarrayOps
	if st.extended[name] {
		return true
	}
	base := st.b.Inputs["IntListFilterOps"]
	if base == nil {
		return false
	}
	if st.nameTaken(name) {
		st.b.Options.Logger.Warn("contrib: type name already taken, intarray operators disabled", "type", name)
		return false
	}
	st.extended[name] = true
	in := &schema.Input{
		Name:        name,
		Description: "Int list operators plus intarray's query_int match, which a GIN (gin__int_ops) or GiST (gist__int_ops) index can serve.",
	}
	for _, f := range base.Fields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	for _, o := range ia.Ops {
		in.AddField(&schema.InputField{Name: contribtype.OpName(ia, o), Type: "String", Description: o.Description})
	}
	st.b.Inputs[name] = in
	return true
}

// nameTaken reports whether a schema type of the given name exists.
func (st *build) nameTaken(name string) bool {
	_, in := st.b.Inputs[name]
	_, obj := st.b.Objects[name]
	_, en := st.b.Enums[name]
	return in || obj || en
}

// operandType is the GraphQL type of a vocabulary operator's operand on a
// column.
func operandType(ci contribtype.Column, o pgcontrib.Op) string {
	switch o.Arg {
	case pgcontrib.ArgTextList:
		return "[String!]"
	case pgcontrib.ArgHstore:
		return contribtype.ScalarKeyValueHash
	case pgcontrib.ArgCube:
		return contribtype.ScalarCube
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

// headlineOptionsInput names the shared input of the headline options.
const headlineOptionsInput = "FullTextHeadlineOptions"

// headlineOptionFields are the fields of FullTextHeadlineOptions, keyed as
// pdbcore's contrib.ParseHeadlineOptions reads them.
var headlineOptionFields = []*schema.InputField{
	{Name: "maxWords", Type: "Int", Description: "Longest headline, in words (1 to 100; default 35)."},
	{Name: "minWords", Type: "Int", Description: "Shortest headline, in words (1 to 100, less than maxWords; default 15)."},
	{Name: "shortWord", Type: "Int", Description: "Words of at most this many characters are dropped at the start and end of a headline (0 to 100; default 3)."},
	{Name: "maxFragments", Type: "Int", Description: "Number of fragments to show (0 to 10); 0, the default, shows one headline instead of fragments."},
	{Name: "highlightAll", Type: "Boolean", Description: "Highlight the whole document instead of a headline, ignoring the word and fragment limits."},
	{Name: "startSel", Type: "String", Description: "Markup before each match (1 to 32 bytes, no double quotes, backslashes or control characters; default <b>)."},
	{Name: "stopSel", Type: "String", Description: "Markup after each match (same limits; default </b>)."},
	{Name: "fragmentDelimiter", Type: "String", Description: "Markup between fragments (same limits; default \" ... \")."},
}

// ensureHeadlineOptions declares FullTextHeadlineOptions, reporting false
// when the name is taken.
func (st *build) ensureHeadlineOptions() bool {
	if st.extended[headlineOptionsInput] {
		return true
	}
	if st.nameTaken(headlineOptionsInput) {
		st.b.Options.Logger.Warn("contrib: type name already taken, full-text headlines disabled", "type", headlineOptionsInput)
		return false
	}
	st.extended[headlineOptionsInput] = true
	in := &schema.Input{
		Name:        headlineOptionsInput,
		Description: "ts_headline options of a full-text headline; omitted fields keep PostgreSQL's defaults.",
	}
	for _, f := range headlineOptionFields {
		cp := *f
		in.Fields = append(in.Fields, &cp)
	}
	st.b.Inputs[headlineOptionsInput] = in
	return true
}

// addHeadlineField adds <field>Headline(document:, options:): String next
// to a tsvector column, over an enum of the text columns the table's object
// type exposes (<Type>HeadlineDocument). Nothing is added when the table has no such
// column or a name is taken.
func (st *build) addHeadlineField(t *introspect.Table, c *introspect.Column) {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	obj := b.Objects[typeName]
	if obj == nil {
		return
	}
	name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}) + "Headline"
	if obj.Field(name) != nil {
		return
	}
	enumName := typeName + "HeadlineDocument"
	if !st.extended[enumName] {
		docs := map[string]*introspect.Column{}
		en := &schema.Enum{Name: enumName, Description: "Text columns of " + typeName + " a full-text headline can highlight."}
		readable := map[*introspect.Column]bool{}
		for _, f := range obj.Fields {
			if f.Meta != nil && f.Meta.Kind == schema.KindColumn && f.Meta.Column != nil {
				readable[f.Meta.Column] = true
			}
		}
		for _, d := range t.Columns {
			if !readable[d] || !pgcontrib.IsText(d.PGType) || d.IsArray || d.EnumRef != "" || omit.Column(d, omit.Read) {
				continue
			}
			v := coreinflect.EnumValue(d.Name)
			if _, dup := docs[v]; dup {
				continue
			}
			docs[v] = d
			en.Values = append(en.Values, schema.EnumValue{Name: v, Column: d.Name, Description: "The " + d.Name + " column."})
		}
		if len(en.Values) == 0 {
			return
		}
		if st.nameTaken(enumName) {
			b.Options.Logger.Warn("contrib: type name already taken, full-text headlines skipped", "type", enumName)
			return
		}
		if !st.ensureHeadlineOptions() {
			return
		}
		st.extended[enumName] = true
		b.Enums[enumName] = en
		st.documents[enumName] = docs
	}
	docs := st.documents[enumName]
	if docs == nil {
		return
	}
	maxDoc := b.Options.MaxHeadlineDocument
	if maxDoc < 1 {
		maxDoc = pgcontrib.MaxHeadlineDocument
	}
	obj.AddField(&schema.Field{
		Name: name,
		Type: "String",
		Args: []schema.Arg{
			{Name: "document", Type: enumName + "!", Description: "The text column to highlight."},
			{Name: "options", Type: headlineOptionsInput, Description: "ts_headline options; PostgreSQL's defaults when omitted."},
		},
		Description: "Highlighted snippet (ts_headline) of the document column against the first matches* operator on " + c.Name +
			" in the filter of the list this row was read through, the same match " + strings.TrimSuffix(name, "Headline") + "Rank ranks against; null without one.",
		Meta: &schema.FieldMeta{
			Kind: schema.KindFullTextHeadline, Table: t, Column: c,
			Nested:  map[string]any{"documents": docs, "max_document": maxDoc},
			RowCost: pgcontrib.HeadlineDocumentCost(maxDoc),
		},
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
	levenshtein := map[string]bool{}
	refs := map[string][]vectorColumn{}
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
			arg := cc.ci.ReferenceArg()
			refs[arg] = append(refs[arg], vectorColumn{
				column: cc, vocab: dv,
				ordered: ordered && filterable[cc.col.Name],
				scored:  cc.ci.Owned() || st.served(t, cc.col, hints),
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
		added := false
		if fz, ok := cc.ci.Fuzzy(); ok && len(fz.Ops) > 0 && st.served(t, cc.col, fz.Ops[0].Index) {
			if e, ok := fz.Expr("levenshteinDistance"); ok && ordered {
				base := coreinflect.EnumValue(cc.col.Name) + "_LEVENSHTEIN_DISTANCE"
				desc := "Order by fuzzystrmatch Levenshtein distance to the similarTo: reference text for this column"
				addValue(en, base+"_ASC", schema.EnumValue{
					Column: cc.col.Name, Similarity: e.Name,
					Description: desc + " (nearest first; values over 255 characters last). Requires similarTo:.",
				})
				addValue(en, base+"_DESC", schema.EnumValue{
					Column: cc.col.Name, Similarity: e.Name, Desc: true,
					Description: desc + " (farthest first). Requires similarTo:.",
				})
				added = true
			}
			if st.addScores(t, cc.col, fz, "similarTo") {
				added = true
			}
			levenshtein[cc.col.Name] = added
		}
		trgm, ok := cc.ci.Trgm()
		if !ok {
			if added {
				similar = append(similar, cc.col)
			}
			continue
		}
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
	if len(similar) > 0 && !st.addSimilarToArg(t, enumName, similar, levenshtein) {
		st.dropScores(t, "similarTo")
	}
	for _, arg := range []string{contribtype.NearVectorArg, contribtype.NearPointArg} {
		if len(refs[arg]) > 0 {
			st.addReferenceOrdering(t, enumName, en, arg, refs[arg])
		}
	}
}

// vectorColumn is a column with distances to a per-request reference
// ([contribtype.Column.Distances]): its distance vocabulary, whether the
// policy admits its ordering, and whether it gets distance scores (every
// vector, cube and earth column; a bit or point column only where an index
// or the policy admits the surface, so installing pgvector or earthdistance
// leaves unrelated bit and point columns alone).
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
			Type: scalarFor(e.Type),
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

// referenceKind describes the distance surface of one vocabulary in
// descriptions: the extension it comes from, what its reference is and
// which index serves its orderings.
type referenceKind struct{ family, reference, index string }

// referenceKinds are keyed by vocabulary name.
var referenceKinds = map[string]referenceKind{
	pgcontrib.VocabVector:     {"pgvector", "reference vector", "index-assisted with an HNSW or IVFFlat index built with the matching operator class"},
	pgcontrib.VocabBit:        {"pgvector", "reference vector", "index-assisted with an HNSW or IVFFlat index built with the matching operator class"},
	pgcontrib.VocabCube:       {"cube", "reference cube", "index-assisted with a GiST index"},
	pgcontrib.VocabEarth:      {"earthdistance", "reference location", "index-assisted with a GiST index"},
	pgcontrib.VocabEarthPoint: {"earthdistance", "reference location", "no index serves it"},
}

// addReferenceOrdering adds the distance orderBy values of the columns
// measured against the list argument arg (nearVector: for vector, bit and
// cube columns, nearPoint: for earth and point columns) that the
// indexed-only policy admits them for and the distance score fields of
// every readable one, declares <Type>NearVectorInput or
// <Type>NearPointInput (one reference per such column) and adds arg to
// every field taking the table's orderBy enum. Nothing is added when the
// input's name is taken.
func (st *build) addReferenceOrdering(t *introspect.Table, enumName string, en *schema.Enum, arg string, cols []vectorColumn) {
	b := st.b
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	inputName := typeName + coreinflect.UpperCamel(arg) + "Input"
	if st.nameTaken(inputName) {
		b.Options.Logger.Warn("contrib: type name already taken, distance ordering skipped", "type", inputName)
		return
	}
	if arg == contribtype.NearPointArg && slices.ContainsFunc(cols, func(vc vectorColumn) bool { return vc.scored }) && !b.EnsureLatLonTypes() {
		b.Options.Logger.Warn("contrib: type name already taken, distance ordering skipped", "type", contribtype.LatLonInput)
		return
	}
	desc := "Reference vectors for the pgvector distance orderBy values and distance fields of " + typeName + ", per column."
	if arg == contribtype.NearPointArg {
		desc = "Reference locations for the earthdistance distance orderBy values and distance fields of " + typeName + ", per column."
	} else if slices.ContainsFunc(cols, func(vc vectorColumn) bool { return vc.ci.Kind == contribtype.Cube }) {
		desc = "Reference vectors and cubes for the pgvector and cube distance orderBy values and distance fields of " + typeName + ", per column."
	}
	input := &schema.Input{Name: inputName, Description: desc}
	for _, vc := range cols {
		kind := referenceKinds[vc.vocab.Name]
		added := false
		for _, do := range contribtype.DistanceOrders {
			e, ok := vc.vocab.Expr(do.Expr)
			if !vc.ordered || !ok || !st.served(t, vc.col, e.Index) {
				continue
			}
			base := coreinflect.EnumValue(vc.col.Name) + do.Infix
			desc := "Order by " + kind.family + " " + do.Label + " from the " + arg + ": " + kind.reference + " for this column"
			addValue(en, base+"_ASC", schema.EnumValue{
				Column: vc.col.Name, Vector: do.Expr, Reference: arg,
				Description: desc + " (nearest first; " + kind.index + "). Requires " + arg + ":.",
			})
			addValue(en, base+"_DESC", schema.EnumValue{
				Column: vc.col.Name, Vector: do.Expr, Reference: arg, Desc: true,
				Description: desc + " (farthest first). Requires " + arg + ":.",
			})
			added = true
		}
		if vc.scored && st.addScores(t, vc.col, vc.vocab, arg) {
			added = true
		}
		if !added {
			continue
		}
		fieldDesc := "Reference vector for the " + vc.col.Name + " distance orderBy values and distance fields; it must have the column's dimensions."
		switch {
		case vc.ci.Kind == contribtype.Cube:
			fieldDesc = "Reference cube for the " + vc.col.Name + " distance orderBy values and distance fields."
		case arg == contribtype.NearPointArg:
			fieldDesc = "Reference location for the " + vc.col.Name + " distance orderBy values and distance fields."
		}
		input.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: vc.col.Name}),
			Type:        vc.ci.DistanceInput(),
			Description: fieldDesc,
			Column:      vc.col.Name,
		})
	}
	if len(input.Fields) == 0 {
		return
	}
	b.Inputs[inputName] = input
	for _, f := range st.orderBy[enumName] {
		if !hasArg(f, arg) {
			f.Args = append(f.Args, schema.Arg{Name: arg, Type: inputName})
		}
	}
}

// orderable reports whether the orderBy enum orders by the column, i.e. the
// column passed the indexed-only policy.
func orderable(en *schema.Enum, col string) bool {
	for _, v := range en.Values {
		if v.Column == col && v.Computed == nil && v.Relation == nil && !v.Distance && !v.Rank &&
			v.Similarity == "" && v.Vector == "" && v.Contrib == nil {
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
// similarity or Levenshtein distance ordering or scores, the latter marked
// in levenshtein) and adds similarTo: to every field taking
// the table's orderBy enum, reporting false when the input's name is taken.
func (st *build) addSimilarToArg(t *introspect.Table, enumName string, cols []*introspect.Column, levenshtein map[string]bool) bool {
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
	fuzzy := false
	for _, c := range cols {
		fuzzy = fuzzy || levenshtein[c.Name]
	}
	if fuzzy {
		input.Description = "Reference texts for the pg_trgm similarity and fuzzystrmatch Levenshtein distance orderBy values and fields of " +
			typeName + ", per column."
	}
	for _, c := range cols {
		desc := "Reference text for the " + c.Name + " similarity orderBy values and similarity fields."
		if levenshtein[c.Name] {
			desc = "Reference text for the " + c.Name + " similarity and Levenshtein distance orderBy values and fields."
		}
		input.AddField(&schema.InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        "String",
			Description: desc,
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
