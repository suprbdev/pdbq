// Package schema turns an introspect.Catalog into a GraphQL schema.
//
// The Builder holds a mutable intermediate representation (objects, inputs,
// enums + per-field metadata binding GraphQL fields back to catalog objects).
// SchemaHook plugins mutate the Builder; Build() then renders SDL, validates
// it with gqlparser, and returns the executable schema plus the metadata the
// SQL compiler needs.
package schema

import (
	"github.com/suprbdev/pdbq/internal/introspect"
)

// FieldKind tells the compiler how to resolve a field.
type FieldKind string

const (
	KindColumn           FieldKind = "column"
	KindCompositeField   FieldKind = "composite_field"   // attribute of a composite-type column
	KindRelationForward  FieldKind = "relation_forward"  // many-to-one: FK on this table
	KindRelationBackward FieldKind = "relation_backward" // one-to-many: FK on the other table
	KindListQuery        FieldKind = "list_query"
	KindConnectionQuery  FieldKind = "connection_query"
	KindRowByKey         FieldKind = "row_by_key"
	KindCreate           FieldKind = "create"
	KindUpdate           FieldKind = "update"
	KindDelete           FieldKind = "delete"
	KindUpsert           FieldKind = "upsert"      // INSERT ... ON CONFLICT (KeyColumns) DO UPDATE
	KindCreateMany       FieldKind = "create_many" // multi-row INSERT
	KindUpdateMany       FieldKind = "update_many" // filtered UPDATE
	KindDeleteMany       FieldKind = "delete_many" // filtered DELETE
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
)

// FieldMeta binds a GraphQL field to the catalog for SQL compilation.
type FieldMeta struct {
	Kind       FieldKind
	Table      *introspect.Table      // owning/target table
	Column     *introspect.Column     // for KindColumn
	KeyColumns []string               // lookup / update / delete key
	FK         *introspect.ForeignKey // for relations
	Function   *introspect.Function
	// Nested carries plugin-defined payload (e.g. nested-mutations input shape).
	Nested map[string]any
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
	Name    string
	Type    string
	Default string // SDL literal, empty for none
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
}

// FilterRelation binds a relation filter field to the catalog.
type FilterRelation struct {
	// Forward: the FK lives on the filtered table and points at Table
	// (many-to-one); the field takes Table's filter directly. Otherwise the
	// FK lives on Table and points back at the filtered table (one-to-many);
	// the field takes a {some|none|every} wrapper over Table's filter.
	Forward bool
	FK      *introspect.ForeignKey
	// Table is the related table the EXISTS subquery scans.
	Table *introspect.Table
	// FilterType is Table's <Type>Filter input name.
	FilterType string
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
	// PGValue backs catalog enum values.
	PGValue string
}
