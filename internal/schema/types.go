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
	KindCompositeField   FieldKind = "composite_field"   // attribute of a composite-type column
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
)

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
	// Nested carries plugin-defined payload (e.g. nested-mutations input shape).
	Nested map[string]any
	// PeriodColumn is set on a KindRowByKey point-in-time lookup over a
	// temporal (WITHOUT OVERLAPS) key: KeyColumns holds the key's equality
	// columns and the trailing "at" argument must be contained (@>) in this
	// range column.
	PeriodColumn string
	// Aggregate binds a KindAggregateValue field to its aggregate expression.
	Aggregate *Aggregate
	// ScanCost is a flat surcharge the executor's cost estimate adds for a
	// field that reads a whole filtered row set rather than a page (the
	// aggregates plugin's aggregates and groupedAggregates fields). A
	// list-typed field with a ScanCost also multiplies its selection by the
	// list fan-out assumption, even inside a connection.
	ScanCost int
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
}

func (o *Object) Field(name string) *Field {
	for _, f := range o.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// AddField appends a field, replacing any existing field of the same name.
func (o *Object) AddField(f *Field) {
	for i, ex := range o.Fields {
		if ex.Name == f.Name {
			o.Fields[i] = f
			return
		}
	}
	o.Fields = append(o.Fields, f)
}

// RemoveField deletes a field by name.
func (o *Object) RemoveField(name string) {
	for i, ex := range o.Fields {
		if ex.Name == name {
			o.Fields = append(o.Fields[:i], o.Fields[i+1:]...)
			return
		}
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
	// Relation marks a relation filter field (advanced-filters); the compiler
	// turns it into an EXISTS subquery over the related table.
	Relation *FilterRelation
	// Computed marks a computed-column filter field (advanced-filters): the
	// bound row-type function is called on the current row.
	Computed *introspect.Function
	// Nested marks plugin-added nested-input fields (e.g. nested mutations).
	Nested map[string]any
	// Aggregate marks a column field of a having or relation-aggregate
	// filter input (aggregates plugin): its operators compare the bound
	// aggregate's result.
	Aggregate *Aggregate
}

// OrderRelation binds a relational orderBy enum value to the catalog
// (advanced-filters plugin). The compiler renders it as a correlated scalar
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
}

func (in *Input) Field(name string) *InputField {
	for _, f := range in.Fields {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func (in *Input) AddField(f *InputField) {
	for i, ex := range in.Fields {
		if ex.Name == f.Name {
			in.Fields[i] = f
			return
		}
	}
	in.Fields = append(in.Fields, f)
}

// RemoveField deletes a field by name.
func (in *Input) RemoveField(name string) {
	for i, ex := range in.Fields {
		if ex.Name == name {
			in.Fields = append(in.Fields[:i], in.Fields[i+1:]...)
			return
		}
	}
}

// Enum is a GraphQL enum type.
type Enum struct {
	Name        string
	Description string
	Values      []EnumValue
}

type EnumValue struct {
	Name        string
	Description string
	// Column/Desc back orderBy enum values.
	Column string
	Desc   bool
	// Computed backs orderBy enum values sorting by a computed column
	// (advanced-filters): a row-type function instead of a plain column.
	Computed *introspect.Function
	// Relation backs orderBy enum values sorting by a related table's column
	// (advanced-filters); Column stays empty for these.
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
	// PGValue backs catalog enum values.
	PGValue string
	// GroupBy backs groupBy enum values of groupedAggregates (aggregates
	// plugin).
	GroupBy *GroupBy
}
