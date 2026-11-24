package schema

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/postgis"
	"github.com/suprbdev/pdbcore/smarttags"
	"github.com/suprbdev/pdbq/internal/contribtype"
	"github.com/suprbdev/pdbq/internal/inflect"
)

// catalogHasSpatial reports whether any column, composite attribute, or
// function signature in the catalog uses a PostGIS type — the trigger for
// declaring the GeoJSON scalar.
func catalogHasSpatial(cat *introspect.Catalog) bool {
	for _, t := range cat.Tables {
		for _, c := range t.Columns {
			if postgis.IsSpatial(c.PGType) {
				return true
			}
		}
	}
	for _, ct := range cat.Composites {
		for _, f := range ct.Fields {
			if postgis.IsSpatial(f.PGType) {
				return true
			}
		}
	}
	for _, fn := range cat.Functions {
		if postgis.IsSpatial(FuncReturnColumn(cat, fn).PGType) {
			return true
		}
		for _, a := range fn.Args {
			if postgis.IsSpatial(FuncArgColumn(cat, a).PGType) {
				return true
			}
		}
	}
	return false
}

// Options control default generation.
type Options struct {
	FilterIndexedOnly bool
	// AllowColumns lists extra filterable columns per "schema.table".
	AllowColumns map[string][]string
	// Functions exposes PostgreSQL functions as custom queries/mutations.
	Functions bool
	// ExposePartitions keeps partitions (introspect.Table.IsPartition) in the
	// schema. By default they are left out: their rows are reachable through
	// the partitioned parent.
	ExposePartitions bool
	// JWTType is the "schema.type" composite that functions return to have a
	// JWT minted (auth.mint_type). Root functions returning it keep a
	// String result instead of the composite's object type.
	JWTType string
	// DomainScalars declares a custom scalar per domain over a built-in
	// scalar type (schema.domains), as PostGraphile does; otherwise
	// domains take their base type's GraphQL type.
	DomainScalars bool
	// Subscriptions declares the Subscription root with PostGraphile's
	// simple listen subscription (subscriptions.enabled). Without it the
	// schema has no Subscription type.
	Subscriptions bool
	// Live adds live queries (live.enabled): every Query root field is
	// repeated on the Subscription root, and the live directive is
	// declared for query operations. It has no effect without
	// Subscriptions.
	Live bool
	// IncrementalDelivery declares the defer and stream directives of the
	// incremental delivery RFC (incremental.enabled). Without it
	// the SDL declares no directive.
	IncrementalDelivery bool
	// MaxHeadlineDocument bounds, in characters, the document a full-text
	// headline reads (limits.max_headline_document); the contrib plugin
	// prices headlines by it. Values below 1 mean
	// contrib.MaxHeadlineDocument.
	MaxHeadlineDocument int
	Logger              *slog.Logger
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
	// ScalarDescriptions holds the descriptions of the custom scalars in
	// Scalars that have one, keyed by scalar name.
	ScalarDescriptions map[string]string
	// Query/Mutation are the root objects (also present in Objects).
	Query    *Object
	Mutation *Object
	// Subscription is the subscription root (also present in Objects), nil
	// unless Options.Subscriptions is set.
	Subscription *Object

	// TypeForTable maps "schema.table" to the generated object type name.
	TypeForTable map[string]string
	// TypeForComposite maps "schema.type" to the generated object type name
	// of a composite type, or of a table's row values held in other values
	// (see RowValueType), generated on first use.
	TypeForComposite map[string]string
	// InputForComposite maps "schema.type" to the generated input type name.
	InputForComposite map[string]string
	// RowInputForTable maps "schema.table" to the input type of function
	// arguments typed as the table's row type, generated on first use (see
	// rowInput).
	RowInputForTable map[string]string
	// Ext holds plugin-owned data about this schema, keyed by plugin name,
	// and is carried to [Built.Ext]. A plugin that needs state at request
	// time keeps it here rather than on itself, so the executors of
	// several schemas (contracts, watch-mode rebuilds) never read each
	// other's.
	Ext map[string]any

	// domainScalars maps a domain ref ("schema.name") to its custom scalar
	// (see addDomains).
	domainScalars map[string]string

	// contribTypes records the contrib scalar, range object and input type
	// names declared so far (see ensureContrib).
	contribTypes map[string]bool

	log *slog.Logger
	// hidden holds the "schema.table" keys of tables left out of the
	// catalog (partitions), so functions over their row types are skipped.
	hidden map[string]bool
	// relationHidden is the catalog's RelationHidden, kept from the catalog
	// given to New since withoutPartitions copies it.
	relationHidden func(schema, name string) bool
	// tableNames caches tableTypeNames.
	tableNames map[string]bool
	// recordTypes caches the object type generated per function returning
	// OUT/TABLE parameter records.
	recordTypes map[*introspect.Function]string
	// mutationSources describes the table mutation behind each Mutation
	// root field name, and typeSources the table mutation type behind each
	// type name they claimed (see claimType), for collision errors.
	mutationSources map[string]string
	typeSources     map[string]string
	// mutationTypes holds the table mutation input and payload types,
	// removed when no field references them (see pruneMutations).
	mutationTypes map[string]bool
	// errs holds the build errors Build returns (see fail).
	errs []error
	// filterTables maps each table filter input name to its "schema.table".
	filterTables map[string]string
	// operatorInputs holds the names claimed by ClaimOperatorInput.
	operatorInputs map[string]bool
	// nearInputs maps tables to their reference input (see NearInput), nil
	// when its name was taken.
	nearInputs map[*introspect.Table]*Input
	// orderByFields caches FieldsByOrderBy for NearInput.
	orderByFields map[string][]*Field
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
	// CompositeTypeForPG maps "typeschema.typename" -> GraphQL object type name
	// of a composite type, or of a table's nested row values.
	CompositeTypeForPG map[string]string
	// CompositeInputForPG maps "typeschema.typename" -> GraphQL input type name.
	CompositeInputForPG map[string]string
	// RowInputForPG maps "schema.table" -> the input type of function
	// arguments typed as that table's row type.
	RowInputForPG map[string]string
	// InputColumns maps input type name -> field name -> column name.
	InputColumns map[string]map[string]string
	// FilterRelations maps filter input type name -> field name -> relation
	// binding (advanced_filters plugin; empty otherwise).
	FilterRelations map[string]map[string]*FilterRelation
	// FilterComputed maps filter input type name -> field name -> the
	// computed-column function backing it (advanced_filters plugin).
	FilterComputed map[string]map[string]*introspect.Function
	// FilterContrib maps filter input type name -> field name -> the
	// contrib value of a column the field compares (contrib plugin).
	FilterContrib map[string]map[string]*ContribValue
	// InputAggregates maps input type name -> field name -> the aggregate a
	// having or relation-aggregate filter field compares (aggregates plugin).
	InputAggregates map[string]map[string]*Aggregate
	// GroupBy maps groupBy enum type name -> value name -> grouping
	// expression (aggregates plugin).
	GroupBy map[string]map[string]*GroupBy
	// ContribTypes is the set of contrib output types (FullText,
	// KeyValueHash, IntRange, ...) the schema declares. A column whose
	// contrib type is missing here fell back to String because the name
	// was taken; see ContribColumn.
	ContribTypes map[string]bool
	// Ext is the plugin-owned data of [Builder.Ext].
	Ext map[string]any
}

type OrderSpec struct {
	Column string
	Desc   bool
	// Computed orders by a row-type function instead of Column.
	Computed *introspect.Function
	// Relation orders by a related table's column (advanced_filters); Column
	// stays empty for these.
	Relation *OrderRelation
	// Distance orders by spatial distance of Column from its geometry in
	// the field's near: argument (postgis plugin).
	Distance bool
	// Rank orders by the full-text rank of Column against the first
	// matches* operator on it in the field's filter (contrib plugin).
	Rank bool
	// Similarity names the pg_trgm distance expression ("distance",
	// "wordDistance", "strictWordDistance") or the fuzzystrmatch
	// Levenshtein distance ordering by similarity of Column to its
	// reference text in the field's near: argument (contrib plugin); Desc
	// applies to the distance, so best matches first is ascending.
	Similarity string
	// Vector names the distance expression ("l2Distance", "cosineDistance",
	// "chebyshevDistance", "earthDistance", ...) ordering by distance of
	// Column from its reference in the field's near: argument (contrib
	// plugin); nearest first is ascending.
	Vector string
	// Contrib orders by a contrib value of Column (contrib plugin): an
	// accessor, or a derived expression measured against the field's
	// Contrib.Arg reference for Column.
	Contrib *ContribValue
}

// New creates a Builder with all default types generated from the catalog.
// Unless opts.ExposePartitions is set, partitions are dropped from (a shallow
// copy of) cat first, so the builder and the compiler never see them.
func New(cat *introspect.Catalog, inflector inflect.Next, opts Options) *Builder {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	hidden := map[string]bool{}
	relationHidden := cat.RelationHidden
	if !opts.ExposePartitions {
		cat, hidden = withoutPartitions(cat)
	}
	b := &Builder{
		Catalog:            cat,
		Inflect:            inflector,
		Options:            opts,
		Objects:            map[string]*Object{},
		Inputs:             map[string]*Input{},
		Enums:              map[string]*Enum{},
		Scalars:            append([]string{}, customScalars...),
		ScalarDescriptions: map[string]string{},
		domainScalars:      map[string]string{},
		TypeForTable:       map[string]string{},
		TypeForComposite:   map[string]string{},
		InputForComposite:  map[string]string{},
		RowInputForTable:   map[string]string{},
		Ext:                map[string]any{},
		contribTypes:       map[string]bool{},
		log:                opts.Logger,
		hidden:             hidden,
		relationHidden:     relationHidden,
		recordTypes:        map[*introspect.Function]string{},
		mutationSources:    map[string]string{},
		typeSources:        map[string]string{},
		mutationTypes:      map[string]bool{},
		filterTables:       map[string]string{},
		operatorInputs:     map[string]bool{},
		nearInputs:         map[*introspect.Table]*Input{},
	}
	// The GeoJSON scalar is only declared when the database actually has a
	// PostGIS column, so schemas without spatial data are byte-identical to
	// what pdbq generated before PostGIS support existed.
	if catalogHasSpatial(cat) {
		b.Scalars = append(b.Scalars, postgis.TypeName)
	}
	b.Query = &Object{Name: "Query"}
	b.Mutation = &Object{Name: "Mutation"}
	b.Objects["Query"] = b.Query
	b.Objects["Mutation"] = b.Mutation
	if opts.Subscriptions {
		b.Subscription = &Object{Name: "Subscription"}
		b.Objects["Subscription"] = b.Subscription
	}
	b.addPageInfo()
	b.addNode()
	b.addEnums()
	b.addDomains()
	b.addComposites()
	b.addTables()
	b.addFunctions()
	if b.Subscription != nil {
		b.addSubscriptions()
	}
	return b
}

// addSubscriptions declares PostGraphile's simple subscription on the
// Subscription root: listen(topic:, initialEvent:) returning a
// ListenPayload. relatedNode and relatedNodeId exist only when the schema
// has the Node interface.
func (b *Builder) addSubscriptions() {
	payload := &Object{
		Name:        b.uniqueTypeName("ListenPayload"),
		Description: "The payload of a listen subscription event.",
	}
	payload.AddField(&Field{
		Name:        "query",
		Type:        "Query",
		Description: "Our root query field type. Allows us to run any query from our subscription payload.",
		Meta:        &FieldMeta{Kind: KindListenQuery},
	})
	if b.Objects["Node"] != nil {
		payload.AddField(&Field{
			Name:        "relatedNode",
			Type:        "Node",
			Description: "The node named by the notification payload's __node__, or null when it has none or it is not visible.",
			Meta:        &FieldMeta{Kind: KindListenRelatedNode},
		})
		payload.AddField(&Field{
			Name:        "relatedNodeId",
			Type:        "ID",
			Description: "The nodeId of the notification payload's __node__, or null when it has none.",
			Meta:        &FieldMeta{Kind: KindListenRelatedNodeID},
		})
	}
	b.Objects[payload.Name] = payload
	b.addRootField(b.Subscription, &Field{
		Name:        "listen",
		Type:        payload.Name + "!",
		Description: "Listens for notifications sent with pg_notify on the topic's channel (the topic with the configured prefix, postgraphile: by default).",
		Args: []Arg{
			{Name: "topic", Type: "String!"},
			{Name: "initialEvent", Type: "Boolean!", Default: "false", Description: "If true, this subscription will trigger an event as soon as it initiates."},
		},
		Meta: &FieldMeta{Kind: KindListen},
	})
}

// withoutPartitions returns cat without its partitions, plus the keys of the
// tables it dropped. cat itself is returned when it has none.
func withoutPartitions(cat *introspect.Catalog) (*introspect.Catalog, map[string]bool) {
	hidden := map[string]bool{}
	var kept []*introspect.Table
	for _, t := range cat.Tables {
		if t.IsPartition() {
			hidden[tableKey(t.Schema, t.Name)] = true
			continue
		}
		kept = append(kept, t)
	}
	if len(hidden) == 0 {
		return cat, hidden
	}
	cp := &introspect.Catalog{
		FormatVersion:    cat.FormatVersion,
		ServerVersion:    cat.ServerVersion,
		ServerVersionNum: cat.ServerVersionNum,
		Schemas:          cat.Schemas,
		Tables:           kept,
		Enums:            cat.Enums,
		Composites:       cat.Composites,
		Functions:        cat.Functions,
		Extensions:       cat.Extensions,
		Ranges:           cat.Ranges,
		Domains:          cat.Domains,
	}
	return cp, hidden
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
		for i, v := range e.Values {
			gv := b.Inflect(inflect.KindEnumValueName, inflect.Input{Enum: e.Name, Value: v})
			for seen[gv] {
				gv += "_"
			}
			seen[gv] = true
			desc := ""
			if i < len(e.ValueDescriptions) {
				desc = e.ValueDescriptions[i]
			}
			en.Values = append(en.Values, EnumValue{Name: gv, PGValue: v, Description: desc})
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
			if !b.noOutput(f) {
				obj.AddField(&Field{
					Name:        name,
					Type:        b.gqlTypeForColumn(f),
					Description: f.Comment,
					Meta:        &FieldMeta{Kind: KindCompositeField, Column: f},
				})
			}
			in.AddField(&InputField{
				Name:        name,
				Type:        b.gqlInputTypeForColumn(f),
				Description: f.Comment,
				Column:      f.Name,
			})
		}
		ensureField(obj)
	}
}

// gqlTypeForColumn resolves a column to an SDL type reference (without
// nullability marker): its domain's custom scalar when it has one (see
// addDomains), otherwise baseTypeForColumn.
func (b *Builder) gqlTypeForColumn(c *introspect.Column) string {
	if name := b.DomainScalar(c); name != "" {
		if c.IsArray {
			return "[" + name + "!]"
		}
		return name
	}
	return b.baseTypeForColumn(c)
}

// baseTypeForColumn resolves a column to the SDL type reference of its
// (base) pg type, ignoring domain scalars.
func (b *Builder) baseTypeForColumn(c *introspect.Column) string {
	base := ""
	pg := strings.TrimPrefix(c.PGType, "_")
	if e := b.Catalog.EnumByRef(c.EnumRef); e != nil {
		base = b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name})
	} else if e := b.Catalog.Enum(c.TypeSchema, pg); e != nil {
		base = b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name})
	} else if tn, ok := b.TypeForComposite[tableKey(ValueType(c))]; ok {
		base = tn
	} else if t := RowTable(b.Catalog, c); t != nil {
		base = b.RowValueType(t)
	} else if ci := b.ContribColumn(c); ci.Owned() {
		if ci.List() {
			return "[" + ci.Scalar + "!]"
		}
		base = ci.Scalar
	} else {
		base = scalarFor(pg)
	}
	if c.IsArray {
		return "[" + base + "!]"
	}
	return base
}

// unordered reports whether c has no meaningful scalar equality or order,
// and so no condition field, distinctOn value or plain orderBy values: a
// PostGIS geometry, geography or raster column, or a pgvector, cube or
// earth column (their plugins add distance orderings instead).
func (b *Builder) unordered(c *introspect.Column) bool {
	if postgis.IsSpatial(c.PGType) || postgis.IsRaster(c.PGType) {
		return true
	}
	switch b.ContribColumn(c).Kind {
	case contribtype.Vector, contribtype.Cube, contribtype.Earth:
		return true
	}
	return false
}

// ContribColumn resolves a column's contrib mapping (see package
// contribtype), declaring the scalar or range types it needs on first use.
// A column whose type name is already taken by another type keeps the
// String fallback and resolves as not owned.
func (b *Builder) ContribColumn(c *introspect.Column) contribtype.Column {
	ci := contribtype.For(b.Catalog, c)
	if !ci.Owned() || b.ensureContrib(ci) {
		return ci
	}
	return contribtype.Column{Vocabs: ci.Vocabs}
}

// ensureContrib declares the output (and, for ranges, input) types of a
// contrib column once, reporting false when a name is taken by a type it
// did not declare.
func (b *Builder) ensureContrib(ci contribtype.Column) bool {
	if !ci.Declares() || b.contribTypes[ci.Scalar] {
		return true
	}
	names := []string{ci.Scalar}
	if ci.Kind == contribtype.Interval || ci.Kind == contribtype.Earth {
		names = append(names, ci.Input)
	}
	if ci.Kind == contribtype.Range || ci.Kind == contribtype.Multirange {
		names = append(names, contribtype.RangeBound(ci.Scalar), ci.Input, contribtype.RangeBoundInput(ci.Scalar))
	}
	for _, n := range names {
		_, obj := b.Objects[n]
		_, in := b.Inputs[n]
		_, en := b.Enums[n]
		if obj || in || en || slices.Contains(b.Scalars, n) {
			b.log.Warn("contrib type name already taken, column falls back to String", "type", n)
			return false
		}
	}
	for _, n := range names {
		b.contribTypes[n] = true
	}
	switch ci.Kind {
	case contribtype.Interval:
		b.addIntervalTypes()
		return true
	case contribtype.Earth:
		b.addLatLonTypes()
		return true
	case contribtype.Cube:
		b.Scalars = append(b.Scalars, ci.Scalar)
		b.ScalarDescriptions[ci.Scalar] = contribtype.ScalarCubeDescription
		return true
	}
	if ci.Kind != contribtype.Range && ci.Kind != contribtype.Multirange {
		b.Scalars = append(b.Scalars, ci.Scalar)
		return true
	}
	bound := contribtype.RangeBound(ci.Scalar)
	boundIn := contribtype.RangeBoundInput(ci.Scalar)
	desc := "A range of `" + ci.ElemScalar + "`."
	boundDesc := "The value at one end of a range. A range can either include this value, or not."
	synthetic := func() *FieldMeta { return &FieldMeta{Kind: KindSynthetic} }
	b.Objects[ci.Scalar] = &Object{Name: ci.Scalar, Description: desc, Fields: []*Field{
		{Name: "start", Type: bound, Description: "The starting bound of our range; null when unbounded.", Meta: synthetic()},
		{Name: "end", Type: bound, Description: "The ending bound of our range; null when unbounded.", Meta: synthetic()},
		{Name: "isEmpty", Type: "Boolean!", Description: "Whether the range is empty (both bounds read as null). Not part of PostGraphile's range type.", Meta: synthetic()},
	}}
	b.Objects[bound] = &Object{Name: bound, Description: boundDesc, Fields: []*Field{
		{Name: "value", Type: ci.ElemScalar + "!", Description: "The value at one end of our range.", Meta: synthetic()},
		{Name: "inclusive", Type: "Boolean!", Description: "Whether or not the value of this bound is included in the range.", Meta: synthetic()},
	}}
	b.Inputs[ci.Input] = &Input{Name: ci.Input, Description: desc, Fields: []*InputField{
		{Name: "start", Type: boundIn, Description: "The starting bound of our range; omit for unbounded."},
		{Name: "end", Type: boundIn, Description: "The ending bound of our range; omit for unbounded."},
		{Name: "isEmpty", Type: "Boolean", Description: "True for the empty range; the bounds are then ignored. Not part of PostGraphile's range input."},
	}}
	b.Inputs[boundIn] = &Input{Name: boundIn, Description: boundDesc, Fields: []*InputField{
		{Name: "value", Type: ci.ElemScalar + "!", Description: "The value at one end of our range."},
		{Name: "inclusive", Type: "Boolean!", Description: "Whether or not the value of this bound is included in the range."},
	}}
	return true
}

// addIntervalTypes declares the Interval object and IntervalInput input
// types, as PostGraphile does: every field is optional, seconds is the only
// non-integer one and the others carry no overflow into larger units.
func (b *Builder) addIntervalTypes() {
	const desc = "An interval of time that has passed where the smallest distinct unit is a second."
	fieldDesc := map[string]string{
		"seconds": "A quantity of seconds. This is the only non-integer field, as all the other fields will dump their overflow into a smaller unit of time. Intervals don’t have a smaller unit than seconds.",
		"minutes": "A quantity of minutes.",
		"hours":   "A quantity of hours.",
		"days":    "A quantity of days.",
		"months":  "A quantity of months.",
		"years":   "A quantity of years.",
	}
	obj := &Object{Name: contribtype.IntervalType, Description: desc}
	in := &Input{Name: contribtype.IntervalInput, Description: desc}
	for _, name := range contribtype.IntervalFields {
		typ := "Int"
		if name == "seconds" {
			typ = "Float"
		}
		obj.Fields = append(obj.Fields, &Field{Name: name, Type: typ, Description: fieldDesc[name], Meta: &FieldMeta{Kind: KindSynthetic}})
		in.Fields = append(in.Fields, &InputField{Name: name, Type: typ, Description: fieldDesc[name]})
	}
	b.Objects[obj.Name] = obj
	b.Inputs[in.Name] = in
}

// EnsureLatLonTypes declares the LatLon object and LatLonInput input types
// of earth locations unless an earth column already did, for SchemaHook
// plugins taking locations elsewhere (the contrib plugin's near:
// references of point columns). It reports false when another type took
// one of the names.
func (b *Builder) EnsureLatLonTypes() bool {
	if b.contribTypes[contribtype.LatLonType] {
		return true
	}
	for _, n := range []string{contribtype.LatLonType, contribtype.LatLonInput} {
		_, obj := b.Objects[n]
		_, in := b.Inputs[n]
		_, en := b.Enums[n]
		if obj || in || en || slices.Contains(b.Scalars, n) {
			return false
		}
	}
	b.contribTypes[contribtype.LatLonType], b.contribTypes[contribtype.LatLonInput] = true, true
	b.addLatLonTypes()
	return true
}

// addLatLonTypes declares the LatLon object and LatLonInput input types of
// earthdistance's earth columns: a point on the Earth's surface in degrees.
func (b *Builder) addLatLonTypes() {
	const desc = "A location on the Earth's surface (earthdistance earth value), in degrees."
	fields := []struct{ name, desc string }{
		{"lat", "Latitude in degrees, between -90 and 90."},
		{"lon", "Longitude in degrees, between -180 and 180."},
	}
	obj := &Object{Name: contribtype.LatLonType, Description: desc}
	in := &Input{Name: contribtype.LatLonInput, Description: desc}
	for _, f := range fields {
		obj.Fields = append(obj.Fields, &Field{Name: f.name, Type: "Float!", Description: f.desc, Meta: &FieldMeta{Kind: KindSynthetic}})
		in.Fields = append(in.Fields, &InputField{Name: f.name, Type: "Float!", Description: f.desc})
	}
	b.Objects[obj.Name] = obj
	b.Inputs[in.Name] = in
}

// gqlInputTypeForColumn is gqlTypeForColumn for input positions: composite
// columns resolve to their generated input type (object types are not valid
// in argument or input-field position), values of a table's row type to
// their text form (String).
func (b *Builder) gqlInputTypeForColumn(c *introspect.Column) string {
	if in, ok := b.InputForComposite[tableKey(ValueType(c))]; ok {
		if c.IsArray {
			return "[" + in + "!]"
		}
		return in
	}
	if RowTable(b.Catalog, c) != nil || b.HiddenRowType(c) {
		if c.IsArray {
			return "[String!]"
		}
		return "String"
	}
	if ci := b.ContribColumn(c); ci.Kind == contribtype.Range || ci.Kind == contribtype.Earth || (ci.Kind == contribtype.Interval && !ci.Array) {
		return ci.Input
	} else if ci.List() || ci.Kind == contribtype.Interval {
		return "[" + ci.Input + "!]"
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
			if postgis.IsRaster(c.PGType) && !c.IsArray || b.HiddenRowType(c) {
				continue
			}
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
	conditionNames := map[string]string{}
	orderNames := map[string]string{}
	for _, t := range b.Catalog.Tables {
		key := tableKey(t.Schema, t.Name)
		filterNames[key] = b.addFilter(t)
		conditionNames[key] = b.addCondition(t)
		orderNames[key] = b.addOrderBy(t)
		b.addDistinctOn(t)
	}
	for _, t := range b.Catalog.Tables {
		b.addRelations(t)
	}
	for _, t := range b.Catalog.Tables {
		key := tableKey(t.Schema, t.Name)
		b.addQueries(t, filterNames[key], orderNames[key], conditionNames[key])
		b.addMutations(t, filterNames[key])
	}
}

func (b *Builder) uniqueTypeName(name string) string {
	for b.Objects[name] != nil || b.Enums[name] != nil || b.Inputs[name] != nil || slices.Contains(b.Scalars, name) {
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

// costMultiplier reads the @costMultiplier smart comment off a database
// COMMENT, returning 0 when absent or malformed. It is parsed straight from
// the catalog (like advanced_filters does) so the cost override works with the
// smart_comments plugin disabled. Values below 1 are ignored: a zero or
// negative multiplier would let a subtree escape the cost limit entirely.
func costMultiplier(comment string) int {
	tags, _ := smarttags.Parse(comment)
	if tags == nil {
		return 0
	}
	n, err := strconv.Atoi(tags.First("costMultiplier"))
	if err != nil || n < 1 {
		return 0
	}
	return n
}

func (b *Builder) addRelations(t *introspect.Table) {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	obj := b.Objects[typeName]
	// Forward: FK on t points to parent row (many-to-one).
	for _, fk := range t.ForeignKeys {
		if f := b.forwardRelationField(t, fk); f != nil {
			b.addRelationField(obj, f, "fieldName")
		}
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
			b.addRelationField(obj, &Field{
				Name: name,
				Type: b.ensureConnection(other) + "!",
				Args: b.connectionArgs(other, "", "", ""),
				Meta: &FieldMeta{
					Kind: KindRelationBackward, Table: other, FK: fk,
					CostMultiplier: costMultiplier(fk.Comment),
				},
			}, "foreignFieldName")
		}
	}
}

// forwardRelationField builds the relation field following fk from a row of
// t, or nil when the referenced table is not in the schema: the referenced
// row's object, or a connection over the referenced rows for a PERIOD key.
func (b *Builder) forwardRelationField(t *introspect.Table, fk *introspect.ForeignKey) *Field {
	ref := b.Catalog.Table(fk.RefSchema, fk.RefTable)
	refType, ok := b.TypeForTable[tableKey(fk.RefSchema, fk.RefTable)]
	if ref == nil || !ok {
		return nil
	}
	if !fk.ReferencesOne() {
		return &Field{
			Name: b.ForwardRelationName(t, fk),
			Type: b.ensureConnection(ref) + "!",
			Args: b.connectionArgs(ref, "", "", ""),
			Meta: &FieldMeta{
				Kind: KindRelationForward, Table: ref, FK: fk,
				CostMultiplier: costMultiplier(fk.Comment),
			},
		}
	}
	return &Field{
		Name: b.ForwardRelationName(t, fk),
		Type: refType,
		Meta: &FieldMeta{Kind: KindRelationForward, Table: ref, FK: fk},
	}
}

// addRelationField adds the relation field f to obj, recording a build
// error when obj already has a field of that name. tag is the smart comment
// on f's constraint that renames the relation.
func (b *Builder) addRelationField(obj *Object, f *Field, tag string) {
	existing := obj.Field(f.Name)
	if existing == nil {
		obj.AddField(f)
		return
	}
	b.errs = append(b.errs, fmt.Errorf(
		"schema: type %s: relation field %q of constraint %s conflicts with %s of the same name; rename the relation with a @%s smart comment on constraint %s, or switch the naming mode (plugins.settings.simple_names.enabled)",
		obj.Name, f.Name, f.Meta.FK.Name, describeField(existing), tag, f.Meta.FK.Name))
}

// describeField names the source of an object field for a naming-conflict
// error.
func describeField(f *Field) string {
	switch {
	case f.Meta == nil:
		return "a field"
	case f.Meta.FK != nil:
		return "the relation of constraint " + f.Meta.FK.Name
	case f.Meta.Kind == KindComputed && f.Meta.Function != nil:
		return "the computed column " + f.Meta.Function.Schema + "." + f.Meta.Function.Name
	case f.Meta.Kind == KindColumn:
		return "a column field"
	}
	return "a field"
}

// addPayloadFields appends the fields PostGraphile gives mutation payloads:
// query (the Query root, readable alongside the mutation's result) on every
// payload and, when t is set (the payload carries one row of t), the row's
// forward relations and a <row>Edge for Relay connection updates. A
// relation whose name is one of the payload's own fields (those already on
// obj, the edge, and reserved, the fields the caller adds afterwards) is
// left out with a warning; the row field still reaches it.
func (b *Builder) addPayloadFields(obj *Object, t *introspect.Table, reserved ...string) {
	b.addRootField(obj, &Field{
		Name:        "query",
		Type:        "Query",
		Description: "The root query type, to read any data alongside the mutation's result.",
		Meta:        &FieldMeta{Kind: KindPayloadQuery},
	})
	if t == nil {
		return
	}
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	hasEdge := t.PrimaryKey != nil && typeName != ""
	edgeName := ""
	if hasEdge {
		edgeName = strings.ToLower(typeName[:1]) + typeName[1:] + "Edge"
	}
	for _, fk := range t.ForeignKeys {
		f := b.forwardRelationField(t, fk)
		if f == nil {
			continue
		}
		if obj.Field(f.Name) != nil || f.Name == edgeName || slices.Contains(reserved, f.Name) {
			b.log.Warn("relation left out of mutation payload: a payload field has its name",
				"type", obj.Name, "field", f.Name, "constraint", fk.Name)
			continue
		}
		obj.AddField(f)
	}
	if !hasEdge {
		return
	}
	edge := &Field{
		Name:        edgeName,
		Type:        typeName + "Edge",
		Description: "An edge for the payload's " + typeName + ", to add to a Relay connection. Its cursor is the row's nodeId, valid under every ordering; orderBy is accepted for PostGraphile compatibility.",
		Meta:        &FieldMeta{Kind: KindPayloadEdge, Table: t},
	}
	b.ensureConnection(t)
	if order := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name}); b.Enums[order] != nil {
		edge.Args = []Arg{{Name: "orderBy", Type: "[" + order + "!]"}}
	}
	b.addRootField(obj, edge)
}

// ForwardRelationName returns the name of the relation field following fk
// from t to the referenced table: [inflect.KindRelationForward] for a key
// referencing one row, [inflect.KindRelationForwardMany] for a PERIOD key.
// The name depends on fk alone, never on the other fields of t's type (a
// conflict is a Build error). SchemaHook plugins adding surface keyed by
// the relation (filters, nested inputs) use it so their names line up with
// the selection field.
func (b *Builder) ForwardRelationName(t *introspect.Table, fk *introspect.ForeignKey) string {
	kind := inflect.KindRelationForward
	if !fk.ReferencesOne() {
		kind = inflect.KindRelationForwardMany
	}
	return b.Inflect(kind, inflect.Input{
		Schema: t.Schema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns,
		Constraint: fk.Name,
	})
}

// filterableColumns applies the indexed-only policy plus config overrides.
// Composite-typed and row-typed columns are never filterable: their object
// types have no meaningful scalar operator set, and comparing a row's text
// form would test the columns it hides.
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
		if _, ok := b.TypeForComposite[tableKey(ValueType(c))]; ok || RowTable(b.Catalog, c) != nil || b.HiddenRowType(c) {
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
		// Spatial columns get their operator set from the postgis plugin
		// (GeoJSONFilter). Generating a scalar operator input here would
		// leave an unused type behind whenever the plugin is enabled, and an
		// unusable one when it is not.
		if postgis.IsSpatial(c.PGType) || postgis.IsRaster(c.PGType) {
			continue
		}
		opInput := b.FilterInputFor(c)
		in.AddField(&InputField{
			Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
			Type:   opInput,
			Column: c.Name,
		})
	}
	b.Inputs[name] = in
	b.filterTables[name] = tableKey(t.Schema, t.Name)
	return name
}

// addCondition creates the <Type>Condition input: one field per filterable
// column, typed as the column's plain input type. Every provided field is an
// equality test on its column (an explicit null matches SQL NULL) and all
// tests are ANDed. Returns the type name or "" when no column qualifies.
func (b *Builder) addCondition(t *introspect.Table) string {
	cols := b.filterableColumns(t)
	if len(cols) == 0 {
		return ""
	}
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]
	name := b.Inflect(inflect.KindConditionTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	in := &Input{Name: name, Description: "Equality condition for " + typeName + ": every provided field must equal its value (all fields are ANDed); an explicit null matches SQL NULL."}
	for _, c := range cols {
		// Spatial columns have no plain equality operator; use the filter
		// argument's spatial predicates (equals, intersects, ...) instead.
		if b.unordered(c) {
			continue
		}
		in.AddField(&InputField{
			Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: c.Name}),
			Type:   b.gqlInputTypeForColumn(c),
			Column: c.Name,
		})
	}
	b.Inputs[name] = in
	return name
}

// FilterableColumns exposes the filterable/orderable column set (indexed-only
// policy plus config overrides) for SchemaHook plugins adding filter or order
// surface over another table's columns (e.g. advanced_filters relational
// ordering).
func (b *Builder) FilterableColumns(t *introspect.Table) []*introspect.Column {
	return b.filterableColumns(t)
}

// GraphQLTypeFor returns the GraphQL output type a column-shaped value maps
// to (without the non-null marker), e.g. BigInt for int8 or [String!] for a
// text array — exported for SchemaHook plugins typing derived fields.
func (b *Builder) GraphQLTypeFor(c *introspect.Column) string {
	return b.gqlTypeForColumn(c)
}

// GraphQLInputTypeFor returns the GraphQL input type a column takes in
// mutation inputs and key arguments (without the non-null marker), e.g. a
// composite's input type or a range's bound input — exported for SchemaHook
// plugins adding column-valued input fields (e.g. nested_mutations keys).
func (b *Builder) GraphQLInputTypeFor(c *introspect.Column) string {
	return b.gqlInputTypeForColumn(c)
}

// FilterInputFor returns (creating if needed) the operator input type for
// a column-shaped value — exported for SchemaHook plugins adding filterable
// fields (e.g. advanced_filters computed columns).
func (b *Builder) FilterInputFor(c *introspect.Column) string {
	if ci := b.ContribColumn(c); ci.Owned() && ci.OpsInput != "" {
		return b.contribOpInput(ci)
	}
	gqlType := b.baseTypeForColumn(c)
	elem := strings.TrimSuffix(strings.TrimPrefix(gqlType, "["), "!]")
	return b.opInputFor(elem, c.PGType, c.IsArray)
}

// opInputFor returns (creating if needed) the shared operator input type for
// a scalar, e.g. StringFilter.
func (b *Builder) opInputFor(gqlType, pgType string, isArray bool) string {
	name := gqlType + "Filter"
	if isArray {
		name = gqlType + "ListFilter"
	} else if pgType == "jsonb" || pgType == "json" {
		name = "JSONFilter"
	}
	b.ClaimOperatorInput(name)
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

// contribOpInput returns (creating if needed) the shared operator input of a
// contrib type (FullTextFilter, KeyValueHashFilter, ...) holding its
// generic comparison operators; the contrib plugin adds the vocabulary
// operators.
func (b *Builder) contribOpInput(ci contribtype.Column) string {
	b.ClaimOperatorInput(ci.OpsInput)
	if _, ok := b.Inputs[ci.OpsInput]; ok {
		return ci.OpsInput
	}
	in := &Input{Name: ci.OpsInput}
	for _, op := range contribFilterOps(ci) {
		in.AddField(&InputField{Name: op.Name, Type: op.Type})
	}
	b.Inputs[ci.OpsInput] = in
	return ci.OpsInput
}

// ClaimOperatorInput records name as the name of a scalar operator input
// type (StringFilter, GeoJSONFilter, HavingIntFilter, ...). Plugins call it
// before declaring one, also when the name turns out to be taken: Build
// fails when a table's <Type>Filter input has a claimed name, since the
// two would share one GraphQL type.
func (b *Builder) ClaimOperatorInput(name string) {
	b.operatorInputs[name] = true
}

// NearArg is the list field argument holding the per-column references
// that distance and similarity orderBy values and score fields measure
// against (postgis and contrib plugins).
const NearArg = "near"

// NearInput returns table t's reference input, <Type>NearInput, declaring
// it on first use, or nil when another type took the name (logged once).
// Declaring it adds the near: argument to every field taking t's orderBy
// enum (as FieldsByOrderBy found them on the first call). Plugins add one
// field per reference-taking column, named after the column and bound to
// it through InputField.Column; Build drops an input left without fields,
// with its arguments.
func (b *Builder) NearInput(t *introspect.Table) *Input {
	if in, seen := b.nearInputs[t]; seen {
		return in
	}
	key := tableKey(t.Schema, t.Name)
	typeName := b.TypeForTable[key]
	name := typeName + "NearInput"
	_, obj := b.Objects[name]
	_, input := b.Inputs[name]
	_, enum := b.Enums[name]
	if obj || input || enum {
		b.log.Warn("type name already taken, near: argument skipped", "type", name, "table", key)
		b.nearInputs[t] = nil
		return nil
	}
	in := &Input{
		Name: name,
		Description: "Per-column references for the distance and similarity orderBy values and score fields of " +
			typeName + ".",
	}
	b.Inputs[name] = in
	b.nearInputs[t] = in
	if b.orderByFields == nil {
		b.orderByFields = b.FieldsByOrderBy()
	}
	enumName := b.Inflect(inflect.KindOrderByTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
	for _, f := range b.orderByFields[enumName] {
		if !slices.ContainsFunc(f.Args, func(a Arg) bool { return a.Name == NearArg }) {
			f.Args = append(f.Args, Arg{Name: NearArg, Type: name})
		}
	}
	return in
}

// pruneNearInputs removes the reference inputs left without fields and the
// near: arguments typed by them.
func (b *Builder) pruneNearInputs() {
	empty := map[string]bool{}
	for _, in := range b.nearInputs {
		if in != nil && len(in.Fields) == 0 {
			empty[in.Name] = true
			delete(b.Inputs, in.Name)
		}
	}
	if len(empty) == 0 {
		return
	}
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			f.Args = slices.DeleteFunc(f.Args, func(a Arg) bool { return a.Name == NearArg && empty[a.Type] })
		}
	}
}

// operatorInputCollision reports a table filter input sharing its name with
// a scalar operator input, naming the first such pair in name order.
func (b *Builder) operatorInputCollision() error {
	for _, name := range sortedKeys(b.filterTables) {
		if b.operatorInputs[name] {
			return fmt.Errorf("schema: the filter input of table %s and a scalar operator input are both named %s; "+
				"rename the table with an @name smart comment (or an inflection plugin)", b.filterTables[name], name)
		}
	}
	return nil
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
		// DISTINCT ON needs a btree equality operator, which PostGIS types do
		// not provide; offering the value would only produce a runtime error.
		if b.unordered(c) {
			continue
		}
		en.Values = append(en.Values, EnumValue{Name: coreinflect.EnumValue(c.Name), Column: c.Name})
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
		// PostGIS values have no meaningful linear order; the postgis plugin
		// adds <COLUMN>_DISTANCE_ASC/_DESC instead, which sorts by distance
		// from a supplied reference geometry.
		if b.unordered(c) {
			continue
		}
		base := coreinflect.EnumValue(c.Name)
		en.Values = append(en.Values,
			EnumValue{Name: base + "_ASC", Column: c.Name},
			EnumValue{Name: base + "_DESC", Column: c.Name, Desc: true},
		)
	}
	b.Enums[name] = en
	return name
}

func (b *Builder) listArgs(t *introspect.Table, filterName, orderName, conditionName string) []Arg {
	if filterName == "" {
		filterName = b.Inflect(inflect.KindFilterTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		if _, ok := b.Inputs[filterName]; !ok {
			filterName = ""
		}
	}
	if conditionName == "" {
		conditionName = b.Inflect(inflect.KindConditionTypeName, inflect.Input{Schema: t.Schema, Table: t.Name})
		if _, ok := b.Inputs[conditionName]; !ok {
			conditionName = ""
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
	if conditionName != "" {
		args = append(args, Arg{Name: "condition", Type: conditionName})
	}
	return args
}

// connectionArgs is listArgs plus Relay cursor pagination.
func (b *Builder) connectionArgs(t *introspect.Table, filterName, orderName, conditionName string) []Arg {
	args := []Arg{
		{Name: "first", Type: "Int"},
		{Name: "last", Type: "Int"},
		{Name: "offset", Type: "Int"},
		{Name: "before", Type: "Cursor"},
		{Name: "after", Type: "Cursor"},
	}
	return append(args, b.listArgs(t, filterName, orderName, conditionName)[2:]...)
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

func (b *Builder) addQueries(t *introspect.Table, filterName, orderName, conditionName string) {
	typeName := b.TypeForTable[tableKey(t.Schema, t.Name)]

	// Relay-style connection is the collection query.
	connName := b.ensureConnection(t)
	b.addRootField(b.Query, &Field{
		Name:        b.Inflect(inflect.KindAllRowsField, inflect.Input{Schema: t.Schema, Table: t.Name}),
		Type:        connName + "!",
		Description: "Reads a paginated connection of " + typeName + ".",
		Args:        b.connectionArgs(t, filterName, orderName, conditionName),
		Meta:        &FieldMeta{Kind: KindConnectionQuery, Table: t},
	})

	// Single-row lookups by PK and by each unique constraint; temporal
	// (WITHOUT OVERLAPS) keys get a point-in-time lookup instead.
	seen := map[string]bool{}
	for _, con := range b.lookupKeys(t) {
		kind := inflect.KindRowByUniqueField
		if con == t.PrimaryKey {
			kind = inflect.KindRowByPKField
		}
		keyCols := con.EqualityColumns()
		name := b.Inflect(kind, inflect.Input{Schema: t.Schema, Table: t.Name, Columns: keyCols})
		if seen[name] {
			continue
		}
		seen[name] = true
		var args []Arg
		ok := true
		for _, cn := range keyCols {
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
		meta := &FieldMeta{Kind: KindRowByKey, Table: t, KeyColumns: keyCols}
		desc := ""
		if period := con.PeriodColumn(); period != "" {
			if slices.ContainsFunc(args, func(a Arg) bool { return a.Name == "at" }) {
				continue
			}
			args = append(args, Arg{
				Name:        "at",
				Type:        scalarFor(RangeElementType(t.Column(period).PGType)) + "!",
				Description: "Value the row's " + b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: period}) + " range must contain.",
			})
			meta.PeriodColumn = period
			desc = "Reads the " + typeName + " whose period contains the given point."
		}
		b.addRootField(b.Query, &Field{
			Name:        name,
			Type:        typeName,
			Description: desc,
			Args:        args,
			Meta:        meta,
		})
	}
}

// lookupKeys returns the keys single-row lookups are generated for: the row
// keys (see [introspect.Table.RowKeys]) followed by the temporal keys whose
// period column is a built-in range or multirange, which are looked up by
// their equality columns plus a contained point.
func (b *Builder) lookupKeys(t *introspect.Table) []*introspect.Constraint {
	keys := t.RowKeys()
	for _, con := range append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...) {
		if con == nil || !con.WithoutOverlaps {
			continue
		}
		if c := t.Column(con.PeriodColumn()); c != nil && !c.IsArray && RangeElementType(c.PGType) != "" {
			keys = append(keys, con)
		}
	}
	return keys
}

func (b *Builder) addFunctions() {
	if !b.Options.Functions {
		return
	}
	for _, f := range b.Catalog.Functions {
		if len(f.PolymorphicFamilies()) > 0 {
			b.log.Info("skipping function: polymorphic signature (name the concrete type with an @anyelement or @anycompatible smart comment)",
				"function", f.Schema+"."+f.Name)
			continue
		}
		if ref, partition := b.unexposedType(f); partition {
			b.log.Debug("skipping function: uses the row type of a table left out of the schema",
				"function", f.Schema+"."+f.Name, "table", ref)
			continue
		} else if ref != "" {
			b.log.Warn("skipping function: uses a type from a schema outside schema.schemas (the row type of an unexposed table?)",
				"function", f.Schema+"."+f.Name, "type", ref)
			continue
		}
		// A stable function whose first argument is a table's row type is a
		// computed column on that table's type, not a root field.
		if len(f.Args) > 0 && f.Volatility != introspect.VolatilityVolatile {
			if t := b.Catalog.Table(f.Args[0].TypeSchema, f.Args[0].PGType); t != nil {
				b.addComputedField(t, f)
				continue
			}
		}
		if arg := b.unexposedRowArg(f); arg != "" {
			b.log.Info("skipping function: row-type argument of a table without a GraphQL type",
				"function", f.Schema+"."+f.Name, "arg", arg)
			continue
		}
		if hasUnnamedArg(f) {
			b.log.Debug("skipping function: unnamed argument (GraphQL arguments need names)",
				"function", f.Schema+"."+f.Name)
			continue
		}
		// The JWT mint path replaces a mint-composite result with a signed
		// token string, so that composite stays a String here.
		retBase, ok := b.functionReturnBase(f, true)
		if !ok {
			b.log.Info("skipping function: RETURNS record without OUT parameters has no fixed shape (declare OUT parameters or RETURNS TABLE to expose it)",
				"function", f.Schema+"."+f.Name, "type", f.ReturnType)
			continue
		}
		retType := retBase
		switch {
		case f.ReturnsSet:
			retType = "[" + retBase + "!]!"
		case FuncReturnColumn(b.Catalog, f).IsArray:
			// An array return type is a list of the element type.
			retType = "[" + retBase + "!]"
		}
		fieldName := b.Inflect(inflect.KindFunctionField, inflect.Input{Schema: f.Schema, Function: f.Name})
		if f.Volatility == introspect.VolatilityVolatile {
			// Volatile functions are mutations with PostGraphile's payload
			// shape: fn(input: FnInput!): FnPayload! { <result>
			// clientMutationId query ... }.
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
					Name:        coreinflect.LowerCamel(a.Name),
					Type:        b.argGQLType(a),
					Description: argDescription(a),
				})
			}
			in.AddField(&InputField{Name: "clientMutationId", Type: "String"})
			b.Inputs[inputName] = in

			payload := &Object{Name: payloadName}
			if f.ReturnType != "void" {
				resultType := retType
				if f.ReturnsSet {
					resultType = "[" + retBase + "!]"
				}
				payload.AddField(&Field{
					Name: b.functionResultField(f, retType),
					Type: resultType,
					Meta: &FieldMeta{Kind: KindPayloadResult, Function: f},
				})
			}
			b.addRootField(payload, &Field{Name: "clientMutationId", Type: "String", Meta: &FieldMeta{Kind: KindClientMutationID}})
			var row *introspect.Table
			if !f.ReturnsSet {
				row = b.Catalog.Table(f.ReturnTypeSchema, f.ReturnType)
			}
			b.addPayloadFields(payload, row)
			b.Objects[payloadName] = payload
			b.addRootField(b.Mutation, &Field{
				Name:        fieldName,
				Type:        payloadName + "!",
				Description: f.Comment,
				Args:        []Arg{{Name: "input", Type: inputName + "!"}},
				Meta:        &FieldMeta{Kind: KindFunction, Function: f},
			})
			continue
		}
		b.addRootField(b.Query, &Field{
			Name:        fieldName,
			Type:        retType,
			Description: f.Comment,
			Args:        b.functionArgs(f.Args),
			Meta:        &FieldMeta{Kind: KindFunction, Function: f},
		})
	}
}

// functionResultField names the result field of volatile function f's
// payload (inflect.KindFunctionResultField) from its GraphQL type retType. A
// JWT-minting function is named after its composite, not the String token.
func (b *Builder) functionResultField(f *introspect.Function, retType string) string {
	in := inflect.Input{
		Schema:   f.Schema,
		Function: f.Name,
		Value:    strings.Trim(retType, "[]!"),
		IsList:   strings.HasPrefix(retType, "["),
	}
	if len(f.ReturnColumns) == 1 {
		in.Column = f.ReturnColumns[0].Name
	}
	if f.ReturnType == "record" && len(f.ReturnColumns) > 1 {
		in.Value = ""
	}
	if key := tableKey(f.ReturnTypeSchema, f.ReturnType); key == b.Options.JWTType {
		if tn, ok := b.TypeForComposite[key]; ok {
			in.Value = tn
		}
	}
	return b.Inflect(inflect.KindFunctionResultField, in)
}

// functionArgs maps function parameters to GraphQL field arguments. All are
// nullable: an omitted argument is left out of the call, so a parameter with
// a DEFAULT takes it, and an explicit null passes SQL NULL.
func (b *Builder) functionArgs(params []introspect.FuncArg) []Arg {
	var args []Arg
	for _, a := range params {
		args = append(args, Arg{
			Name:        coreinflect.LowerCamel(a.Name),
			Type:        b.argGQLType(a),
			Description: argDescription(a),
		})
	}
	return args
}

// argDescription documents an optional (DEFAULT) or VARIADIC parameter.
func argDescription(a introspect.FuncArg) string {
	var parts []string
	if a.HasDefault {
		if a.Default != "" {
			parts = append(parts, "Optional; defaults to `"+a.Default+"` when omitted.")
		} else {
			parts = append(parts, "Optional; the function's default applies when omitted.")
		}
	}
	if a.Variadic() {
		parts = append(parts, "Variadic: pass the values as a list.")
	}
	return strings.Join(parts, " ")
}

// functionReturnBase resolves the GraphQL type of one value f returns (list
// wrappers left to the caller): a table's object type, the generated record
// type for OUT/TABLE parameters, an enum, a composite's object type, a
// domain's custom scalar, or a scalar. With mint set, the JWT composite
// (Options.JWTType) stays a String. ok is false when the result has no GraphQL representation (RETURNS record
// without output parameters).
func (b *Builder) functionReturnBase(f *introspect.Function, mint bool) (string, bool) {
	switch {
	case f.ReturnType == "void":
		return "Boolean", true
	case f.ReturnType == "record" && f.ReturnTypeSchema == "pg_catalog":
		if len(f.ReturnColumns) == 0 {
			return "", false
		}
		return b.functionRecordType(f), true
	}
	if tn, ok := b.TypeForTable[tableKey(f.ReturnTypeSchema, f.ReturnType)]; ok {
		return tn, true
	}
	ret := FuncReturnColumn(b.Catalog, f)
	elem := strings.TrimPrefix(ret.PGType, "_")
	key := tableKey(ret.TypeSchema, elem)
	if e := b.Catalog.Enum(ret.TypeSchema, elem); e != nil {
		return b.Inflect(inflect.KindEnumTypeName, inflect.Input{Schema: e.Schema, Enum: e.Name}), true
	}
	if tn, ok := b.TypeForComposite[key]; ok && (!mint || key != b.Options.JWTType) {
		return tn, true
	}
	if t := RowTable(b.Catalog, ret); t != nil {
		return b.RowValueType(t), true
	}
	if name := b.DomainScalar(ret); name != "" {
		return name, true
	}
	if ci := b.ContribColumn(ret); ci.Owned() {
		if ci.List() {
			return "[" + ci.Scalar + "!]", true
		}
		return ci.Scalar, true
	}
	return scalarFor(elem), true
}

// functionRecordType returns the object type of the rows f returns through
// OUT/INOUT/TABLE parameters, generating it on first use: one nullable field
// per output parameter, bound like a table column (KindColumn) to a
// synthetic column named after the parameter.
func (b *Builder) functionRecordType(f *introspect.Function) string {
	if name, ok := b.recordTypes[f]; ok {
		return name
	}
	name := b.uniqueTypeName(b.Inflect(inflect.KindFunctionRecord, inflect.Input{Schema: f.Schema, Function: f.Name}))
	src := &introspect.Table{Schema: f.Schema, Name: f.Name, Kind: introspect.RelView}
	obj := &Object{Name: name, Description: "The output parameters of " + f.Name + "."}
	for i, a := range f.ReturnColumns {
		c := FuncArgColumn(b.Catalog, a)
		c.Position = i + 1
		src.Columns = append(src.Columns, c)
		obj.AddField(&Field{
			Name: b.Inflect(inflect.KindFieldName, inflect.Input{Schema: f.Schema, Table: f.Name, Column: a.Name}),
			Type: b.gqlTypeForColumn(c),
			Meta: &FieldMeta{Kind: KindColumn, Table: src, Column: c},
		})
	}
	b.Objects[name] = obj
	b.recordTypes[f] = name
	return name
}

// unexposedType returns the "schema.type" of the first type f returns or
// takes that the schema must not expose, or "": the row type of a table left
// out of the catalog (a partition, see Options.ExposePartitions, or a table
// a contract or a bare @omit removed; partition is then true), or a type
// from a schema outside the introspected ones that is no known enum,
// composite, domain, range or extension type. The latter
// is typically the row type of a table in an unexposed schema, which would
// otherwise be typed String and return whole rows as JSON.
func (b *Builder) unexposedType(f *introspect.Function) (ref string, partition bool) {
	refs := []introspect.FuncArg{{PGType: f.ReturnType, TypeSchema: f.ReturnTypeSchema, Domain: f.ReturnDomain}}
	refs = append(append(refs, f.Args...), f.ReturnColumns...)
	for _, a := range refs {
		key := tableKey(a.TypeSchema, strings.TrimPrefix(a.PGType, "_"))
		if b.hidden[key] || b.HiddenRowType(FuncArgColumn(b.Catalog, a)) {
			return key, true
		}
		if b.foreignType(a) {
			return key, false
		}
	}
	return "", false
}

// foreignType reports whether a's type lives in a schema neither built in
// nor introspected and resolves to nothing the catalog describes.
func (b *Builder) foreignType(a introspect.FuncArg) bool {
	elem := strings.TrimPrefix(a.PGType, "_")
	switch {
	case a.TypeSchema == "" || a.TypeSchema == "pg_catalog" || a.Domain != "",
		slices.Contains(b.Catalog.Schemas, a.TypeSchema),
		b.Catalog.Table(a.TypeSchema, elem) != nil,
		b.Catalog.Composite(a.TypeSchema, elem) != nil,
		b.Catalog.Enum(a.TypeSchema, elem) != nil,
		postgis.IsSpatial(elem):
		return false
	}
	for _, r := range b.Catalog.Ranges {
		if r.Schema == a.TypeSchema && (r.Name == elem || r.Multirange == elem) {
			return false
		}
	}
	for _, e := range b.Catalog.Extensions {
		if e.Schema == a.TypeSchema {
			return false
		}
	}
	return !b.ContribColumn(FuncArgColumn(b.Catalog, a)).Owned()
}

// argGQLType maps a function argument's pg type to a GraphQL input type the
// same way a column's input type is chosen: enums take the generated enum
// type (callers pass value names), composites their generated input type,
// PostGIS types GeoJSON. Array types ("_uuid", and every VARIADIC
// parameter) become lists; arrays of contrib types other than ltree[] are
// lists of their text form, like array columns of those types. A domain
// parameter takes its custom scalar or else its base type (FuncArgColumn).
func (b *Builder) argGQLType(a introspect.FuncArg) string {
	arr := FuncArgColumn(b.Catalog, a)
	elem := &introspect.Column{
		Name: a.Name, PGType: strings.TrimPrefix(arr.PGType, "_"),
		TypeSchema: arr.TypeSchema, BaseTypeSchema: arr.BaseTypeSchema, Domain: arr.Domain,
	}
	base := b.gqlInputTypeForColumn(elem)
	if t := b.Catalog.Table(elem.TypeSchema, elem.PGType); t != nil && elem.Domain == "" {
		if _, ok := b.TypeForTable[tableKey(t.Schema, t.Name)]; ok {
			base = b.rowInput(t)
		}
	}
	if arr.IsArray {
		if b.ContribColumn(elem).Owned() && !b.ContribColumn(arr).Owned() {
			base = "String"
		}
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

// unexposedRowArg returns the name of the first argument typed as the row
// type (or an array of it) of a catalog table that has no object type, or
// "" when there is none.
func (b *Builder) unexposedRowArg(f *introspect.Function) string {
	for _, a := range f.Args {
		if t := b.Catalog.Table(a.TypeSchema, strings.TrimPrefix(a.PGType, "_")); t != nil {
			if _, ok := b.TypeForTable[tableKey(t.Schema, t.Name)]; !ok {
				return a.Name
			}
		}
	}
	return ""
}

// rowInput returns the input type of function arguments typed as t's row
// type, generating it on first use: <Type>RowInput with one nullable field per
// column, bound like a composite input (the argument is populated from it
// with jsonb_populate_record, absent fields NULL). Generated and identity
// columns are included: the argument is a value handed to the function,
// not a write to t. The smart_comments plugin removes columns hidden from
// reads.
func (b *Builder) rowInput(t *introspect.Table) string {
	key := tableKey(t.Schema, t.Name)
	if name, ok := b.RowInputForTable[key]; ok {
		return name
	}
	name := b.uniqueTypeName(b.TypeForTable[key] + "RowInput")
	in := &Input{Name: name, Description: "The " + b.TypeForTable[key] + " row passed to a function."}
	for _, c := range t.Columns {
		in.AddField(&InputField{
			Name:        b.Inflect(inflect.KindFieldName, inflect.Input{Schema: t.Schema, Table: t.Name, Column: c.Name}),
			Type:        b.gqlInputTypeForColumn(c),
			Description: c.Comment,
			Column:      c.Name,
		})
	}
	b.Inputs[name] = in
	b.RowInputForTable[key] = name
	return name
}

// addComputedField exposes a stable/immutable function whose first argument
// is t's row type as a computed field on t's object type. A `<table>_` name
// prefix is stripped by the default inflector (users_post_count -> postCount);
// remaining arguments become GraphQL field arguments.
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
	base, ok := b.functionReturnBase(f, false)
	if !ok {
		b.log.Debug("skipping computed column: return type has no GraphQL representation", "function", fnKey)
		return
	}
	var fieldType string
	switch {
	case f.ReturnsSet:
		fieldType = "[" + base + "!]!"
	case strings.HasPrefix(f.ReturnType, "_"):
		fieldType = "[" + base + "!]"
	default:
		fieldType = base // always nullable: the function may return NULL
	}
	field := &Field{
		Name:        b.Inflect(inflect.KindComputedField, inflect.Input{Schema: f.Schema, Table: t.Name, Function: f.Name}),
		Type:        fieldType,
		Description: f.Comment,
		Args:        b.functionArgs(f.Args[1:]),
		Meta:        &FieldMeta{Kind: KindComputed, Table: t, Function: f},
	}
	if rel := obj.Field(field.Name); rel != nil && rel.Meta != nil && rel.Meta.FK != nil {
		tag := "fieldName"
		if rel.Meta.Kind == KindRelationBackward {
			tag = "foreignFieldName"
		}
		b.errs = append(b.errs, fmt.Errorf(
			"schema: type %s: relation field %q of constraint %s conflicts with %s of the same name; rename the relation with a @%s smart comment on constraint %s or the function with @name, or switch the naming mode (plugins.settings.simple_names.enabled)",
			obj.Name, field.Name, rel.Meta.FK.Name, describeField(field), tag, rel.Meta.FK.Name))
		return
	}
	b.addRootField(obj, field)
}

// FieldsByOrderBy indexes every object field that takes an orderBy argument
// by the argument's enum type name, so plugins extending an orderBy enum's
// consumers need not rescan the whole schema once per table. The index is a
// snapshot: fields and arguments added afterwards are not reflected.
func (b *Builder) FieldsByOrderBy() map[string][]*Field {
	out := map[string][]*Field{}
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			for _, a := range f.Args {
				if a.Name == "orderBy" {
					enum := strings.Trim(a.Type, "[]!")
					out[enum] = append(out[enum], f)
				}
			}
		}
	}
	return out
}

// incrementalDirectives declares the defer and stream directives as
// graphql-js 17 does.
const incrementalDirectives = `"""Directs the executor to defer this fragment when the ` + "`if`" + ` argument is true or undefined."""
directive @defer(
  """Deferred when true or undefined."""
  if: Boolean! = true
  """Unique name"""
  label: String
) on FRAGMENT_SPREAD | INLINE_FRAGMENT

"""Directs the executor to stream plural fields when the ` + "`if`" + ` argument is true or undefined."""
directive @stream(
  """Number of items to return immediately"""
  initialCount: Int! = 0
  """Stream when true or undefined."""
  if: Boolean! = true
  """Unique name"""
  label: String
) on FIELD

`

// liveDirective is declared with live.enabled: a query operation carrying it
// is served as a live query over WebSocket.
const liveDirective = `"""Re-sends the query's result whenever the data it read changes (live query; WebSocket only, over HTTP the query runs once)."""
directive @live on QUERY

`

// addLiveFields repeats the Query root fields on the Subscription root, as
// PostGraphile's live queries do: a subscription selecting one of them is a
// live query. A Query field whose name the Subscription root already has
// (listen) is skipped with a warning.
func (b *Builder) addLiveFields() {
	for _, f := range b.Query.Fields {
		if f.Meta != nil && f.Meta.Kind == KindSynthetic {
			continue
		}
		cp := *f
		cp.Args = slices.Clone(f.Args)
		b.addRootField(b.Subscription, &cp)
	}
}

// Build renders SDL, validates it, and returns the executable schema + meta.
// It fails with every naming conflict found while generating the schema (a
// relation field named like another field of its type, a table mutation
// name collision) and when a table filter input and a scalar operator input
// share a name.
func (b *Builder) Build() (*Built, error) {
	if err := errors.Join(append(b.errs, b.operatorInputCollision())...); err != nil {
		return nil, err
	}
	b.pruneMutations()
	b.pruneNearInputs()
	if len(b.Mutation.Fields) == 0 {
		delete(b.Objects, "Mutation")
	}
	if b.Subscription != nil && b.Options.Live {
		b.addLiveFields()
	}
	if b.Subscription != nil && len(b.Subscription.Fields) == 0 {
		delete(b.Objects, "Subscription")
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
		Ext:                 b.Ext,
		TypeForTable:        b.TypeForTable,
		TableForType:        map[string]*introspect.Table{},
		OrderBy:             map[string]map[string]OrderSpec{},
		EnumValues:          map[string]map[string]string{},
		EnumTypeForPG:       map[string]string{},
		CompositeTypeForPG:  b.TypeForComposite,
		CompositeInputForPG: b.InputForComposite,
		RowInputForPG:       b.RowInputForTable,
		InputColumns:        map[string]map[string]string{},
		FilterRelations:     map[string]map[string]*FilterRelation{},
		FilterComputed:      map[string]map[string]*introspect.Function{},
		FilterContrib:       map[string]map[string]*ContribValue{},
		InputAggregates:     map[string]map[string]*Aggregate{},
		GroupBy:             map[string]map[string]*GroupBy{},
		ContribTypes:        b.contribTypes,
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
		groups := map[string]*GroupBy{}
		for _, v := range en.Values {
			if v.GroupBy != nil {
				groups[v.Name] = v.GroupBy
			}
			if v.Column != "" || v.Computed != nil || v.Relation != nil {
				ordered[v.Name] = OrderSpec{
					Column: v.Column, Desc: v.Desc, Computed: v.Computed, Relation: v.Relation,
					Distance: v.Distance, Rank: v.Rank, Similarity: v.Similarity, Vector: v.Vector,
					Contrib: v.Contrib,
				}
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
		if len(groups) > 0 {
			built.GroupBy[name] = groups
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
			if f.Contrib != nil {
				if built.FilterContrib[name] == nil {
					built.FilterContrib[name] = map[string]*ContribValue{}
				}
				built.FilterContrib[name][f.Name] = f.Contrib
			}
			if f.Aggregate != nil {
				if built.InputAggregates[name] == nil {
					built.InputAggregates[name] = map[string]*Aggregate{}
				}
				built.InputAggregates[name][f.Name] = f.Aggregate
			}
		}
		built.InputColumns[name] = cols
	}
	return built, nil
}

// SDL renders the current IR as GraphQL SDL, deterministically ordered.
func (b *Builder) SDL() string {
	var out strings.Builder
	if b.Options.IncrementalDelivery {
		out.WriteString(incrementalDirectives)
	}
	if b.Options.Live && b.Subscription != nil {
		out.WriteString(liveDirective)
	}
	for _, s := range b.Scalars {
		writeDesc(&out, b.ScalarDescriptions[s], "")
		fmt.Fprintf(&out, "scalar %s\n", s)
	}
	out.WriteString("\n")

	enumNames := sortedKeys(b.Enums)
	for _, n := range enumNames {
		e := b.Enums[n]
		writeDesc(&out, e.Description, "")
		fmt.Fprintf(&out, "enum %s {\n", e.Name)
		for _, v := range e.Values {
			writeDesc(&out, v.Description, "  ")
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
		if n == "Query" || n == "Mutation" || n == "Subscription" {
			continue
		}
		b.writeObject(&out, b.Objects[n])
	}
	b.writeObject(&out, b.Query)
	if m, ok := b.Objects["Mutation"]; ok {
		b.writeObject(&out, m)
	}
	if s, ok := b.Objects["Subscription"]; ok {
		b.writeObject(&out, s)
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
			if a.Description != "" {
				parts[i] = blockString(a.Description) + " " + parts[i]
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
	fmt.Fprintf(out, "%s%s\n", indent, blockString(desc))
}

// blockString renders desc as a GraphQL block string.
func blockString(desc string) string {
	return `"""` + strings.ReplaceAll(desc, `"""`, `\"""`) + `"""`
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
