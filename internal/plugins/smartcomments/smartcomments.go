// Package smartcomments is a built-in plugin that turns PostGraphile-style
// "smart comments" — database COMMENTs whose leading lines start with '@' —
// into schema customization, so the generated API can be tuned with plain DDL
// and no pdbq configuration:
//
//	COMMENT ON TABLE app.people_raw IS E'@name people\n@omit update,delete';
//	COMMENT ON COLUMN app.users.hashed_password IS '@omit';
//	COMMENT ON CONSTRAINT posts_author_id_fkey ON app.posts IS '@fieldName writer';
//	COMMENT ON VIEW app.metrics IS E'@primaryKey name\n@unique label';
//	COMMENT ON FUNCTION app.users_score(app.users) IS '@filterable';
//
// Supported tags (see docs/plugins.md for the full reference):
//
//   - @omit [actions] — on tables (bare, read, all, many, create, update,
//     delete, filter, order), columns (bare, read, create, update, filter,
//     order), foreign keys (bare, many, filter), unique constraints (bare),
//     and functions (bare, plus filter/order for computed columns, honored by
//     advanced-filters).
//   - @name <new_name> — rename a table, column, enum, domain, or function;
//     the value replaces the pg name at the start of the inflection
//     pipeline, so every derived name (type, queries, mutations, filters)
//     follows.
//   - @fieldName / @foreignFieldName <name> — on a foreign key: exact GraphQL
//     name for the forward (child -> parent) / backward (parent -> children)
//     relation field; also picked up by advanced-filters and nested-mutations.
//   - @deprecated [reason] — on columns and functions: marks the generated
//     output fields with @deprecated(reason:).
//   - @notNull / @nullable — on columns: override introspected nullability
//     (views lose NOT NULL information).
//   - @filterable / @sortable — on columns: opt in to filtering and ordering
//     despite filters.indexed_only; on foreign keys / computed functions:
//     opt in when advanced-filters runs in opt-in mode.
//   - @primaryKey col[, col] — on a view (or PK-less table): declare a
//     logical primary key, enabling nodeId, single-row lookups, and keyset
//     pagination. Mutations stay gated by real privileges.
//   - @unique col[, col] — on tables/views: declare a logical unique
//     constraint, generating a single-row lookup field (repeatable).
//   - @foreignKey (col, ...) references [schema.]table (col, ...) — on
//     tables/views: declare a logical foreign key, generating relation fields
//     both ways (repeatable; append |@fieldName x|@foreignFieldName y to name
//     them).
//   - @enum / @enumDescription — turn a table's rows into a GraphQL enum.
//     Handled at introspection time (introspect.convertEnumTables), not by
//     this plugin, because it needs the table's rows; @name on the table
//     still renames the resulting enum through this plugin's InflectionHook.
//
// Tag lines are stripped from the GraphQL descriptions; the rest of the
// comment remains the description. Everything happens at schema-build time —
// the compiler is untouched, and disabling the plugin
// (plugins.disabled: [smart-comments]) restores the raw schema.
package smartcomments

import (
	"context"
	"log/slog"
	"strings"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/smartcomments/catalog"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Plugin embeds pdbcore's catalog half (tag index + CatalogHook) and adds
// the GraphQL naming (InflectionHook) and surface (SchemaHook) halves.
type Plugin struct {
	*catalog.Hook
	log *slog.Logger
}

func New() *Plugin { return &Plugin{Hook: catalog.New(), log: slog.Default()} }

// Priority 50: the CatalogHook shapes the catalog before other plugins read
// it, the InflectionHook applies @name before naming plugins (simple-names,
// 100) format, and the SchemaHook removes omitted filter/orderBy types before
// advanced-filters (150) references them.
func (p *Plugin) Priority() int { return catalog.Priority }

func tableKey(schema, name string) string { return schema + "." + name }

// ---- InflectionHook ----

// Inflect applies @name / @fieldName / @foreignFieldName at the start of the
// naming pipeline: renames replace the pg identifier in the Input and the
// chain continues, so downstream naming plugins (simple-names) and the
// default inflector all derive from the new name.
func (p *Plugin) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	switch kind {
	case inflect.KindRelationForward:
		if name := p.Index().Constraint(in.Schema, in.Constraint).First("fieldName"); name != "" {
			return name
		}
	case inflect.KindRelationForwardMany:
		if name := p.Index().Constraint(in.Schema, in.Constraint).First("fieldName"); name != "" {
			return name
		}
		in.Table = p.Index().RenamedTable(in.Schema, in.Table)
	case inflect.KindRelationBackward:
		if name := p.Index().Constraint(in.Schema, in.Constraint).First("foreignFieldName"); name != "" {
			return name
		}
		// in.Table/in.Columns identify the child table here.
		in.Columns = p.Index().RenamedColumns(in.Schema, in.Table, in.Columns)
		in.Table = p.Index().RenamedTable(in.Schema, in.Table)
	case inflect.KindTypeName, inflect.KindAllRowsField, inflect.KindFilterTypeName,
		inflect.KindConditionTypeName,
		inflect.KindOrderByTypeName, inflect.KindCreateMutation, inflect.KindCreateInput,
		inflect.KindUpdateInput, inflect.KindPayloadTypeName,
		inflect.KindCreateManyMutation, inflect.KindUpdateManyMutation, inflect.KindDeleteManyMutation:
		in.Table = p.Index().RenamedTable(in.Schema, in.Table)
	case inflect.KindRowByPKField, inflect.KindRowByUniqueField,
		inflect.KindUpdateMutation, inflect.KindDeleteMutation,
		inflect.KindUpdateByUniqueMutation, inflect.KindDeleteByUniqueMutation,
		inflect.KindUpsertMutation, inflect.KindUpsertByUniqueMutation:
		in.Columns = p.Index().RenamedColumns(in.Schema, in.Table, in.Columns)
		in.Table = p.Index().RenamedTable(in.Schema, in.Table)
	case inflect.KindFieldName:
		in.Column = p.Index().RenamedColumn(in.Schema, in.Table, in.Column)
	case inflect.KindEnumTypeName:
		if name := p.Index().Enum(in.Schema, in.Enum).First("name"); name != "" {
			in.Enum = name
		}
	case inflect.KindDomainTypeName:
		if name := p.Index().Domain(in.Schema, in.Domain).First("name"); name != "" {
			in.Domain = name
		}
	case inflect.KindFunctionField, inflect.KindFunctionInput, inflect.KindFunctionPayload,
		inflect.KindComputedField, inflect.KindFunctionRecord:
		if name := p.Index().Function(in.Schema, in.Function).First("name"); name != "" {
			in.Function = name
		}
	}
	return next(kind, in)
}

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	if b.Options.Logger != nil {
		p.log = b.Options.Logger
		p.SetLogger(b.Options.Logger)
	}
	for _, t := range b.Catalog.Tables {
		p.applyTableOmits(b, t)
		p.applyColumnTags(b, t)
		p.applyRelationOmits(b, t)
	}
	p.applyFunctionTags(b)
	stripDescriptions(b)
	return nil
}

// applyTableOmits removes generated surface per the table's @omit actions
// that have no catalog-level representation.
func (p *Plugin) applyTableOmits(b *schema.Builder, t *introspect.Table) {
	omits, _ := p.Index().Table(t.Schema, t.Name).Omits()
	if len(omits) == 0 {
		return
	}
	if omits["read"] || omits["all"] {
		kept := b.Query.Fields[:0]
		for _, f := range b.Query.Fields {
			drop := f.Meta != nil && f.Meta.Table == t &&
				(f.Meta.Kind == schema.KindConnectionQuery ||
					(omits["read"] && f.Meta.Kind == schema.KindRowByKey))
			if !drop {
				kept = append(kept, f)
			}
		}
		b.Query.Fields = kept
	}
	if omits["many"] {
		removeFields(b, func(f *schema.Field) bool {
			return f.Meta != nil && f.Meta.Kind == schema.KindRelationBackward && f.Meta.Table == t
		})
	}
	if omits["filter"] {
		removeInputType(b, b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name}))
		removeInputType(b, b.Inflect(inflect.KindConditionTypeName, inflect.Input{Schema: t.Schema, Table: t.Name}))
	}
	if omits["order"] {
		name := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		delete(b.Enums, name)
		removeArgsOfType(b, name)
	}
}

// applyColumnTags handles per-column @omit, @deprecated, and the orderBy enum
// value rename for @name (the builder derives those value names from the raw
// pg column name, outside the inflection pipeline).
func (p *Plugin) applyColumnTags(b *schema.Builder, t *introspect.Table) {
	obj := b.Objects[b.TypeForTable[tableKey(t.Schema, t.Name)]]
	createIn := b.Inputs[b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: t.Schema, Table: t.Name})]
	updateIn := b.Inputs[b.Inflect(inflect.KindUpdateInput, inflect.Input{Schema: t.Schema, Table: t.Name})]
	filterIn := b.Inputs[b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	conditionIn := b.Inputs[b.Inflect(inflect.KindConditionTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]
	orderEnum := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]

	for _, c := range t.Columns {
		tags := p.Index().Column(t.Schema, t.Name, c.Name)
		if tags == nil {
			continue
		}
		omits, everything := tags.Omits()
		omitted := func(action string) bool { return everything || omits[action] }

		if omitted("read") && obj != nil {
			p.removeColumnField(b, obj, c)
		}
		if omitted("create") && createIn != nil {
			removeInputColumn(createIn, c.Name)
		}
		if omitted("update") && updateIn != nil {
			removeInputColumn(updateIn, c.Name)
		}
		if omitted("filter") && filterIn != nil {
			removeInputColumn(filterIn, c.Name)
		}
		if omitted("filter") && conditionIn != nil {
			removeInputColumn(conditionIn, c.Name)
		}
		if omitted("order") && orderEnum != nil {
			removeOrderValues(orderEnum, c.Name)
		}
		if tags.Has("deprecated") && obj != nil {
			if f := columnField(obj, c); f != nil {
				f.Deprecated = deprecationReason(tags)
			}
		}
		if newName := tags.First("name"); newName != "" && orderEnum != nil {
			renameOrderValues(orderEnum, c.Name, newName)
		}
	}
	// A condition input holds only column fields (no and/or/not), so omitting
	// every column would leave an empty input type — invalid SDL. Drop it.
	if conditionIn != nil && len(conditionIn.Fields) == 0 {
		removeInputType(b, conditionIn.Name)
	}
}

// applyRelationOmits handles @omit many on foreign keys (bare @omit is
// resolved at the catalog level; filter omission is advanced-filters' side).
func (p *Plugin) applyRelationOmits(b *schema.Builder, t *introspect.Table) {
	for _, fk := range t.ForeignKeys {
		omits, _ := p.Index().Constraint(t.Schema, fk.Name).Omits()
		if omits["many"] {
			removeFields(b, func(f *schema.Field) bool {
				return f.Meta != nil && f.Meta.Kind == schema.KindRelationBackward && f.Meta.FK == fk
			})
		}
	}
}

// applyFunctionTags marks generated function fields (root and computed)
// deprecated.
func (p *Plugin) applyFunctionTags(b *schema.Builder) {
	for _, fn := range b.Catalog.Functions {
		tags := p.Index().Function(fn.Schema, fn.Name)
		if !tags.Has("deprecated") {
			continue
		}
		reason := deprecationReason(tags)
		for _, obj := range b.Objects {
			for _, f := range obj.Fields {
				if f.Meta != nil && f.Meta.Function == fn {
					f.Deprecated = reason
				}
			}
		}
	}
}

// removeColumnField drops the output field bound to c, refusing to empty the
// type (a fieldless object is invalid SDL).
func (p *Plugin) removeColumnField(b *schema.Builder, obj *schema.Object, c *introspect.Column) {
	f := columnField(obj, c)
	if f == nil {
		return
	}
	if len(obj.Fields) == 1 {
		p.log.Warn("smart-comments: @omit would leave type empty, keeping field",
			"type", obj.Name, "field", f.Name)
		return
	}
	obj.RemoveField(f.Name)
}

func columnField(obj *schema.Object, c *introspect.Column) *schema.Field {
	for _, f := range obj.Fields {
		if f.Meta != nil && f.Meta.Column == c {
			return f
		}
	}
	return nil
}

func deprecationReason(tags smarttags.Tags) string {
	if r := tags.First("deprecated"); r != "" {
		return r
	}
	return "No longer supported"
}

// removeFields drops matching fields from every object type.
func removeFields(b *schema.Builder, match func(*schema.Field) bool) {
	for _, obj := range b.Objects {
		kept := obj.Fields[:0]
		for _, f := range obj.Fields {
			if !match(f) {
				kept = append(kept, f)
			}
		}
		obj.Fields = kept
	}
}

// removeInputType deletes an input type and every argument referencing it.
func removeInputType(b *schema.Builder, name string) {
	if _, ok := b.Inputs[name]; !ok {
		return
	}
	delete(b.Inputs, name)
	removeArgsOfType(b, name)
}

// removeArgsOfType strips arguments whose base type (list/non-null unwrapped)
// is the given type name from every field.
func removeArgsOfType(b *schema.Builder, name string) {
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			kept := f.Args[:0]
			for _, a := range f.Args {
				if baseType(a.Type) != name {
					kept = append(kept, a)
				}
			}
			f.Args = kept
		}
	}
}

func baseType(t string) string {
	return strings.Trim(t, "[]!")
}

// removeInputColumn drops the input field bound to the pg column.
func removeInputColumn(in *schema.Input, column string) {
	for _, f := range in.Fields {
		if f.Column == column {
			in.RemoveField(f.Name)
			return
		}
	}
}

// removeOrderValues drops the ASC/DESC enum values ordering by the column.
func removeOrderValues(en *schema.Enum, column string) {
	kept := en.Values[:0]
	for _, v := range en.Values {
		if v.Column != column {
			kept = append(kept, v)
		}
	}
	en.Values = kept
}

// renameOrderValues realigns orderBy enum values with a renamed column
// (COL_ASC -> NEW_ASC).
func renameOrderValues(en *schema.Enum, column, newName string) {
	oldBase, newBase := coreinflect.EnumValue(column), coreinflect.EnumValue(newName)
	if oldBase == newBase {
		return
	}
	for i, v := range en.Values {
		if v.Column == column {
			en.Values[i].Name = newBase + strings.TrimPrefix(v.Name, oldBase)
		}
	}
}

// stripDescriptions removes smart-tag lines from every generated description
// (the builder copies raw comments in before SchemaHooks run).
func stripDescriptions(b *schema.Builder) {
	for _, obj := range b.Objects {
		obj.Description = smarttags.Strip(obj.Description)
		for _, f := range obj.Fields {
			f.Description = smarttags.Strip(f.Description)
		}
	}
	for _, in := range b.Inputs {
		in.Description = smarttags.Strip(in.Description)
		for _, f := range in.Fields {
			f.Description = smarttags.Strip(f.Description)
		}
	}
	for _, en := range b.Enums {
		en.Description = smarttags.Strip(en.Description)
		for i := range en.Values {
			en.Values[i].Description = smarttags.Strip(en.Values[i].Description)
		}
	}
}
