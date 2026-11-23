package schema

import (
	"strings"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbq/internal/inflect"
)

// RowValueSuffix is appended to a table's type name to name the object type
// of its row values nested in other values (see [Builder.RowValueType]).
const RowValueSuffix = "Row"

// ValueType returns the schema and name of the (element) type a value of
// column type c holds, its domain resolved: the key under which the catalog
// lists the composite or table of that type.
func ValueType(c *introspect.Column) (string, string) {
	s := c.BaseTypeSchema
	if s == "" {
		s = c.TypeSchema
	}
	return s, strings.TrimPrefix(c.PGType, "_")
}

// RowTable returns the catalog table whose row type c's (element) type is,
// or nil.
func RowTable(cat *introspect.Catalog, c *introspect.Column) *introspect.Table {
	if c == nil || c.EnumRef != "" {
		return nil
	}
	return cat.Table(ValueType(c))
}

// HiddenRowType reports whether c's (element) type is the row type of a
// table left out of the schema: one a contract or a bare @omit removed from
// the catalog ([introspect.Catalog.RelationHidden]) or a partition (see
// Options.ExposePartitions). Values of such a type have no output field.
func (b *Builder) HiddenRowType(c *introspect.Column) bool {
	s, name := ValueType(c)
	return b.relationHidden(s, name) || b.hidden[tableKey(s, name)]
}

// noOutput reports whether a composite attribute or nested row column c has
// no output field: a raster (whose bytes are never returned) or a value of
// a hidden row type.
func (b *Builder) noOutput(c *introspect.Column) bool {
	return postgis.IsRaster(c.PGType) || b.HiddenRowType(c)
}

// RowValueType returns the object type of t's row values held in another
// value (an array of rows, a composite attribute, a function's output
// column, a column typed as the row type), generating it on first use and
// registering it in TypeForComposite. The type is named after t's type with
// [RowValueSuffix] and has one nullable field per column, bound like a
// composite attribute (KindCompositeField), so nested rows render through
// json_build_object over the columns they expose. Raster columns and
// columns of hidden row types are left out, and the smart_comments plugin
// removes the columns hidden from reads, as it does from t's own type.
// Relations, computed columns and nodeId are not part of it: a nested row
// is a value, not necessarily a row stored in t.
func (b *Builder) RowValueType(t *introspect.Table) string {
	key := tableKey(t.Schema, t.Name)
	if name, ok := b.TypeForComposite[key]; ok {
		return name
	}
	base := b.Inflect(inflect.KindTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	name := base + RowValueSuffix
	for b.tableTypeNames()[name] {
		name += "_"
	}
	name = b.uniqueTypeName(name)
	obj := &Object{Name: name, Description: "The columns of a " + base + " row held in another value."}
	b.Objects[name] = obj
	b.TypeForComposite[key] = name
	for _, c := range t.Columns {
		if b.noOutput(c) {
			continue
		}
		obj.AddField(&Field{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        b.gqlTypeForColumn(c),
			Description: c.Comment,
			Meta:        &FieldMeta{Kind: KindCompositeField, Column: c},
		})
	}
	ensureField(obj)
	return name
}

// tableTypeNames returns the inflected type names of the catalog's tables,
// which row value types must not take: a row value type may be generated
// before the tables are named, and a table keeps its name.
func (b *Builder) tableTypeNames() map[string]bool {
	if b.tableNames == nil {
		b.tableNames = make(map[string]bool, len(b.Catalog.Tables))
		for _, t := range b.Catalog.Tables {
			b.tableNames[b.Inflect(inflect.KindTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})] = true
		}
	}
	return b.tableNames
}

// ensureField gives an object type left without fields the always-null
// placeholder field, since GraphQL object types need at least one.
func ensureField(obj *Object) {
	if len(obj.Fields) > 0 {
		return
	}
	obj.AddField(&Field{
		Name:        PlaceholderField,
		Type:        "Boolean",
		Description: "Always null: every field of this type is hidden.",
		Meta:        &FieldMeta{Kind: KindPlaceholder},
	})
}
