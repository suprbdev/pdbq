package schema

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/inflect"
)

// addMutations adds t's table mutations in PostGraphile v4's shape. Each
// takes one argument, input, of its own input type
// ([inflect.KindMutationInput]: CreateUserInput, UpdateUserByIdInput, ...)
// holding clientMutationId, the key fields of a keyed update or delete, the
// filter of a bulk update or delete, and the row values: the row
// (<Type>Input, [inflect.KindCreateInput]) of a create or upsert, the rows
// of a bulk create, the patch (<Type>Patch, [inflect.KindPatchInput]) of an
// update. A mutation field, input type or payload type whose name another
// table mutation already took, or an input type whose name any type took,
// is a build error that [Builder.Build] returns.
func (b *Builder) addMutations(t *introspect.Table, filterName string) {
	key := tableKey(t.Schema, t.Name)
	typeName := b.TypeForTable[key]
	rowField := strings.ToLower(typeName[:1]) + typeName[1:]
	rowsField := pluralField(typeName)
	patchField := rowField + "Patch"
	ti := inflect.Input{Schema: t.Schema, Table: t.Name}
	of := " of table " + key

	values := func(kind inflect.Kind, source string, required func(*introspect.Column) bool) string {
		name := b.Inflect(kind, ti)
		in := &Input{Name: name}
		for _, c := range t.Columns {
			if c.GeneratedAlways() {
				continue
			}
			in.AddField(&InputField{
				Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
				Type:   nonNull(b.gqlInputTypeForColumn(c), required(c)),
				Column: c.Name,
			})
		}
		if len(in.Fields) == 0 || !b.claimType(name, source+of) {
			return ""
		}
		b.Inputs[name] = in
		return name
	}

	keyFields := func(con *introspect.Constraint) []*InputField {
		var fields []*InputField
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil {
				return nil
			}
			fields = append(fields, &InputField{
				Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: cn}),
				Type:   b.gqlInputTypeForColumn(c) + "!",
				Column: cn,
			})
		}
		return fields
	}

	var createName, patchName string
	if t.Insertable() {
		createName = values(inflect.KindCreateInput, "the create values", func(c *introspect.Column) bool { return !c.OmittableOnInsert() })
	}
	if t.Updatable() {
		patchName = values(inflect.KindPatchInput, "the patch", func(*introspect.Column) bool { return false })
	}

	if t.Insertable() {
		var fields []*InputField
		if createName != "" {
			fields = append(fields, &InputField{Name: rowField, Type: createName + "!", Description: "The " + typeName + " to create."})
		}
		meta := &FieldMeta{Kind: KindCreate, Table: t}
		if createName != "" {
			meta.Values = rowField
		}
		b.addTableMutation(t, "create", fields, &Field{Name: b.Inflect(inflect.KindCreateMutation, ti), Meta: meta})
		if createName != "" {
			b.addTableMutation(t, "bulk create", []*InputField{
				{Name: rowsField, Type: "[" + createName + "!]!", Description: "The " + typeName + " rows to create."},
			}, &Field{
				Name:        b.Inflect(inflect.KindCreateManyMutation, ti),
				Description: "Inserts multiple " + typeName + " rows in one statement.",
				Meta:        &FieldMeta{Kind: KindCreateMany, Table: t, Values: rowsField},
			})
		}
	}

	// Update/delete key targets: the primary key, then each unique constraint
	// (mirroring the single-row lookup surface).
	var targets []*introspect.Constraint
	if t.PrimaryKey != nil {
		targets = append(targets, t.PrimaryKey)
	}
	targets = append(targets, t.Uniques...)

	// Upsert per key target: INSERT ... ON CONFLICT (target) DO UPDATE. Needs
	// both INSERT and UPDATE privileges, and the create input to exist.
	if t.Insertable() && t.Updatable() && createName != "" {
		seen := map[string]bool{}
		for _, con := range t.RowKeys() {
			// A conflict target must be settable through the create input;
			// identity ALWAYS and generated key columns rule the target out.
			usable := true
			for _, cn := range con.Columns {
				c := t.Column(cn)
				if c == nil || c.GeneratedAlways() {
					usable = false
					break
				}
			}
			if !usable || seen[strings.Join(con.Columns, ",")] {
				continue
			}
			seen[strings.Join(con.Columns, ",")] = true
			kind := inflect.KindUpsertByUniqueMutation
			if con == t.PrimaryKey {
				kind = inflect.KindUpsertMutation
			}
			b.addTableMutation(t, "upsert", []*InputField{
				{Name: rowField, Type: createName + "!", Description: "The " + typeName + " to create or update."},
			}, &Field{
				Name:        b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns}),
				Description: "Inserts a " + typeName + ", updating the existing row on a (" + strings.Join(con.Columns, ", ") + ") conflict.",
				Meta: &FieldMeta{
					Kind: KindUpsert, Table: t, KeyColumns: con.Columns, Values: rowField,
					Patch: b.Inflect(inflect.KindPatchInput, ti),
				},
			})
		}
	}

	if patchName != "" {
		seen := map[string]bool{}
		for i, con := range targets {
			fields := keyFields(con)
			if fields == nil || seen[strings.Join(con.Columns, ",")] {
				continue
			}
			seen[strings.Join(con.Columns, ",")] = true
			kind := inflect.KindUpdateByUniqueMutation
			if i == 0 && t.PrimaryKey != nil {
				kind = inflect.KindUpdateMutation
			}
			fields = append(fields, &InputField{Name: patchField, Type: patchName + "!", Description: "The values to set on the " + typeName + "."})
			b.addTableMutation(t, "update", fields, &Field{
				Name: b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns}),
				Meta: &FieldMeta{Kind: KindUpdate, Table: t, KeyColumns: con.Columns, Values: patchField},
			})
		}
		// Filtered bulk update; only generated when the table has a filter type
		// (a bulk write without a WHERE surface would be a foot-gun).
		if filterName != "" {
			b.addTableMutation(t, "bulk update", []*InputField{
				{Name: "filter", Type: filterName + "!", Description: "The filter selecting the " + typeName + " rows to update."},
				{Name: patchField, Type: patchName + "!", Description: "The values to set on every matching " + typeName + "."},
			}, &Field{
				Name:        b.Inflect(inflect.KindUpdateManyMutation, ti),
				Description: "Updates every " + typeName + " matching the filter.",
				Meta:        &FieldMeta{Kind: KindUpdateMany, Table: t, Values: patchField},
			})
		}
	}

	if t.Deletable() {
		seen := map[string]bool{}
		for i, con := range targets {
			fields := keyFields(con)
			if fields == nil || seen[strings.Join(con.Columns, ",")] {
				continue
			}
			seen[strings.Join(con.Columns, ",")] = true
			kind := inflect.KindDeleteByUniqueMutation
			if i == 0 && t.PrimaryKey != nil {
				kind = inflect.KindDeleteMutation
			}
			b.addTableMutation(t, "delete", fields, &Field{
				Name: b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns}),
				Meta: &FieldMeta{Kind: KindDelete, Table: t, KeyColumns: con.Columns},
			})
		}
		if filterName != "" {
			b.addTableMutation(t, "bulk delete", []*InputField{
				{Name: "filter", Type: filterName + "!", Description: "The filter selecting the " + typeName + " rows to delete."},
			}, &Field{
				Name:        b.Inflect(inflect.KindDeleteManyMutation, ti),
				Description: "Deletes every " + typeName + " matching the filter.",
				Meta:        &FieldMeta{Kind: KindDeleteMany, Table: t},
			})
		}
	}
}

// addTableMutation adds the table mutation f of t (verb names its kind in
// errors) to the Mutation root with its input argument, an input type
// named after f by [inflect.KindMutationInput] holding clientMutationId and
// fields, and its payload type: the verb's single-row payload, shared by
// the mutations of t with that verb, or a bulk mutation's own payload.
func (b *Builder) addTableMutation(t *introspect.Table, verb string, fields []*InputField, f *Field) {
	source := "the " + verb + " mutation of table " + tableKey(t.Schema, t.Name)
	if prev, ok := b.mutationSources[f.Name]; ok {
		b.fail(fmt.Errorf("schema: mutation name collision: %q is both %s and %s; %s", f.Name, prev, source, renameHint))
		return
	}
	var payload string
	switch f.Meta.Kind {
	case KindCreateMany, KindUpdateMany, KindDeleteMany:
		payload = b.manyPayload(t, f.Name, verb)
	default:
		payload = b.rowPayload(t, verb)
	}
	if payload == "" {
		return
	}
	f.Type = payload + "!"
	name := b.Inflect(inflect.KindMutationInput, inflect.Input{Schema: t.Schema, Table: t.Name, Value: f.Name})
	in := &Input{Name: name}
	for _, fl := range append([]*InputField{{Name: "clientMutationId", Type: "String"}}, fields...) {
		if in.Field(fl.Name) != nil {
			b.fail(fmt.Errorf("schema: input field collision: %s (the input of %s) has two fields named %q; %s",
				name, source, fl.Name, renameHint))
			return
		}
		in.AddField(fl)
	}
	if !b.claimType(name, "the input of "+source) {
		return
	}
	b.Inputs[name] = in
	b.mutationTypes[name] = true
	f.Args = []Arg{{Name: "input", Type: name + "!"}}
	b.mutationSources[f.Name] = source
	b.Mutation.AddField(f)
}

// rowPayload returns the payload type of t's single-row mutations with
// verb (create, update, delete, upsert), generating it on first use: the
// row, clientMutationId, query, the row's forward relations and edge, and
// on a delete payload the deleted row's nodeId. It returns "" when the name
// is taken.
func (b *Builder) rowPayload(t *introspect.Table, verb string) string {
	key := tableKey(t.Schema, t.Name)
	typeName := b.TypeForTable[key]
	name := b.Inflect(inflect.KindPayloadTypeName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: verb})
	source := "the " + verb + " payload of table " + key
	if b.typeSources[name] == source {
		return name
	}
	if !b.claimType(name, source) {
		return ""
	}
	obj := &Object{Name: name, Fields: []*Field{
		{Name: strings.ToLower(typeName[:1]) + typeName[1:], Type: typeName, Meta: &FieldMeta{Kind: KindPayloadRow, Table: t}},
		{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}},
	}}
	deletedNodeID := ""
	if verb == "delete" && t.PrimaryKey != nil {
		deletedNodeID = b.Inflect(inflect.KindPayloadDeletedNodeID, inflect.Input{Schema: t.Schema, Table: t.Name})
	}
	b.addPayloadFields(obj, t, deletedNodeID)
	if deletedNodeID != "" {
		b.addRootField(obj, &Field{
			Name:        deletedNodeID,
			Type:        "ID",
			Description: "The nodeId of the deleted " + typeName + ".",
			Meta:        &FieldMeta{Kind: KindPayloadDeletedNodeID, Table: t},
		})
	}
	b.Objects[name] = obj
	b.mutationTypes[name] = true
	return name
}

// manyPayload returns the payload type of t's bulk mutation field (verb
// names its kind in errors): the mutated rows, affectedCount,
// clientMutationId and query. It returns "" when the name is taken.
func (b *Builder) manyPayload(t *introspect.Table, field, verb string) string {
	key := tableKey(t.Schema, t.Name)
	typeName := b.TypeForTable[key]
	name := b.Inflect(inflect.KindManyPayloadTypeName, inflect.Input{Schema: t.Schema, Table: t.Name, Value: field})
	if !b.claimType(name, "the payload of the "+verb+" mutation of table "+key) {
		return ""
	}
	obj := &Object{Name: name, Fields: []*Field{
		{Name: pluralField(typeName), Type: "[" + typeName + "!]!", Meta: &FieldMeta{Kind: KindPayloadRows, Table: t}},
		{Name: "affectedCount", Type: "Int!", Meta: &FieldMeta{Kind: KindPayloadCount}},
		{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}},
	}}
	b.addPayloadFields(obj, nil)
	b.Objects[name] = obj
	b.mutationTypes[name] = true
	return name
}

// pluralField returns the lowerCamel plural of the object type name
// typeName, pluralizing its last word (users, userAccounts, news).
func pluralField(typeName string) string {
	var snake strings.Builder
	for i, r := range typeName {
		if i > 0 && unicode.IsUpper(r) {
			snake.WriteByte('_')
		}
		snake.WriteRune(unicode.ToLower(r))
	}
	return coreinflect.LowerCamel(coreinflect.Pluralize(snake.String()))
}

// renameHint ends every table mutation collision error.
const renameHint = "rename the table or column with the @name smart comment, or the generated name in an InflectionHook plugin"

// claimType reserves the type name for a table mutation's type described
// by source. A name any other type already has is a build error naming
// both.
func (b *Builder) claimType(name, source string) bool {
	if prev := b.describeType(name); prev != "" {
		b.fail(fmt.Errorf("schema: type name collision: %q is both %s and %s; %s", name, prev, source, renameHint))
		return false
	}
	b.typeSources[name] = source
	return true
}

// describeType names what declared the type name, or returns "" when no
// type has it.
func (b *Builder) describeType(name string) string {
	if src, ok := b.typeSources[name]; ok {
		return src
	}
	switch {
	case b.Objects[name] != nil:
		for key, tn := range b.TypeForTable {
			if tn == name {
				return "the object type of table " + key
			}
		}
		return "an object type"
	case b.Inputs[name] != nil:
		return "an input type"
	case b.Enums[name] != nil:
		return "an enum type"
	case slices.Contains(b.Scalars, name):
		return "a scalar"
	}
	return ""
}

// fail records a schema build error, returned by [Builder.Build].
func (b *Builder) fail(err error) {
	b.errs = append(b.errs, err)
}

// pruneMutations settles the table mutations once every SchemaHook ran. A
// create whose row values lost every field (`@omit create` on each column)
// inserts the column defaults, so its input drops the row field; any other
// input type left without fields is removed with [Builder.RemoveInputType]
// (and so the mutations needing it), and the table mutation input and
// payload types no field references any more are removed.
func (b *Builder) pruneMutations() {
	for _, f := range b.Mutation.Fields {
		if f.Meta == nil || f.Meta.Kind != KindCreate || f.Meta.Values == "" || len(f.Args) == 0 {
			continue
		}
		in := b.Inputs[baseType(f.Args[0].Type)]
		if in == nil {
			continue
		}
		if vf := in.Field(f.Meta.Values); vf != nil {
			if values := b.Inputs[baseType(vf.Type)]; values == nil || len(values.Fields) == 0 {
				in.RemoveField(vf.Name)
				f.Meta.Values = ""
			}
		}
	}
	for _, name := range sortedKeys(b.Inputs) {
		if in := b.Inputs[name]; in != nil && len(in.Fields) == 0 {
			b.RemoveInputType(name)
		}
	}
	used := map[string]bool{}
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			used[baseType(f.Type)] = true
			for _, a := range f.Args {
				used[baseType(a.Type)] = true
			}
		}
	}
	for _, in := range b.Inputs {
		for _, f := range in.Fields {
			used[baseType(f.Type)] = true
		}
	}
	for name := range b.mutationTypes {
		if !used[name] {
			delete(b.Inputs, name)
			delete(b.Objects, name)
		}
	}
}

// RemoveInputType deletes the input type name and every reference to it:
// an optional argument or input field of that type is dropped, while a
// field with a required argument of that type (a table mutation's input)
// is removed, and so is an input type with a required field of that type
// (a bulk update's input requiring its filter) or left without fields,
// recursively.
func (b *Builder) RemoveInputType(name string) {
	if _, ok := b.Inputs[name]; !ok {
		return
	}
	delete(b.Inputs, name)
	b.removeReferences(name)
}

// RemoveEnumType deletes the enum type name and every reference to it, as
// [Builder.RemoveInputType] does.
func (b *Builder) RemoveEnumType(name string) {
	if _, ok := b.Enums[name]; !ok {
		return
	}
	delete(b.Enums, name)
	b.removeReferences(name)
}

func (b *Builder) removeReferences(name string) {
	for _, obj := range b.Objects {
		fields := obj.Fields[:0]
		for _, f := range obj.Fields {
			args := f.Args[:0]
			required := false
			for _, a := range f.Args {
				if baseType(a.Type) != name {
					args = append(args, a)
				} else if strings.HasSuffix(a.Type, "!") {
					required = true
				}
			}
			f.Args = args
			if !required {
				fields = append(fields, f)
			}
		}
		obj.Fields = fields
	}
	var gone []string
	for _, inName := range sortedKeys(b.Inputs) {
		in := b.Inputs[inName]
		kept := in.Fields[:0]
		required := false
		for _, f := range in.Fields {
			if baseType(f.Type) != name {
				kept = append(kept, f)
			} else if strings.HasSuffix(f.Type, "!") {
				required = true
			}
		}
		changed := len(kept) != len(in.Fields)
		in.Fields = kept
		if changed && (required || len(kept) == 0) {
			gone = append(gone, inName)
		}
	}
	for _, n := range gone {
		b.RemoveInputType(n)
	}
}

// baseType strips the list and non-null wrappers off an SDL type reference.
func baseType(t string) string {
	return strings.Trim(t, "[]!")
}
