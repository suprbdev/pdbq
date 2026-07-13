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
//   - @name <new_name> — rename a table, column, enum, or function; the value
//     replaces the pg name at the start of the inflection pipeline, so every
//     derived name (type, queries, mutations, filters) follows.
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
//
// Tag lines are stripped from the GraphQL descriptions; the rest of the
// comment remains the description. Everything happens at schema-build time —
// the compiler is untouched, and disabling the plugin
// (plugins.disabled: [smart-comments]) restores the raw schema.
package smartcomments

import (
	"context"
	"log/slog"
	"regexp"
	"strings"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/smarttags"
)

type Plugin struct {
	// Tag indexes, rebuilt on every TransformCatalog (watch-mode rebuilds
	// reuse the plugin instance). The *ByName maps are schema-less fallbacks
	// for inflection call sites that don't carry the pg schema; an ambiguous
	// name (same table name in two schemas) maps to nil.
	tables      map[string]smarttags.Tags // "schema.table"
	tablesBy    map[string]smarttags.Tags // "table"
	columns     map[string]smarttags.Tags // "schema.table.column"
	columnsBy   map[string]smarttags.Tags // "table.column"
	enums       map[string]smarttags.Tags // "schema.enum"
	enumsBy     map[string]smarttags.Tags // "enum"
	functions   map[string]smarttags.Tags // "schema.function"
	functionsBy map[string]smarttags.Tags // "function"
	constraints map[string]smarttags.Tags // "schema.constraint"

	log *slog.Logger
}

func New() *Plugin { return &Plugin{log: slog.Default()} }

func (p *Plugin) Name() string { return "smart-comments" }

// Priority 50: the CatalogHook shapes the catalog before other plugins read
// it, the InflectionHook applies @name before naming plugins (simple-names,
// 100) format, and the SchemaHook removes omitted filter/orderBy types before
// advanced-filters (150) references them.
func (p *Plugin) Priority() int { return 50 }

// ---- CatalogHook ----

func (p *Plugin) TransformCatalog(_ context.Context, c *introspect.Catalog) error {
	p.indexTags(c)

	// Filter fully-omitted tables first, then apply the remaining tags:
	// @foreignKey validation must not see tables that are about to vanish.
	tables := make([]*introspect.Table, 0, len(c.Tables))
	for _, t := range c.Tables {
		if _, everything := p.tables[tableKey(t.Schema, t.Name)].Omits(); !everything {
			tables = append(tables, t)
		}
	}
	c.Tables = tables
	for _, t := range c.Tables {
		p.applyTableCatalogTags(c, t, p.tables[tableKey(t.Schema, t.Name)])
	}

	functions := c.Functions[:0]
	for _, f := range c.Functions {
		tags := p.functions[f.Schema+"."+f.Name]
		if _, everything := tags.Omits(); everything {
			continue
		}
		functions = append(functions, f)
	}
	c.Functions = functions
	return nil
}

// indexTags parses and indexes the smart tags of every commented object.
func (p *Plugin) indexTags(c *introspect.Catalog) {
	p.tables = map[string]smarttags.Tags{}
	p.tablesBy = map[string]smarttags.Tags{}
	p.columns = map[string]smarttags.Tags{}
	p.columnsBy = map[string]smarttags.Tags{}
	p.enums = map[string]smarttags.Tags{}
	p.enumsBy = map[string]smarttags.Tags{}
	p.functions = map[string]smarttags.Tags{}
	p.functionsBy = map[string]smarttags.Tags{}
	p.constraints = map[string]smarttags.Tags{}

	index := func(exact, by map[string]smarttags.Tags, exactKey, byKey, comment string) {
		tags, _ := smarttags.Parse(comment)
		if tags == nil {
			return
		}
		exact[exactKey] = tags
		if _, dup := by[byKey]; dup {
			by[byKey] = nil // ambiguous across schemas: exact lookups only
		} else {
			by[byKey] = tags
		}
	}
	for _, t := range c.Tables {
		index(p.tables, p.tablesBy, tableKey(t.Schema, t.Name), t.Name, t.Comment)
		for _, col := range t.Columns {
			index(p.columns, p.columnsBy,
				tableKey(t.Schema, t.Name)+"."+col.Name, t.Name+"."+col.Name, col.Comment)
		}
		for _, fk := range t.ForeignKeys {
			if tags, _ := smarttags.Parse(fk.Comment); tags != nil {
				p.constraints[t.Schema+"."+fk.Name] = tags
			}
		}
		for _, u := range t.Uniques {
			if tags, _ := smarttags.Parse(u.Comment); tags != nil {
				p.constraints[t.Schema+"."+u.Name] = tags
			}
		}
	}
	for _, e := range c.Enums {
		index(p.enums, p.enumsBy, e.Schema+"."+e.Name, e.Name, e.Comment)
	}
	for _, f := range c.Functions {
		index(p.functions, p.functionsBy, f.Schema+"."+f.Name, f.Name, f.Comment)
	}
}

// applyTableCatalogTags applies the catalog-level tags of one table: mutation
// gating, logical keys/relations, column nullability and filterability, and
// omitted constraints.
func (p *Plugin) applyTableCatalogTags(c *introspect.Catalog, t *introspect.Table, tags smarttags.Tags) {
	omits, _ := tags.Omits()
	if omits["create"] {
		t.Privileges.Insert = false
	}
	if omits["update"] {
		t.Privileges.Update = false
	}
	if omits["delete"] {
		t.Privileges.Delete = false
	}

	if v := tags.First("primaryKey"); v != "" {
		cols := smarttags.SplitList(v)
		switch {
		case t.PrimaryKey != nil:
			p.log.Warn("smart-comments: @primaryKey ignored, table already has one",
				"table", tableKey(t.Schema, t.Name))
		case !columnsExist(t, cols):
			p.log.Warn("smart-comments: @primaryKey references unknown column",
				"table", tableKey(t.Schema, t.Name), "value", v)
		default:
			t.PrimaryKey = &introspect.Constraint{Name: "@primaryKey", Columns: cols}
		}
	}
	for _, v := range tags.All("unique") {
		cols := smarttags.SplitList(v)
		if len(cols) == 0 || !columnsExist(t, cols) {
			p.log.Warn("smart-comments: @unique references unknown column",
				"table", tableKey(t.Schema, t.Name), "value", v)
			continue
		}
		t.Uniques = append(t.Uniques, &introspect.Constraint{Name: "@unique " + v, Columns: cols})
	}
	for _, v := range tags.All("foreignKey") {
		fk := p.parseForeignKey(c, t, v)
		if fk != nil {
			t.ForeignKeys = append(t.ForeignKeys, fk)
		}
	}

	for _, col := range t.Columns {
		ctags := p.columns[tableKey(t.Schema, t.Name)+"."+col.Name]
		if ctags == nil {
			continue
		}
		if ctags.Has("notNull") {
			col.NotNull = true
		}
		if ctags.Has("nullable") {
			col.NotNull = false
		}
		// @filterable / @sortable bypass filters.indexed_only via a synthetic
		// index (the builder's allow-set is "leading column of any index").
		// The SchemaHook does not prune the other half: both tags admit the
		// column to filtering and ordering; combine with @omit filter/order.
		if ctags.Has("filterable") || ctags.Has("sortable") {
			t.Indexes = append(t.Indexes, &introspect.Index{
				Name: "@filterable " + col.Name, Columns: []string{col.Name}, Method: "btree",
			})
		}
	}

	uniques := t.Uniques[:0]
	for _, u := range t.Uniques {
		if _, everything := p.constraints[t.Schema+"."+u.Name].Omits(); everything {
			continue // drop the lookup field; filterability persists via the backing index
		}
		uniques = append(uniques, u)
	}
	t.Uniques = uniques

	fks := t.ForeignKeys[:0]
	for _, fk := range t.ForeignKeys {
		if _, everything := p.constraints[t.Schema+"."+fk.Name].Omits(); everything {
			continue // both relation fields and relation filters disappear
		}
		fks = append(fks, fk)
	}
	t.ForeignKeys = fks
}

// fkRe matches "@foreignKey (a, b) references [schema.]table (x, y)".
var fkRe = regexp.MustCompile(`(?i)^\(([^)]+)\)\s+references\s+(?:([\w$]+)\.)?([\w$]+)\s*\(([^)]+)\)$`)

// parseForeignKey parses a @foreignKey value into a logical ForeignKey.
// Trailing "|@fieldName x|@foreignFieldName y" segments name the generated
// relation fields (the synthetic constraint cannot carry its own comment).
func (p *Plugin) parseForeignKey(c *introspect.Catalog, t *introspect.Table, v string) *introspect.ForeignKey {
	warn := func(reason string) *introspect.ForeignKey {
		p.log.Warn("smart-comments: @foreignKey ignored: "+reason,
			"table", tableKey(t.Schema, t.Name), "value", v)
		return nil
	}
	spec, rest, _ := strings.Cut(v, "|")
	m := fkRe.FindStringSubmatch(strings.TrimSpace(spec))
	if m == nil {
		return warn("want (cols) references [schema.]table (cols)")
	}
	cols, refSchema, refTable, refCols := smarttags.SplitList(m[1]), m[2], m[3], smarttags.SplitList(m[4])
	if refSchema == "" {
		refSchema = t.Schema
	}
	ref := c.Table(refSchema, refTable)
	switch {
	case len(cols) == 0 || len(cols) != len(refCols):
		return warn("column lists must be non-empty and the same length")
	case !columnsExist(t, cols):
		return warn("unknown local column")
	case ref == nil:
		return warn("unknown referenced table")
	case !columnsExist(ref, refCols):
		return warn("unknown referenced column")
	}
	name := "@foreignKey " + spec
	fk := &introspect.ForeignKey{
		Name: name, Columns: cols,
		RefSchema: refSchema, RefTable: refTable, RefColumns: refCols,
	}
	if rest != "" {
		tags := smarttags.Tags{}
		for _, part := range strings.Split(rest, "|") {
			if part = strings.TrimPrefix(strings.TrimSpace(part), "@"); part != "" {
				n, val, _ := strings.Cut(part, " ")
				tags[n] = append(tags[n], strings.TrimSpace(val))
			}
		}
		p.constraints[t.Schema+"."+name] = tags
	}
	return fk
}

func columnsExist(t *introspect.Table, cols []string) bool {
	for _, c := range cols {
		if t.Column(c) == nil {
			return false
		}
	}
	return true
}

func tableKey(schema, name string) string { return schema + "." + name }

// ---- InflectionHook ----

// Inflect applies @name / @fieldName / @foreignFieldName at the start of the
// naming pipeline: renames replace the pg identifier in the Input and the
// chain continues, so downstream naming plugins (simple-names) and the
// default inflector all derive from the new name.
func (p *Plugin) Inflect(kind inflect.Kind, in inflect.Input, next inflect.Next) string {
	switch kind {
	case inflect.KindRelationForward:
		if name := p.constraintTags(in.Schema, in.Constraint).First("fieldName"); name != "" {
			return name
		}
	case inflect.KindRelationBackward:
		if name := p.constraintTags(in.Schema, in.Constraint).First("foreignFieldName"); name != "" {
			return name
		}
		// in.Table/in.Columns identify the child table here.
		in.Columns = p.renamedColumns(in.Schema, in.Table, in.Columns)
		in.Table = p.renamedTable(in.Schema, in.Table)
	case inflect.KindTypeName, inflect.KindAllRowsField, inflect.KindFilterTypeName,
		inflect.KindOrderByTypeName, inflect.KindCreateMutation, inflect.KindCreateInput,
		inflect.KindUpdateInput, inflect.KindPayloadTypeName,
		inflect.KindCreateManyMutation, inflect.KindUpdateManyMutation, inflect.KindDeleteManyMutation:
		in.Table = p.renamedTable(in.Schema, in.Table)
	case inflect.KindRowByPKField, inflect.KindRowByUniqueField,
		inflect.KindUpdateMutation, inflect.KindDeleteMutation,
		inflect.KindUpdateByUniqueMutation, inflect.KindDeleteByUniqueMutation,
		inflect.KindUpsertMutation, inflect.KindUpsertByUniqueMutation:
		in.Columns = p.renamedColumns(in.Schema, in.Table, in.Columns)
		in.Table = p.renamedTable(in.Schema, in.Table)
	case inflect.KindFieldName:
		in.Column = p.renamedColumn(in.Schema, in.Table, in.Column)
	case inflect.KindEnumTypeName:
		if name := p.lookup(p.enums, p.enumsBy, in.Schema, in.Enum).First("name"); name != "" {
			in.Enum = name
		}
	case inflect.KindFunctionField, inflect.KindFunctionInput, inflect.KindFunctionPayload,
		inflect.KindComputedField:
		if name := p.lookup(p.functions, p.functionsBy, in.Schema, in.Function).First("name"); name != "" {
			in.Function = name
		}
	}
	return next(kind, in)
}

// lookup resolves tags by "schema.name", falling back to the schema-less
// index when the call site did not carry the pg schema.
func (p *Plugin) lookup(exact, by map[string]smarttags.Tags, schema, name string) smarttags.Tags {
	if schema != "" {
		return exact[schema+"."+name]
	}
	return by[name]
}

func (p *Plugin) tableTags(schema, table string) smarttags.Tags {
	return p.lookup(p.tables, p.tablesBy, schema, table)
}

func (p *Plugin) columnTags(schema, table, column string) smarttags.Tags {
	if schema != "" {
		return p.columns[schema+"."+table+"."+column]
	}
	return p.columnsBy[table+"."+column]
}

func (p *Plugin) constraintTags(schema, constraint string) smarttags.Tags {
	if constraint == "" {
		return nil
	}
	return p.constraints[schema+"."+constraint]
}

func (p *Plugin) renamedTable(schema, table string) string {
	if name := p.tableTags(schema, table).First("name"); name != "" {
		return name
	}
	return table
}

func (p *Plugin) renamedColumn(schema, table, column string) string {
	if name := p.columnTags(schema, table, column).First("name"); name != "" {
		return name
	}
	return column
}

func (p *Plugin) renamedColumns(schema, table string, cols []string) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = p.renamedColumn(schema, table, c)
	}
	return out
}

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	if b.Options.Logger != nil {
		p.log = b.Options.Logger
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
	omits, _ := p.tableTags(t.Schema, t.Name).Omits()
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
	orderEnum := b.Enums[b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})]

	for _, c := range t.Columns {
		tags := p.columnTags(t.Schema, t.Name, c.Name)
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
}

// applyRelationOmits handles @omit many on foreign keys (bare @omit is
// resolved at the catalog level; filter omission is advanced-filters' side).
func (p *Plugin) applyRelationOmits(b *schema.Builder, t *introspect.Table) {
	for _, fk := range t.ForeignKeys {
		omits, _ := p.constraintTags(t.Schema, fk.Name).Omits()
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
		tags := p.functions[fn.Schema+"."+fn.Name]
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
	oldBase, newBase := inflect.EnumValue(column), inflect.EnumValue(newName)
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
