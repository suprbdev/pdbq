package schema

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
)

// Options control default generation.
type Options struct {
	FilterIndexedOnly bool
	// AllowColumns lists extra filterable columns per "schema.table".
	AllowColumns map[string][]string
	// Functions exposes PostgreSQL functions as custom queries/mutations.
	Functions bool
	Logger    *slog.Logger
}

// Builder is the mutable schema IR handed to SchemaHook plugins.
type Builder struct {
	Catalog *introspect.Catalog
	Inflect inflect.Next
	Options Options
	Objects map[string]*Object
	Inputs  map[string]*Input
	Enums   map[string]*Enum
	Scalars []string
	// Query/Mutation are the root objects (also present in Objects).
	Query    *Object
	Mutation *Object

	// TypeForTable maps "schema.table" to the generated object type name.
	TypeForTable map[string]string
	// TypeForComposite maps "schema.type" to the generated object type name.
	TypeForComposite map[string]string
	// InputForComposite maps "schema.type" to the generated input type name.
	InputForComposite map[string]string

	log *slog.Logger
}

// Meta is the compiled lookup the executor/compiler use: type name -> field
// name -> metadata.
type Meta map[string]map[string]*FieldMeta

// Built is the result of Build: the validated executable schema plus
// everything the compiler needs.
type Built struct {
	Schema       *ast.Schema
	SDL          string
	Meta         Meta
	Catalog      *introspect.Catalog
	TypeForTable map[string]string
	// TableForType maps object type name -> catalog table (node() dispatch).
	TableForType map[string]*introspect.Table
	// OrderBy maps enum type name -> value name -> (column, desc).
	OrderBy map[string]map[string]OrderSpec
	// EnumValues maps enum type name -> GraphQL value -> pg value.
	EnumValues map[string]map[string]string
	// EnumTypeForPG maps "typeschema.typename" -> GraphQL enum type name.
	EnumTypeForPG map[string]string
	// CompositeTypeForPG maps "typeschema.typename" -> GraphQL object type name.
	CompositeTypeForPG map[string]string
	// CompositeInputForPG maps "typeschema.typename" -> GraphQL input type name.
	CompositeInputForPG map[string]string
	// InputColumns maps input type name -> field name -> column name.
	InputColumns map[string]map[string]string
	// FilterRelations maps filter input type name -> field name -> relation
	// binding (advanced-filters plugin; empty otherwise).
	FilterRelations map[string]map[string]*FilterRelation
	// FilterComputed maps filter input type name -> field name -> the
	// computed-column function backing it (advanced-filters plugin).
	FilterComputed map[string]map[string]*introspect.Function
}

type OrderSpec struct {
	Column string
	Desc   bool
	// Computed orders by a row-type function instead of Column.
	Computed *introspect.Function
}

// New creates a Builder with all default types generated from the catalog.
func New(cat *introspect.Catalog, inflector inflect.Next, opts Options) *Builder {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	b := &Builder{
		Catalog:           cat,
		Inflect:           inflector,
		Options:           opts,
		Objects:           map[string]*Object{},
		Inputs:            map[string]*Input{},
		Enums:             map[string]*Enum{},
		Scalars:           append([]string{}, customScalars...),
		TypeForTable:      map[string]string{},
		TypeForComposite:  map[string]string{},
		InputForComposite: map[string]string{},
		log:               opts.Logger,
	}
	b.Query = &Object{Name: "Query"}
	b.Mutation = &Object{Name: "Mutation"}
	b.Objects["Query"] = b.Query
	b.Objects["Mutation"] = b.Mutation
	b.addPageInfo()
	b.addNode()
	b.addEnums()
	b.addComposites()
	b.addTables()
	b.addFunctions()
	return b
}

// addNode declares the Relay Node interface and the node(nodeId:) global
// lookup. Runs before addTables so a table named "node" loses the collision
// via addRootField's keep-first policy.
func (b *Builder) addNode() {
	hasPK := false
	for _, t := range b.Catalog.Tables {
		if t.PrimaryKey != nil {
			hasPK = true
			break
		}
	}
	if !hasPK {
		return
	}
	b.Objects["Node"] = &Object{
		Name:        "Node",
		Description: "An object with a globally unique identifier.",
		IsInterface: true,
		Fields: []*Field{
			{Name: "nodeId", Type: "ID!", Meta: &FieldMeta{Kind: KindNodeID}},
		},
	}
	b.addRootField(b.Query, &Field{
		Name:        "node",
		Type:        "Node",
		Description: "Fetches an object given its globally unique identifier.",
		Args:        []Arg{{Name: "nodeId", Type: "ID!"}},
		Meta:        &FieldMeta{Kind: KindNode},
	})
}

func (b *Builder) addPageInfo() {
	b.Objects["PageInfo"] = &Object{
		Name:        "PageInfo",
		Description: "Pagination metadata for connections.",
		Fields: []*Field{
			{Name: "hasNextPage", Type: "Boolean!", Meta: &FieldMeta{Kind: KindSynthetic}},
			{Name: "hasPreviousPage", Type: "Boolean!", Meta: &FieldMeta{Kind: KindSynthetic}},
			{Name: "startCursor", Type: "Cursor", Meta: &FieldMeta{Kind: KindSynthetic}},
			{Name: "endCursor", Type: "Cursor", Meta: &FieldMeta{Kind: KindSynthetic}},
		},
	}
}

func (b *Builder) addEnums() {
	for _, e := range b.Catalog.Enums {
		name := b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name})
		en := &Enum{Name: name, Description: e.Comment}
		seen := map[string]bool{}
		for _, v := range e.Values {
			gv := b.Inflect(inflect.KindEnumValueName, inflect.Input{Enum: e.Name, Value: v})
			for seen[gv] {
				gv += "_"
			}
			seen[gv] = true
			en.Values = append(en.Values, EnumValue{Name: gv, PGValue: v})
		}
		b.Enums[name] = en
	}
}

// addComposites generates an object type and a matching input type per
// user-defined composite type. Names are assigned in a first pass so
// composite attributes referencing other composites resolve.
func (b *Builder) addComposites() {
	for _, ct := range b.Catalog.Composites {
		typeName := b.uniqueTypeName(b.Inflect(inflect.KindTypeName, inflect.Input{Schema: ct.Schema, Table: ct.Name}))
		b.TypeForComposite[tableKey(ct.Schema, ct.Name)] = typeName
		b.Objects[typeName] = &Object{Name: typeName}
		inputName := b.uniqueTypeName(typeName + "Input")
		b.InputForComposite[tableKey(ct.Schema, ct.Name)] = inputName
		b.Inputs[inputName] = &Input{Name: inputName}
	}
	for _, ct := range b.Catalog.Composites {
		obj := b.Objects[b.TypeForComposite[tableKey(ct.Schema, ct.Name)]]
		in := b.Inputs[b.InputForComposite[tableKey(ct.Schema, ct.Name)]]
		for _, f := range ct.Fields {
			name := b.Inflect(inflect.KindFieldName, inflect.Input{Schema: ct.Schema, Table: ct.Name, Column: f.Name})
			// Composite attributes cannot carry NOT NULL: always nullable.
			obj.AddField(&Field{
				Name:        name,
				Type:        b.gqlTypeForColumn(f),
				Description: f.Comment,
				Meta:        &FieldMeta{Kind: KindCompositeField, Column: f},
			})
			in.AddField(&InputField{
				Name:        name,
				Type:        b.gqlInputTypeForColumn(f),
				Description: f.Comment,
				Column:      f.Name,
			})
		}
	}
}

// gqlTypeForColumn resolves a column to an SDL type reference (without
// nullability marker).
func (b *Builder) gqlTypeForColumn(c *introspect.Column) string {
	base := ""
	pg := strings.TrimPrefix(c.PGType, "_")
	if e := b.Catalog.Enum(c.TypeSchema, pg); e != nil {
		base = b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name})
	} else if tn, ok := b.TypeForComposite[tableKey(c.TypeSchema, pg)]; ok {
		base = tn
	} else {
		base = scalarFor(pg)
	}
	if c.IsArray {
		return "[" + base + "!]"
	}
	return base
}

// gqlInputTypeForColumn is gqlTypeForColumn for input positions: composite
// columns resolve to their generated input type (object types are not valid
// in argument or input-field position).
func (b *Builder) gqlInputTypeForColumn(c *introspect.Column) string {
	pg := strings.TrimPrefix(c.PGType, "_")
	if in, ok := b.InputForComposite[tableKey(c.TypeSchema, pg)]; ok {
		if c.IsArray {
			return "[" + in + "!]"
		}
		return in
	}
	return b.gqlTypeForColumn(c)
}

func nonNull(t string, notNull bool) string {
	if notNull {
		return t + "!"
	}
	return t
}

func tableKey(schema, name string) string { return schema + "." + name }

func (b *Builder) addTables() {
	// First pass: object types + column fields, so relations can reference
	// target type names in the second pass.
	for _, t := range b.Catalog.Tables {
		typeName := b.uniqueTypeName(b.Inflect(inflect.KindTypeName, inflect.Input{Schema: t.Schema, Table: t.Name}))
		b.TypeForTable[tableKey(t.Schema, t.Name)] = typeName
		obj := &Object{Name: typeName, Description: t.Comment}
		for _, c := range t.Columns {
			obj.AddField(&Field{
				Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
				Type:        nonNull(b.gqlTypeForColumn(c), c.NotNull),
				Description: c.Comment,
				Meta:        &FieldMeta{Kind: KindColumn, Table: t, Column: c},
			})
		}
		// Node interface membership: tables with a primary key get a globally
		// unique nodeId (base64 of ["TypeName", pk...]). A real column that
		// inflects to "nodeId" wins; the type then opts out of Node.
		if t.PrimaryKey != nil {
			if obj.Field("nodeId") != nil {
				b.log.Warn("column collides with nodeId, type opts out of Node interface",
					"table", tableKey(t.Schema, t.Name))
			} else {
				obj.Fields = append([]*Field{{
					Name:        "nodeId",
					Type:        "ID!",
					Description: "Globally unique identifier for Relay node lookups.",
					Meta:        &FieldMeta{Kind: KindNodeID, Table: t},
				}}, obj.Fields...)
				obj.Interfaces = []string{"Node"}
			}
		}
		b.Objects[typeName] = obj
	}
	// Second pass: filters and orderBy enums first, so relation list fields
	// can reference them.
	filterNames := map[string]string{}
	orderNames := map[string]string{}
	for _, t := range b.Catalog.Tables {
		key := tableKey(t.Schema, t.Name)
		filterNames[key] = b.addFilter(t)
		orderNames[key] = b.addOrderBy(t)
		b.addDistinctOn(t)
	}
	for _, t := range b.Catalog.Tables {
		b.addRelations(t)
	}
	for _, t := range b.Catalog.Tables {
		key := tableKey(t.Schema, t.Name)
		b.addQueries(t, filterNames[key], orderNames[key])
		b.addMutations(t, filterNames[key])
	}
}

func (b *Builder) uniqueTypeName(name string) string {
	for b.Objects[name] != nil || b.Enums[name] != nil || b.Inputs[name] != nil {
		b.log.Warn("type name collision, appending suffix", "name", name)
		name += "_"
	}
	return name
}

// addObjectField adds a field, warning on collision. It keeps the existing
// field rather than failing, so inflection plugins degrade gracefully; callers
// that need a different outcome must pre-check.
func (b *Builder) addRootField(obj *Object, f *Field) {
	if obj.Field(f.Name) != nil {
		b.log.Warn("field name collision, keeping first", "type", obj.Name, "field", f.Name)
		return
	}
	obj.AddField(f)
}

func (b *Builder) addRelations(t *introspect.Table) {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	obj := b.Objects[typeName]
	// Forward: FK on t points to parent row (many-to-one).
	for _, fk := range t.ForeignKeys {
		ref := b.Catalog.Table(fk.RefSchema, fk.RefTable)
		refType, ok := b.TypeForTable[tableKey(fk.RefSchema, fk.RefTable)]
		if ref == nil || !ok {
			continue
		}
		name := b.Inflect(inflect.KindRelationForward, inflect.Input{
			Schema: t.Schema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns,
			Constraint: fk.Name,
		})
		if obj.Field(name) != nil {
			name += "Row"
		}
		b.addRootField(obj, &Field{
			Name: name,
			Type: refType,
			Meta: &FieldMeta{Kind: KindRelationForward, Table: ref, FK: fk},
		})
	}
	// Backward: FKs on other tables pointing at t (one-to-many).
	for _, other := range b.Catalog.Tables {
		for _, fk := range other.ForeignKeys {
			if fk.RefSchema != t.Schema || fk.RefTable != t.Name {
				continue
			}
			if _, ok := b.TypeForTable[tableKey(other.Schema, other.Name)]; !ok {
				continue
			}
			name := b.Inflect(inflect.KindRelationBackward, inflect.Input{
				Schema: other.Schema, Table: other.Name, Columns: fk.Columns,
				Constraint: fk.Name,
			})
			b.addRootField(obj, &Field{
				Name: name,
				Type: b.ensureConnection(other) + "!",
				Args: b.connectionArgs(other, "", ""),
				Meta: &FieldMeta{Kind: KindRelationBackward, Table: other, FK: fk},
			})
		}
	}
}

// filterableColumns applies the indexed-only policy plus config overrides.
// Composite-typed columns are never filterable: their object types have no
// meaningful scalar operator set.
func (b *Builder) filterableColumns(t *introspect.Table) []*introspect.Column {
	allowed := map[string]bool{}
	if b.Options.FilterIndexedOnly {
		allowed = t.IndexedColumns()
		for _, extra := range b.Options.AllowColumns[tableKey(t.Schema, t.Name)] {
			allowed[extra] = true
		}
	}
	var out []*introspect.Column
	for _, c := range t.Columns {
		if b.Options.FilterIndexedOnly && !allowed[c.Name] {
			continue
		}
		if _, ok := b.TypeForComposite[tableKey(c.TypeSchema, strings.TrimPrefix(c.PGType, "_"))]; ok {
			continue
		}
		out = append(out, c)
	}
	return out
}

// addFilter creates <Type>Filter and per-scalar operator inputs; returns the
// filter type name or "" when no column is filterable.
func (b *Builder) addFilter(t *introspect.Table) string {
	cols := b.filterableColumns(t)
	if len(cols) == 0 {
		return ""
	}
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	name := b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	in := &Input{Name: name, Description: "Boolean filter for " + typeName + " (all fields are ANDed)."}
	in.AddField(&InputField{Name: "and", Type: "[" + name + "!]"})
	in.AddField(&InputField{Name: "or", Type: "[" + name + "!]"})
	in.AddField(&InputField{Name: "not", Type: name})
	for _, c := range cols {
		gqlType := b.gqlTypeForColumn(c)
		elem := strings.TrimSuffix(strings.TrimPrefix(gqlType, "["), "!]")
		opInput := b.opInputFor(elem, c.PGType, c.IsArray)
		in.AddField(&InputField{
			Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
			Type:   opInput,
			Column: c.Name,
		})
	}
	b.Inputs[name] = in
	return name
}

// FilterOpsInputFor returns (creating if needed) the operator input type for
// a column-shaped value — exported for SchemaHook plugins adding filterable
// fields (e.g. advanced-filters computed columns).
func (b *Builder) FilterOpsInputFor(c *introspect.Column) string {
	gqlType := b.gqlTypeForColumn(c)
	elem := strings.TrimSuffix(strings.TrimPrefix(gqlType, "["), "!]")
	return b.opInputFor(elem, c.PGType, c.IsArray)
}

// opInputFor returns (creating if needed) the shared operator input type for
// a scalar, e.g. StringFilterOps.
func (b *Builder) opInputFor(gqlType, pgType string, isArray bool) string {
	name := gqlType + "FilterOps"
	if isArray {
		name = gqlType + "ListFilterOps"
	} else if pgType == "jsonb" || pgType == "json" {
		name = "JSONFilterOps"
	}
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	in := &Input{Name: name}
	for _, op := range filterOps(gqlType, strings.TrimPrefix(pgType, "_"), isArray) {
		in.AddField(&InputField{Name: op.Name, Type: op.Type})
	}
	b.Inputs[name] = in
	return name
}

// addDistinctOn creates the <Types>DistinctOn column enum backing the
// distinctOn argument; returns its name or "" when no column qualifies.
// Values carry Column bindings so Build() collects them into Built.OrderBy,
// which is where the compiler resolves them.
func (b *Builder) addDistinctOn(t *introspect.Table) string {
	cols := b.filterableColumns(t)
	if len(cols) == 0 {
		return ""
	}
	name := b.Inflect(inflect.KindDistinctOnTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en := &Enum{Name: name, Description: "Columns to de-duplicate " + b.TypeForTable[tableKey(t.Schema, t.Name)] + " rows by (SELECT DISTINCT ON)."}
	for _, c := range cols {
		en.Values = append(en.Values, EnumValue{Name: inflect.EnumValue(c.Name), Column: c.Name})
	}
	b.Enums[name] = en
	return name
}

// addOrderBy creates the <Types>OrderBy enum; returns its name or "".
func (b *Builder) addOrderBy(t *introspect.Table) string {
	cols := b.filterableColumns(t)
	if len(cols) == 0 {
		return ""
	}
	name := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	en := &Enum{Name: name}
	for _, c := range cols {
		base := inflect.EnumValue(c.Name)
		en.Values = append(en.Values,
			EnumValue{Name: base + "_ASC", Column: c.Name},
			EnumValue{Name: base + "_DESC", Column: c.Name, Desc: true},
		)
	}
	b.Enums[name] = en
	return name
}

func (b *Builder) listArgs(t *introspect.Table, filterName, orderName string) []Arg {
	if filterName == "" {
		filterName = b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		if _, ok := b.Inputs[filterName]; !ok {
			filterName = ""
		}
	}
	if orderName == "" {
		orderName = b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		if _, ok := b.Enums[orderName]; !ok {
			orderName = ""
		}
	}
	args := []Arg{
		{Name: "first", Type: "Int"},
		{Name: "offset", Type: "Int"},
	}
	if orderName != "" {
		args = append(args, Arg{Name: "orderBy", Type: "[" + orderName + "!]"})
	}
	if distinctName := b.Inflect(inflect.KindDistinctOnTypeName, inflect.Input{Schema: t.Schema, Table: t.Name}); b.Enums[distinctName] != nil {
		args = append(args, Arg{Name: "distinctOn", Type: "[" + distinctName + "!]"})
	}
	if filterName != "" {
		args = append(args, Arg{Name: "filter", Type: filterName})
	}
	return args
}

// connectionArgs is listArgs plus Relay cursor pagination.
func (b *Builder) connectionArgs(t *introspect.Table, filterName, orderName string) []Arg {
	args := []Arg{
		{Name: "first", Type: "Int"},
		{Name: "last", Type: "Int"},
		{Name: "offset", Type: "Int"},
		{Name: "before", Type: "Cursor"},
		{Name: "after", Type: "Cursor"},
	}
	return append(args, b.listArgs(t, filterName, orderName)[2:]...)
}

// ensureConnection creates the <Type>Connection and <Type>Edge objects once
// and returns the connection type name.
func (b *Builder) ensureConnection(t *introspect.Table) string {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	connName := typeName + "Connection"
	edgeName := typeName + "Edge"
	if _, ok := b.Objects[connName]; ok {
		return connName
	}
	b.Objects[edgeName] = &Object{Name: edgeName, Fields: []*Field{
		{Name: "cursor", Type: "Cursor!", Meta: &FieldMeta{Kind: KindSynthetic}},
		{Name: "node", Type: typeName + "!", Meta: &FieldMeta{Kind: KindSynthetic, Table: t}},
	}}
	b.Objects[connName] = &Object{Name: connName, Fields: []*Field{
		{Name: "nodes", Type: "[" + typeName + "!]!", Meta: &FieldMeta{Kind: KindSynthetic, Table: t}},
		{Name: "edges", Type: "[" + edgeName + "!]!", Meta: &FieldMeta{Kind: KindSynthetic, Table: t}},
		{Name: "pageInfo", Type: "PageInfo!", Meta: &FieldMeta{Kind: KindSynthetic}},
		{Name: "totalCount", Type: "Int!", Meta: &FieldMeta{Kind: KindSynthetic}},
	}}
	return connName
}

func (b *Builder) addQueries(t *introspect.Table, filterName, orderName string) {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]

	// Relay-style connection is the collection query.
	connName := b.ensureConnection(t)
	b.addRootField(b.Query, &Field{
		Name:        b.Inflect(inflect.KindAllRowsField, inflect.Input{Schema: t.Schema, Table: t.Name}),
		Type:        connName + "!",
		Description: "Reads a paginated connection of " + typeName + ".",
		Args:        b.connectionArgs(t, filterName, orderName),
		Meta:        &FieldMeta{Kind: KindConnectionQuery, Table: t},
	})

	// Single-row lookups by PK and by each unique constraint.
	lookups := []*introspect.Constraint{}
	if t.PrimaryKey != nil {
		lookups = append(lookups, t.PrimaryKey)
	}
	lookups = append(lookups, t.Uniques...)
	seen := map[string]bool{}
	for i, con := range lookups {
		kind := inflect.KindRowByUniqueField
		if i == 0 && t.PrimaryKey != nil {
			kind = inflect.KindRowByPKField
		}
		name := b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns})
		if seen[name] {
			continue
		}
		seen[name] = true
		var args []Arg
		ok := true
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil {
				ok = false
				break
			}
			args = append(args, Arg{
				Name: b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: cn}),
				Type: b.gqlInputTypeForColumn(c) + "!",
			})
		}
		if !ok {
			continue
		}
		b.addRootField(b.Query, &Field{
			Name: name,
			Type: typeName,
			Args: args,
			Meta: &FieldMeta{Kind: KindRowByKey, Table: t, KeyColumns: con.Columns},
		})
	}
}

func (b *Builder) addMutations(t *introspect.Table, filterName string) {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	rowField := strings.ToLower(typeName[:1]) + typeName[1:]

	payload := func(verb string) string {
		name := b.Inflect(inflect.KindPayloadTypeName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: verb})
		if _, ok := b.Objects[name]; !ok {
			b.Objects[name] = &Object{Name: name, Fields: []*Field{
				{Name: rowField, Type: typeName, Meta: &FieldMeta{Kind: KindPayloadRow, Table: t}},
				{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}},
			}}
		}
		return name
	}

	// payloadMany is the bulk-mutation payload: the mutated rows plus a count.
	payloadMany := func(verb string) string {
		name := b.Inflect(inflect.KindPayloadTypeName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: verb})
		if _, ok := b.Objects[name]; !ok {
			rowsField := inflect.LowerCamel(inflect.Pluralize(typeName))
			b.Objects[name] = &Object{Name: name, Fields: []*Field{
				{Name: rowsField, Type: "[" + typeName + "!]!", Meta: &FieldMeta{Kind: KindPayloadRows, Table: t}},
				{Name: "affectedCount", Type: "Int!", Meta: &FieldMeta{Kind: KindPayloadCount}},
				{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}},
			}}
		}
		return name
	}

	if t.Insertable() {
		inputName := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: t.Schema, Table: t.Name})
		in := &Input{Name: inputName}
		for _, c := range t.Columns {
			if c.Generated {
				continue
			}
			required := c.NotNull && !c.HasDefault
			in.AddField(&InputField{
				Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
				Type:   nonNull(b.gqlInputTypeForColumn(c), required),
				Column: c.Name,
			})
		}
		in.AddField(&InputField{Name: "clientMutationId", Type: "String"})
		b.Inputs[inputName] = in
		b.addRootField(b.Mutation, &Field{
			Name: b.Inflect(inflect.KindCreateMutation, inflect.Input{Schema: t.Schema, Table: t.Name}),
			Type: payload("create") + "!",
			Args: []Arg{{Name: "input", Type: inputName + "!"}},
			Meta: &FieldMeta{Kind: KindCreate, Table: t},
		})
		b.addRootField(b.Mutation, &Field{
			Name:        b.Inflect(inflect.KindCreateManyMutation, inflect.Input{Schema: t.Schema, Table: t.Name}),
			Type:        payloadMany("createMany") + "!",
			Description: "Inserts multiple " + typeName + " rows in one statement.",
			Args: []Arg{
				{Name: "input", Type: "[" + inputName + "!]!"},
				{Name: "clientMutationId", Type: "String"},
			},
			Meta: &FieldMeta{Kind: KindCreateMany, Table: t},
		})
	}

	// Update/delete key targets: the primary key, then each unique constraint
	// (mirroring the single-row lookup surface).
	var targets []*introspect.Constraint
	if t.PrimaryKey != nil {
		targets = append(targets, t.PrimaryKey)
	}
	targets = append(targets, t.Uniques...)
	if len(targets) == 0 {
		return
	}
	keyArgs := func(con *introspect.Constraint) ([]Arg, bool) {
		var args []Arg
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil {
				return nil, false
			}
			args = append(args, Arg{
				Name: b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: cn}),
				Type: b.gqlInputTypeForColumn(c) + "!",
			})
		}
		return args, true
	}

	// Upsert per key target: INSERT ... ON CONFLICT (target) DO UPDATE. Needs
	// both INSERT and UPDATE privileges, and the create input to exist.
	if t.Insertable() && t.Updatable() {
		inputName := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: t.Schema, Table: t.Name})
		if _, ok := b.Inputs[inputName]; ok {
			seen := map[string]bool{}
			for i, con := range targets {
				// A conflict target must be settable through the create input;
				// generated (identity) key columns rule the target out.
				usable := true
				for _, cn := range con.Columns {
					c := t.Column(cn)
					if c == nil || c.Generated {
						usable = false
						break
					}
				}
				if !usable {
					continue
				}
				kind := inflect.KindUpsertByUniqueMutation
				if i == 0 && t.PrimaryKey != nil {
					kind = inflect.KindUpsertMutation
				}
				name := b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns})
				if seen[name] {
					continue
				}
				seen[name] = true
				b.addRootField(b.Mutation, &Field{
					Name:        name,
					Type:        payload("upsert") + "!",
					Description: "Inserts a " + typeName + ", updating the existing row on a (" + strings.Join(con.Columns, ", ") + ") conflict.",
					Args:        []Arg{{Name: "input", Type: inputName + "!"}},
					Meta:        &FieldMeta{Kind: KindUpsert, Table: t, KeyColumns: con.Columns},
				})
			}
		}
	}

	if t.Updatable() {
		patchName := b.Inflect(inflect.KindUpdateInput, inflect.Input{Schema: t.Schema, Table: t.Name})
		patch := &Input{Name: patchName}
		for _, c := range t.Columns {
			if c.Generated {
				continue
			}
			patch.AddField(&InputField{
				Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
				Type:   b.gqlInputTypeForColumn(c),
				Column: c.Name,
			})
		}
		patch.AddField(&InputField{Name: "clientMutationId", Type: "String"})
		b.Inputs[patchName] = patch
		seen := map[string]bool{}
		for i, con := range targets {
			kind := inflect.KindUpdateByUniqueMutation
			if i == 0 && t.PrimaryKey != nil {
				kind = inflect.KindUpdateMutation
			}
			name := b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns})
			if seen[name] {
				continue
			}
			seen[name] = true
			args, ok := keyArgs(con)
			if !ok {
				continue
			}
			args = append(args, Arg{Name: "patch", Type: patchName + "!"})
			b.addRootField(b.Mutation, &Field{
				Name: name,
				Type: payload("update") + "!",
				Args: args,
				Meta: &FieldMeta{Kind: KindUpdate, Table: t, KeyColumns: con.Columns},
			})
		}
		// Filtered bulk update; only generated when the table has a filter type
		// (a bulk write without a WHERE surface would be a foot-gun).
		if filterName != "" {
			b.addRootField(b.Mutation, &Field{
				Name:        b.Inflect(inflect.KindUpdateManyMutation, inflect.Input{Schema: t.Schema, Table: t.Name}),
				Type:        payloadMany("updateMany") + "!",
				Description: "Updates every " + typeName + " matching the filter.",
				Args: []Arg{
					{Name: "filter", Type: filterName + "!"},
					{Name: "patch", Type: patchName + "!"},
					{Name: "clientMutationId", Type: "String"},
				},
				Meta: &FieldMeta{Kind: KindUpdateMany, Table: t},
			})
		}
	}

	if t.Deletable() {
		seen := map[string]bool{}
		for i, con := range targets {
			kind := inflect.KindDeleteByUniqueMutation
			if i == 0 && t.PrimaryKey != nil {
				kind = inflect.KindDeleteMutation
			}
			name := b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: con.Columns})
			if seen[name] {
				continue
			}
			seen[name] = true
			args, ok := keyArgs(con)
			if !ok {
				continue
			}
			args = append(args, Arg{Name: "clientMutationId", Type: "String"})
			b.addRootField(b.Mutation, &Field{
				Name: name,
				Type: payload("delete") + "!",
				Args: args,
				Meta: &FieldMeta{Kind: KindDelete, Table: t, KeyColumns: con.Columns},
			})
		}
		if filterName != "" {
			b.addRootField(b.Mutation, &Field{
				Name:        b.Inflect(inflect.KindDeleteManyMutation, inflect.Input{Schema: t.Schema, Table: t.Name}),
				Type:        payloadMany("deleteMany") + "!",
				Description: "Deletes every " + typeName + " matching the filter.",
				Args: []Arg{
					{Name: "filter", Type: filterName + "!"},
					{Name: "clientMutationId", Type: "String"},
				},
				Meta: &FieldMeta{Kind: KindDeleteMany, Table: t},
			})
		}
	}
}

func (b *Builder) addFunctions() {
	if !b.Options.Functions {
		return
	}
	for _, f := range b.Catalog.Functions {
		// A function whose first argument is a table's row type is a computed
		// column on that table's type, not a root field.
		if len(f.Args) > 0 {
			if t := b.Catalog.Table(f.Args[0].TypeSchema, f.Args[0].PGType); t != nil {
				b.addComputedField(t, f)
				continue
			}
		}
		if arg := rowTypeArg(b.Catalog, f); arg != "" {
			b.log.Debug("skipping function: row-type argument not in first position",
				"function", f.Schema+"."+f.Name, "arg", arg)
			continue
		}
		if hasUnnamedArg(f) {
			b.log.Debug("skipping function: unnamed argument (GraphQL arguments need names)",
				"function", f.Schema+"."+f.Name)
			continue
		}
		retBase := scalarFor(strings.TrimPrefix(f.ReturnType, "_"))
		// setof <table> -> list of the table's object type when known.
		for key, tn := range b.TypeForTable {
			if strings.HasSuffix(key, "."+f.ReturnType) {
				retBase = tn
				break
			}
		}
		if f.ReturnType == "void" {
			retBase = "Boolean"
		}
		retType := retBase
		if f.ReturnsSet {
			retType = "[" + retBase + "!]!"
		}
		fieldName := b.Inflect(inflect.KindFunctionField, inflect.Input{Schema: f.Schema, Function: f.Name})
		if f.Volatility == introspect.VolatilityVolatile {
			// Volatile functions are mutations with the Relay-classic shape:
			// fn(input: FnInput!): FnPayload! { result clientMutationId }.
			inputName := b.Inflect(inflect.KindFunctionInput, inflect.Input{Schema: f.Schema, Function: f.Name})
			payloadName := b.Inflect(inflect.KindFunctionPayload, inflect.Input{Schema: f.Schema, Function: f.Name})
			if _, dup := b.Inputs[inputName]; dup {
				b.log.Warn("skipping function: input type name collides (rename with a @name smart comment)",
					"function", f.Schema+"."+f.Name, "type", inputName)
				continue
			}
			if _, dup := b.Objects[payloadName]; dup {
				b.log.Warn("skipping function: payload type name collides (rename with a @name smart comment)",
					"function", f.Schema+"."+f.Name, "type", payloadName)
				continue
			}
			in := &Input{Name: inputName}
			for _, a := range f.Args {
				in.AddField(&InputField{
					Name: inflect.LowerCamel(a.Name),
					Type: argGQLType(a),
				})
			}
			in.AddField(&InputField{Name: "clientMutationId", Type: "String"})
			b.Inputs[inputName] = in

			resultType := retBase
			if f.ReturnsSet {
				resultType = "[" + resultType + "!]"
			}
			b.Objects[payloadName] = &Object{Name: payloadName, Fields: []*Field{
				{Name: "result", Type: resultType, Meta: &FieldMeta{Kind: KindPayloadResult, Function: f}},
				{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}},
			}}
			b.addRootField(b.Mutation, &Field{
				Name:        fieldName,
				Type:        payloadName + "!",
				Description: f.Comment,
				Args:        []Arg{{Name: "input", Type: inputName + "!"}},
				Meta:        &FieldMeta{Kind: KindFunction, Function: f},
			})
			continue
		}
		var args []Arg
		for _, a := range f.Args {
			args = append(args, Arg{
				Name: inflect.LowerCamel(a.Name),
				Type: argGQLType(a),
			})
		}
		b.addRootField(b.Query, &Field{
			Name:        fieldName,
			Type:        retType,
			Description: f.Comment,
			Args:        args,
			Meta:        &FieldMeta{Kind: KindFunction, Function: f},
		})
	}
}

// argGQLType maps a function argument's pg type to a GraphQL input type;
// array types ("_uuid") become lists.
func argGQLType(a introspect.FuncArg) string {
	base := scalarFor(strings.TrimPrefix(a.PGType, "_"))
	if strings.HasPrefix(a.PGType, "_") {
		return "[" + base + "]"
	}
	return base
}

// hasUnnamedArg reports whether any argument lacks a name; such functions
// cannot be exposed because GraphQL arguments are named, not positional.
func hasUnnamedArg(f *introspect.Function) bool {
	for _, a := range f.Args {
		if a.Name == "" {
			return true
		}
	}
	return false
}

// rowTypeArg returns the name of the first argument whose type is a catalog
// table's row type, or "" when there is none.
func rowTypeArg(cat *introspect.Catalog, f *introspect.Function) string {
	for _, a := range f.Args {
		if cat.Table(a.TypeSchema, a.PGType) != nil {
			return a.Name
		}
	}
	return ""
}

// addComputedField exposes a stable/immutable function whose first argument
// is t's row type as a computed field on t's object type. A `<table>_` name
// prefix is stripped by the default inflector (users_post_count -> postCount);
// remaining scalar arguments become GraphQL field arguments.
func (b *Builder) addComputedField(t *introspect.Table, f *introspect.Function) {
	fnKey := f.Schema + "." + f.Name
	switch {
	case f.Volatility == introspect.VolatilityVolatile:
		b.log.Debug("skipping computed column: volatile", "function", fnKey)
		return
	case f.ReturnType == "void":
		b.log.Debug("skipping computed column: void return", "function", fnKey)
		return
	}
	obj := b.Objects[b.TypeForTable[tableKey(t.Schema, t.Name)]]
	if obj == nil {
		return
	}
	var fieldType string
	if f.ReturnsSet {
		// Set-returning computed column: SETOF <table> becomes a list of the
		// table's object type, SETOF <scalar> a scalar list.
		if tn, ok := b.TypeForTable[tableKey(f.ReturnTypeSchema, f.ReturnType)]; ok {
			fieldType = "[" + tn + "!]!"
		} else {
			ret := &introspect.Column{PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema}
			fieldType = "[" + b.gqlTypeForColumn(ret) + "!]!"
		}
	} else {
		ret := &introspect.Column{
			PGType:     f.ReturnType,
			TypeSchema: f.ReturnTypeSchema,
			IsArray:    strings.HasPrefix(f.ReturnType, "_"),
		}
		fieldType = b.gqlTypeForColumn(ret) // always nullable: the function may return NULL
	}
	var args []Arg
	for _, a := range f.Args[1:] {
		args = append(args, Arg{
			Name: inflect.LowerCamel(a.Name),
			Type: scalarFor(strings.TrimPrefix(a.PGType, "_")),
		})
	}
	b.addRootField(obj, &Field{
		Name:        b.Inflect(inflect.KindComputedField, inflect.Input{Schema: f.Schema, Table: t.Name, Function: f.Name}),
		Type:        fieldType,
		Description: f.Comment,
		Args:        args,
		Meta:        &FieldMeta{Kind: KindComputed, Table: t, Function: f},
	})
}

// Build renders SDL, validates it, and returns the executable schema + meta.
func (b *Builder) Build() (*Built, error) {
	if len(b.Mutation.Fields) == 0 {
		delete(b.Objects, "Mutation")
	}
	if len(b.Query.Fields) == 0 {
		b.Query.Fields = append(b.Query.Fields, &Field{
			Name: "_empty", Type: "Boolean",
			Description: "No queryable relations were found.",
			Meta:        &FieldMeta{Kind: KindSynthetic},
		})
	}
	sdl := b.SDL()
	astSchema, err := gqlparser.LoadSchema(&ast.Source{Name: "pdbq", Input: sdl})
	if err != nil {
		return nil, fmt.Errorf("schema: generated SDL invalid: %w", err)
	}

	built := &Built{
		Schema:              astSchema,
		SDL:                 sdl,
		Meta:                Meta{},
		Catalog:             b.Catalog,
		TypeForTable:        b.TypeForTable,
		TableForType:        map[string]*introspect.Table{},
		OrderBy:             map[string]map[string]OrderSpec{},
		EnumValues:          map[string]map[string]string{},
		EnumTypeForPG:       map[string]string{},
		CompositeTypeForPG:  b.TypeForComposite,
		CompositeInputForPG: b.InputForComposite,
		InputColumns:        map[string]map[string]string{},
		FilterRelations:     map[string]map[string]*FilterRelation{},
		FilterComputed:      map[string]map[string]*introspect.Function{},
	}
	for _, e := range b.Catalog.Enums {
		built.EnumTypeForPG[e.Schema+"."+e.Name] = b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name})
	}
	for _, t := range b.Catalog.Tables {
		if tn, ok := b.TypeForTable[tableKey(t.Schema, t.Name)]; ok {
			built.TableForType[tn] = t
		}
	}
	for name, obj := range b.Objects {
		fm := map[string]*FieldMeta{}
		for _, f := range obj.Fields {
			fm[f.Name] = f.Meta
		}
		built.Meta[name] = fm
	}
	for name, en := range b.Enums {
		ordered := map[string]OrderSpec{}
		values := map[string]string{}
		for _, v := range en.Values {
			if v.Column != "" || v.Computed != nil {
				ordered[v.Name] = OrderSpec{Column: v.Column, Desc: v.Desc, Computed: v.Computed}
			}
			if v.PGValue != "" {
				values[v.Name] = v.PGValue
			}
		}
		if len(ordered) > 0 {
			built.OrderBy[name] = ordered
		}
		if len(values) > 0 {
			built.EnumValues[name] = values
		}
	}
	for name, in := range b.Inputs {
		cols := map[string]string{}
		for _, f := range in.Fields {
			if f.Column != "" {
				cols[f.Name] = f.Column
			}
			if f.Relation != nil {
				if built.FilterRelations[name] == nil {
					built.FilterRelations[name] = map[string]*FilterRelation{}
				}
				built.FilterRelations[name][f.Name] = f.Relation
			}
			if f.Computed != nil {
				if built.FilterComputed[name] == nil {
					built.FilterComputed[name] = map[string]*introspect.Function{}
				}
				built.FilterComputed[name][f.Name] = f.Computed
			}
		}
		built.InputColumns[name] = cols
	}
	return built, nil
}

// SDL renders the current IR as GraphQL SDL, deterministically ordered.
func (b *Builder) SDL() string {
	var out strings.Builder
	for _, s := range b.Scalars {
		fmt.Fprintf(&out, "scalar %s\n", s)
	}
	out.WriteString("\n")

	enumNames := sortedKeys(b.Enums)
	for _, n := range enumNames {
		e := b.Enums[n]
		writeDesc(&out, e.Description, "")
		fmt.Fprintf(&out, "enum %s {\n", e.Name)
		for _, v := range e.Values {
			fmt.Fprintf(&out, "  %s\n", v.Name)
		}
		out.WriteString("}\n\n")
	}

	inputNames := sortedKeys(b.Inputs)
	for _, n := range inputNames {
		in := b.Inputs[n]
		writeDesc(&out, in.Description, "")
		fmt.Fprintf(&out, "input %s {\n", in.Name)
		for _, f := range in.Fields {
			writeDesc(&out, f.Description, "  ")
			if f.Default != "" {
				fmt.Fprintf(&out, "  %s: %s = %s\n", f.Name, f.Type, f.Default)
			} else {
				fmt.Fprintf(&out, "  %s: %s\n", f.Name, f.Type)
			}
		}
		out.WriteString("}\n\n")
	}

	objNames := sortedKeys(b.Objects)
	for _, n := range objNames {
		if n == "Query" || n == "Mutation" {
			continue
		}
		b.writeObject(&out, b.Objects[n])
	}
	b.writeObject(&out, b.Query)
	if m, ok := b.Objects["Mutation"]; ok {
		b.writeObject(&out, m)
	}
	return out.String()
}

func (b *Builder) writeObject(out *strings.Builder, o *Object) {
	writeDesc(out, o.Description, "")
	keyword := "type"
	if o.IsInterface {
		keyword = "interface"
	}
	implements := ""
	if len(o.Interfaces) > 0 {
		implements = " implements " + strings.Join(o.Interfaces, " & ")
	}
	fmt.Fprintf(out, "%s %s%s {\n", keyword, o.Name, implements)
	for _, f := range o.Fields {
		writeDesc(out, f.Description, "  ")
		deprecated := ""
		if f.Deprecated != "" {
			deprecated = fmt.Sprintf(" @deprecated(reason: %q)", f.Deprecated)
		}
		if len(f.Args) == 0 {
			fmt.Fprintf(out, "  %s: %s%s\n", f.Name, f.Type, deprecated)
			continue
		}
		parts := make([]string, len(f.Args))
		for i, a := range f.Args {
			if a.Default != "" {
				parts[i] = fmt.Sprintf("%s: %s = %s", a.Name, a.Type, a.Default)
			} else {
				parts[i] = fmt.Sprintf("%s: %s", a.Name, a.Type)
			}
		}
		fmt.Fprintf(out, "  %s(%s): %s%s\n", f.Name, strings.Join(parts, ", "), f.Type, deprecated)
	}
	out.WriteString("}\n\n")
}

func writeDesc(out *strings.Builder, desc, indent string) {
	if desc == "" {
		return
	}
	fmt.Fprintf(out, "%s\"\"\"%s\"\"\"\n", indent, strings.ReplaceAll(desc, `"""`, `\"""`))
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
