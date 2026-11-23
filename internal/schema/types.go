// Package schema turns an introspect.Catalog into a GraphQL schema.
//
// The Builder holds a mutable intermediate representation (objects, inputs,
// enums + per-field metadata binding GraphQL fields back to catalog objects).
// SchemaHook plugins mutate the Builder; Build() then renders SDL, validates
// it with gqlparser, and returns the executable schema plus the metadata the
// SQL compiler needs.
package schema

import (
	"github.com/suprbdev/pdbcore/introspect"
)

// FieldKind tells the compiler how to resolve a field.
type FieldKind string

const (
	KindColumn           FieldKind = "column"
	KindCompositeField   FieldKind = "composite_field"   // attribute of a composite value, or column of a nested row value
	KindRelationForward  FieldKind = "relation_forward"  // many-to-one: FK on this table (a connection for a PERIOD FK)
	KindRelationBackward FieldKind = "relation_backward" // one-to-many: FK on the other table
	KindListQuery        FieldKind = "list_query"
	KindConnectionQuery  FieldKind = "connection_query"
	KindRowByKey         FieldKind = "row_by_key"
	KindCreate           FieldKind = "create"
	KindUpdate           FieldKind = "update"
	KindDelete           FieldKind = "delete"
	KindUpsert           FieldKind = "upsert"        // INSERT ... ON CONFLICT (KeyColumns) DO UPDATE
	KindCreateMany       FieldKind = "create_many"   // multi-row INSERT
	KindUpdateMany       FieldKind = "update_many"   // filtered UPDATE
	KindDeleteMany       FieldKind = "delete_many"   // filtered DELETE
	KindPayloadRows      FieldKind = "payload_rows"  // list of mutated rows in a bulk payload
	KindPayloadCount     FieldKind = "payload_count" // affected-row count in a bulk payload
	KindFunction         FieldKind = "function"
	KindComputed         FieldKind = "computed"           // stable function on a row type -> field on that type
	KindPayloadRow       FieldKind = "payload_row"        // the row field inside a mutation payload
	KindPayloadResult    FieldKind = "payload_result"     // the result field inside a function-mutation payload
	KindClientMutationID FieldKind = "client_mutation_id" // Relay-classic clientMutationId passthrough
	KindSynthetic        FieldKind = "synthetic"          // handled by the executor (e.g. connection internals)
	KindNodeID           FieldKind = "node_id"            // nodeId: ID! global identifier on a row type
	KindNode             FieldKind = "node"               // Query.node(nodeId: ID!) global lookup
	// KindSpatialDerived is a computed PostGIS accessor on a spatial column
	// (postgis plugin): <field>Area, <field>Centroid, ... The accessor is
	// named by Meta.Nested["suffix"] and compiles to a PostGIS call over
	// Meta.Column in the same statement.
	KindSpatialDerived FieldKind = "spatial_derived"
	// KindFullTextRank is the <field>Rank: Float sibling of a tsvector
	// column (contrib plugin): the full-text rank of Meta.Column against the
	// first matches* operator on it in the enclosing list field's filter,
	// null without one.
	KindFullTextRank FieldKind = "fulltext_rank"
	// KindFullTextHeadline is the <field>Headline(document:, options:):
	// String sibling of a tsvector column (contrib plugin): ts_headline of
	// the document column the document: argument names (Meta.Nested
	// "documents" maps its enum values to columns) against the same match
	// a KindFullTextRank field ranks against, null without one.
	KindFullTextHeadline FieldKind = "fulltext_headline"
	// KindScore is a selectable score of Meta.Column measured against the
	// reference the enclosing list field takes for it (Meta.Score): the
	// <field>Distance of a spatial column from near: (postgis plugin), the
	// <field>Similarity family of a text column against similarTo: and the
	// <field><Metric> distances of a vector, bit or cube column from
	// nearVector: and of an earth or point column from nearPoint: (contrib
	// plugin). Null without the reference, and for rows not read through
	// such a list.
	KindScore FieldKind = "score"
	// KindContribAccessor is an operand-free contrib accessor of
	// Meta.Column (contrib plugin): <field>Nlevel and <field>Parent of an
	// ltree column, <field>Lca of an ltree[] column, <field>Icount of an
	// int4[] column with intarray. Meta.Contrib names it.
	KindContribAccessor FieldKind = "contrib_accessor"
	// KindContribDerived is a contrib value of Meta.Column computed from
	// the field's own arguments (contrib plugin): <field>Value(key:) of an
	// hstore column, <field>Lca(path:) and <field>Subpath(offset:, length:)
	// of an ltree column. Meta.Contrib names the expression; the arguments
	// are bind parameters.
	KindContribDerived FieldKind = "contrib_derived"
	// KindRasterAccessor is a metadata accessor of a postgis_raster column
	// (postgis plugin): <field>Width, <field>Srid, <field>Envelope, ...,
	// named by Meta.Nested["suffix"] (see postgis.RasterAccessors).
	KindRasterAccessor FieldKind = "raster_accessor"
	// KindRasterValue is <field>Value(point:, band:) of a raster column
	// (postgis plugin): the pixel value of a band at a point (ST_Value).
	KindRasterValue FieldKind = "raster_value"
	// KindRasterSummaryStats is <field>SummaryStats(band:) of a raster
	// column (postgis plugin): the RasterSummaryStats object of a band
	// (ST_SummaryStats).
	KindRasterSummaryStats FieldKind = "raster_summary_stats"
	// KindAggregates is the aggregates field of a connection (aggregates
	// plugin): an object of aggregate groups computed over the connection's
	// filtered row set, ignoring pagination.
	KindAggregates FieldKind = "aggregates"
	// KindGroupedAggregates is the groupedAggregates(groupBy:, having:)
	// field of a connection (aggregates plugin): one aggregates object per
	// group of the filtered row set.
	KindGroupedAggregates FieldKind = "grouped_aggregates"
	// KindAggregateKeys is the keys field of an aggregates object: the
	// stringified group keys, null outside groupedAggregates.
	KindAggregateKeys FieldKind = "aggregate_keys"
	// KindAggregateGroup is one aggregate group of an aggregates object
	// (sum, average, ...), an object of KindAggregateValue fields.
	KindAggregateGroup FieldKind = "aggregate_group"
	// KindAggregateValue is one aggregated column; Meta.Aggregate binds the
	// expression.
	KindAggregateValue FieldKind = "aggregate_value"
	// KindListen is Subscription.listen(topic:, initialEvent:), the
	// PostGraphile simple subscription over LISTEN/NOTIFY; it is served by
	// the subscription transport, never compiled.
	KindListen FieldKind = "listen"
	// KindListenQuery is ListenPayload.query: the Query root, resolved per
	// event under the subscriber's identity.
	KindListenQuery FieldKind = "listen_query"
	// KindListenRelatedNode is ListenPayload.relatedNode: the node named by
	// the notification payload's __node__, resolved per event like
	// Query.node.
	KindListenRelatedNode FieldKind = "listen_related_node"
	// KindListenRelatedNodeID is ListenPayload.relatedNodeId: the nodeId
	// encoded from the payload's __node__.
	KindListenRelatedNodeID FieldKind = "listen_related_node_id"
	// KindPayloadQuery is the query: Query field of a mutation payload; its
	// selections compile like root query fields, in the same statement.
	KindPayloadQuery FieldKind = "payload_query"
	// KindPayloadDeletedNodeID is the deleted<Type>NodeId field of a delete
	// payload: the nodeId of the row the mutation deleted.
	KindPayloadDeletedNodeID FieldKind = "payload_deleted_node_id"
	// KindPayloadEdge is the <row>Edge field of a payload carrying one row of
	// Table: an edge whose cursor is the row's nodeId.
	KindPayloadEdge FieldKind = "payload_edge"
	// KindPlaceholder is the always-null field ([PlaceholderField]) standing
	// in for an object type whose every field is hidden by smart comments,
	// since GraphQL object types need at least one field.
	KindPlaceholder FieldKind = "placeholder"
)

// PlaceholderField names the [KindPlaceholder] field.
const PlaceholderField = "_empty"

// FieldMeta binds a GraphQL field to the catalog for SQL compilation.
type FieldMeta struct {
	Kind       FieldKind
	Table      *introspect.Table      // owning/target table
	Column     *introspect.Column     // for KindColumn
	KeyColumns []string               // lookup / update / delete key
	FK         *introspect.ForeignKey // for relations
	Function   *introspect.Function
	// CostMultiplier overrides the executor's page-size fan-out assumption for
	// this field (the @costMultiplier smart comment). Zero means "no override":
	// the cost estimator falls back to the requested first/last. It exists
	// because the default estimate is geometric in nesting depth (page^depth),
	// which is correct for a cartesian fan-out but wildly pessimistic for
	// self-referential trees (comment threads) where most rows have few
	// children. See exec.measureSel.
	CostMultiplier int
	// Nested carries plugin-defined payload (e.g. nested_mutations input shape).
	Nested map[string]any
	// PeriodColumn is set on a KindRowByKey point-in-time lookup over a
	// temporal (WITHOUT OVERLAPS) key: KeyColumns holds the key's equality
	// columns and the trailing "at" argument must be contained (@>) in this
	// range column.
	PeriodColumn string
	// Aggregate binds a KindAggregateValue field to its aggregate expression.
	Aggregate *Aggregate
	// Patch names the table's patch input (<Type>Patch) on a KindUpsert
	// field: only the columns it binds are overwritten on conflict, so a
	// column the patch leaves out (`@omit update`) keeps its value.
	Patch string
	// Values names the field of a table mutation's input argument holding
	// the row values: the row (<Type>Input) of a create or upsert, the rows
	// of a bulk create, the patch (<Type>Patch) of an update or bulk
	// update. It is empty for deletes and for a create without settable
	// columns, which inserts the column defaults. The key fields of a keyed
	// update or delete are the input's fields bound to KeyColumns
	// ([Built.InputColumns]).
	Values string
	// Score binds a KindScore field to its reference argument and
	// expression.
	Score *Score
	// Contrib binds a KindContribAccessor or KindContribDerived field to
	// the contrib value it reads.
	Contrib *ContribValue
	// ScanCost is a flat surcharge the executor's cost estimate adds for a
	// field that reads a whole filtered row set rather than a page (the
	// aggregates plugin's aggregates and groupedAggregates fields). A
	// list-typed field with a ScanCost also multiplies its selection by the
	// list fan-out assumption, even inside a connection.
	ScanCost int
	// RowCost is a surcharge the executor's cost estimate adds each time
	// the field is evaluated (per row it is read for), for values that are
	// expensive to compute (full-text headlines).
	RowCost int
	// ArgCost maps arguments to a surcharge added per evaluation, like
	// RowCost, when the argument has a non-null value (the spatial buffer:
	// argument).
	ArgCost map[string]int
}

// ContribValue names a contrib value of a column: an operand-free accessor
// (ltree nlevel, intarray icount) or a derived expression taking an operand
// (hstore value, ltree lca and subpath) of one of the column's pdbcore
// vocabularies. It binds output fields, table-filter fields and orderBy
// values (contrib plugin).
type ContribValue struct {
	// Column is the column the value derives from.
	Column string
	// Vocab is the pdbcore vocabulary name (contrib.VocabLtree, ...).
	Vocab string
	// Name is the accessor or, with Expr, the expression name.
	Name string
	// Expr marks Name as an expression ([contrib.Expr.Derived]) rather
	// than an accessor.
	Expr bool
	// Arg is the argument holding an Expr's text operand: the field's own
	// argument on a KindContribDerived field ("key", "path"; empty for
	// subpath, which takes offset and length), the list field argument
	// holding it per column on an orderBy value ("hstoreKey").
	Arg string
}

// Score binds a [KindScore] field.
type Score struct {
	// Arg is the list field argument holding the reference: "near",
	// "similarTo", "nearVector" or "nearPoint".
	Arg string
	// Expr is the pdbcore contrib expression measured ("similarity",
	// "cosineDistance", ...); empty for the spatial distance.
	Expr string
}

// Aggregate binds an aggregate expression over a table's rows (aggregates
// plugin): an aggregate output field, a having or relation-aggregate filter
// field, or a relational aggregate orderBy value.
type Aggregate struct {
	// Spec is the aggregate's id: "count", "sum", "distinctCount", "min",
	// "max", "average", "stddevSample", "stddevPopulation",
	// "varianceSample", "variancePopulation", or a spatial "stExtent",
	// "stUnion", "stCollect", "stCentroid".
	Spec string
	// Column is the aggregated column, nil for an aggregate over whole rows
	// (count).
	Column *introspect.Column
	// SQL renders the aggregate over a column reference ("" when Column is
	// nil). It must not embed user input: the compiled SQL text stays a pure
	// function of the query shape.
	SQL func(ref string) string
	// ResultType is the pg_catalog type name of the aggregate's result; it
	// drives the JSON encoding of the value and the coercion of filter
	// operands compared against it.
	ResultType string
	// Cost is the cost-estimate surcharge for evaluating the aggregate in a
	// filter or orderBy position, where it runs once per candidate row.
	Cost int
}

// ResultColumn describes the aggregate's result as a column, the shape the
// compiler's value encoding and filter operators work with.
func (a *Aggregate) ResultColumn() *introspect.Column {
	return &introspect.Column{Name: a.Spec, PGType: a.ResultType, TypeSchema: "pg_catalog"}
}

// GroupBy binds a groupBy enum value of groupedAggregates (aggregates
// plugin) to its grouping expression.
type GroupBy struct {
	// Column is the grouped column.
	Column *introspect.Column
	// SQL wraps the column reference in a derivative (date_trunc for the
	// _TRUNCATED_TO_HOUR/_DAY values); nil groups by the column itself.
	SQL func(ref string) string
}

// Field is one field on an object type.
type Field struct {
	Name        string
	Type        string // SDL type reference, e.g. "String!", "[User!]!"
	Description string
	// Deprecated carries the @deprecated(reason:) directive when non-empty.
	Deprecated string
	Args       []Arg
	Meta       *FieldMeta
}

type Arg struct {
	Name        string
	Type        string
	Default     string // SDL literal, empty for none
	Description string
}

// Object is an output object type (or interface when IsInterface is set).
type Object struct {
	Name        string
	Description string
	Fields      []*Field
	// Interfaces lists interface names this object implements.
	Interfaces []string
	// IsInterface renders the type as `interface X` instead of `type X`.
	IsInterface bool

	idx nameIndex
}

// nameIndex maps names to slice positions for the Fields and Values slices
// of the IR types. It is built lazily and extended on lookup, so plugins may
// keep appending to or truncating the slice directly: growth is indexed
// incrementally, a shorter slice triggers a rebuild, and a stale hit is
// re-verified against the slice. The first element of a name wins, like a
// linear scan. Replacing an element in place with a differently named one
// without changing the length is not detected.
type nameIndex struct {
	pos map[string]int
	n   int
}

func (ix *nameIndex) reset() {
	ix.pos = nil
	ix.n = 0
}

func (ix *nameIndex) sync(n int, nameAt func(int) string) {
	if ix.pos == nil || n < ix.n {
		ix.pos = make(map[string]int, n)
		ix.n = 0
	}
	for ; ix.n < n; ix.n++ {
		name := nameAt(ix.n)
		if _, ok := ix.pos[name]; !ok {
			ix.pos[name] = ix.n
		}
	}
}

func (ix *nameIndex) find(n int, nameAt func(int) string, name string) int {
	ix.sync(n, nameAt)
	i, ok := ix.pos[name]
	if !ok {
		return -1
	}
	if i < n && nameAt(i) == name {
		return i
	}
	ix.reset()
	ix.sync(n, nameAt)
	if i, ok := ix.pos[name]; ok {
		return i
	}
	return -1
}

// Field returns the field with the given name, or nil.
func (o *Object) Field(name string) *Field {
	if i := o.idx.find(len(o.Fields), func(i int) string { return o.Fields[i].Name }, name); i >= 0 {
		return o.Fields[i]
	}
	return nil
}

// AddField appends a field, replacing any existing field of the same name.
func (o *Object) AddField(f *Field) {
	if i := o.idx.find(len(o.Fields), func(i int) string { return o.Fields[i].Name }, f.Name); i >= 0 {
		o.Fields[i] = f
		return
	}
	o.Fields = append(o.Fields, f)
}

// RemoveField deletes a field by name.
func (o *Object) RemoveField(name string) {
	if i := o.idx.find(len(o.Fields), func(i int) string { return o.Fields[i].Name }, name); i >= 0 {
		o.Fields = append(o.Fields[:i], o.Fields[i+1:]...)
		o.idx.reset()
	}
}

// InputField is one field on an input object type.
type InputField struct {
	Name        string
	Type        string
	Description string
	Default     string
	// Column links the input field to a table column for mutation compilation.
	Column string
	// Relation marks a relation filter field (advanced_filters); the compiler
	// turns it into an EXISTS subquery over the related table.
	Relation *FilterRelation
	// Computed marks a computed-column filter field (advanced_filters): the
	// bound row-type function is called on the current row.
	Computed *introspect.Function
	// Nested marks plugin-added nested-input fields (e.g. nested mutations).
	Nested map[string]any
	// Aggregate marks a column field of a having or relation-aggregate
	// filter input (aggregates plugin): its operators compare the bound
	// aggregate's result.
	Aggregate *Aggregate
	// Contrib marks a table-filter field comparing a contrib value of a
	// column (contrib plugin): <field>Nlevel, <field>Icount, <field>Value.
	// Column stays empty for these.
	Contrib *ContribValue
}

// OrderRelation binds a relational orderBy enum value to the catalog
// (advanced_filters plugin). The compiler renders it as a correlated scalar
// subquery over the related table.
type OrderRelation struct {
	// Forward: the FK lives on the ordered table and points at Table
	// (many-to-one); the subquery selects Column from the single referenced
	// row, or aggregates it like a reverse relation for a PERIOD FK.
	// Otherwise the FK lives on Table and points back at the ordered
	// table (one-to-many); the subquery aggregates Column over the
	// referencing rows (MIN for ASC values, MAX for DESC).
	Forward bool
	FK      *introspect.ForeignKey
	// Table is the related table the subquery scans.
	Table *introspect.Table
	// Column is the sort column on Table.
	Column string
	// Aggregate, when set, sorts by this aggregate over the related rows
	// (aggregates plugin) instead of MIN/MAX of Column.
	Aggregate *Aggregate
}

// FilterRelation binds a relation filter field to the catalog.
type FilterRelation struct {
	// Forward: the FK lives on the filtered table and points at Table
	// (many-to-one); the field takes Table's filter directly, or a
	// {some|none|every} wrapper for a PERIOD FK. Otherwise the
	// FK lives on Table and points back at the filtered table (one-to-many);
	// the field takes a {some|none|every} wrapper over Table's filter.
	Forward bool
	FK      *introspect.ForeignKey
	// Table is the related table the EXISTS subquery scans.
	Table *introspect.Table
	// FilterType is Table's <Type>Filter input name.
	FilterType string
	// Exists marks the Boolean existence variant (<rel>Exists / <rel>Exist):
	// the field takes true/false instead of a filter, compiling to a bare
	// (NOT) EXISTS over the relation join.
	Exists bool
}

// Input is an input object type.
type Input struct {
	Name        string
	Description string
	Fields      []*InputField

	idx nameIndex
}

// Field returns the field with the given name, or nil.
func (in *Input) Field(name string) *InputField {
	if i := in.idx.find(len(in.Fields), func(i int) string { return in.Fields[i].Name }, name); i >= 0 {
		return in.Fields[i]
	}
	return nil
}

// AddField appends a field, replacing any existing field of the same name.
func (in *Input) AddField(f *InputField) {
	if i := in.idx.find(len(in.Fields), func(i int) string { return in.Fields[i].Name }, f.Name); i >= 0 {
		in.Fields[i] = f
		return
	}
	in.Fields = append(in.Fields, f)
}

// RemoveField deletes a field by name.
func (in *Input) RemoveField(name string) {
	if i := in.idx.find(len(in.Fields), func(i int) string { return in.Fields[i].Name }, name); i >= 0 {
		in.Fields = append(in.Fields[:i], in.Fields[i+1:]...)
		in.idx.reset()
	}
}

// Enum is a GraphQL enum type.
type Enum struct {
	Name        string
	Description string
	Values      []EnumValue

	idx nameIndex
}

// HasValue reports whether the enum has a value with the given name.
func (e *Enum) HasValue(name string) bool {
	return e.idx.find(len(e.Values), func(i int) string { return e.Values[i].Name }, name) >= 0
}

type EnumValue struct {
	Name        string
	Description string
	// Column/Desc back orderBy enum values.
	Column string
	Desc   bool
	// Computed backs orderBy enum values sorting by a computed column
	// (advanced_filters): a row-type function instead of a plain column.
	Computed *introspect.Function
	// Relation backs orderBy enum values sorting by a related table's column
	// (advanced_filters); Column stays empty for these.
	Relation *OrderRelation
	// Distance marks orderBy enum values sorting by spatial distance from
	// the reference geometry in the field's near: argument (postgis plugin).
	// Column names the spatial column being measured from.
	Distance bool
	// Rank marks orderBy enum values sorting by full-text rank of Column
	// against the field's matches* filter on it (contrib plugin).
	Rank bool
	// Similarity marks orderBy enum values sorting by a pg_trgm distance
	// expression ("distance", "wordDistance", "strictWordDistance") of
	// Column from the field's similarTo: reference (contrib plugin).
	Similarity string
	// Vector marks orderBy enum values sorting by a distance expression
	// ("l2Distance", "cosineDistance", "earthDistance", ...) of Column from
	// the field's per-column reference in the Reference argument (contrib
	// plugin).
	Vector string
	// Reference names the list argument holding Vector's reference:
	// "nearVector" (pgvector and cube distances) or "nearPoint"
	// (earthdistance distances).
	Reference string
	// Contrib marks orderBy enum values sorting by a contrib value of
	// Column (contrib plugin): an accessor (nlevel, icount) or a derived
	// expression whose operand the list field's Contrib.Arg argument
	// holds per column (hstore value against hstoreKey:).
	Contrib *ContribValue
	// PGValue backs catalog enum values.
	PGValue string
	// GroupBy backs groupBy enum values of groupedAggregates (aggregates
	// plugin).
	GroupBy *GroupBy
}
