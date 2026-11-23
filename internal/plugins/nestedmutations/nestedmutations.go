// Package nestedmutations is a built-in plugin exercising every hook
// surface. It adds postgraphile-plugin-nested-mutations style input fields
// to the create inputs and patches of tables, one per foreign key in either
// direction (SchemaHook): connectBy<Key>/connectByNodeId and create, plus,
// on patches, updateBy<Key>/updateByNodeId, deleteBy<Key>/deleteByNodeId
// and disconnectBy<Key>/disconnectByNodeId. Create and update mutations
// using them compile into a single multi-CTE SQL statement respecting FK
// direction (CompileHook), and the plugin forces a transaction for them even
// when transactions are globally disabled (RequestHook), unless
// plugins.nested-mutations.force_transactions is false.
package nestedmutations

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	coreinflect "github.com/suprbdev/pdbcore/inflect"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/omit"
	"github.com/suprbdev/pdbq/internal/schema"
)

const (
	defaultMaxDepth      = 3
	defaultMaxOperations = 50
)

// Plugin is the nested-mutations plugin. One instance must serve both the
// schema build and compilation, since compilation reads the bindings the
// SchemaHook records.
type Plugin struct {
	forceTx       bool
	maxDepth      int
	maxOperations int

	// specs: row input type (create input or patch) -> nested field -> spec.
	specs map[string]map[string]*nestedSpec
	// ops: nested input type -> operation field -> operation.
	ops map[string]map[string]*opSpec
	// inputForTable / patchForTable: "schema.table" -> create input / patch.
	inputForTable map[string]string
	patchForTable map[string]string
}

// nestedSpec binds a nested field of a row input to its foreign key.
type nestedSpec struct {
	Forward bool // true: the FK is on the row's table and points to Target
	FK      *introspect.ForeignKey
	Owner   *introspect.Table // table owning the FK (child)
	Target  *introspect.Table // referenced table for forward; child table for reverse
	Input   string            // nested input type holding the operations
}

type opKind int

const (
	opCreate opKind = iota
	opConnect
	opUpdate
	opDelete
	opDisconnect
)

// opSpec describes one operation field of a nested input type.
type opSpec struct {
	kind opKind
	// key lists the key columns of the target the operation input carries;
	// nil when it carries a nodeId.
	key    []string
	nodeID bool
	// entity marks the connect of a PERIOD foreign key by its equality
	// columns, which links to every version of the referenced entity.
	entity bool
	// input is the operation's input type.
	input string
}

// New builds the plugin from its `plugins.settings.nested-mutations` map.
func New(settings map[string]any) *Plugin {
	p := &Plugin{forceTx: true, maxDepth: defaultMaxDepth, maxOperations: defaultMaxOperations}
	p.reset()
	if v, ok := settings["force_transactions"].(bool); ok {
		p.forceTx = v
	}
	if v := intSetting(settings["max_depth"]); v > 0 {
		p.maxDepth = v
	}
	if v := intSetting(settings["max_operations"]); v > 0 {
		p.maxOperations = v
	}
	return p
}

func intSetting(v any) int {
	switch v := v.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func (p *Plugin) reset() {
	p.specs = map[string]map[string]*nestedSpec{}
	p.ops = map[string]map[string]*opSpec{}
	p.inputForTable = map[string]string{}
	p.patchForTable = map[string]string{}
}

func (p *Plugin) Name() string  { return "nested-mutations" }
func (p *Plugin) Priority() int { return 200 }

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	p.reset()
	for _, t := range b.Catalog.Tables {
		key := t.Schema + "." + t.Name
		if b.TypeForTable[key] == "" {
			continue
		}
		if name := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: t.Schema, Table: t.Name}); t.Insertable() && b.Inputs[name] != nil {
			p.inputForTable[key] = name
		}
		if name := b.Inflect(inflect.KindUpdateInput, inflect.Input{Schema: t.Schema, Table: t.Name}); t.Updatable() && b.Inputs[name] != nil {
			p.patchForTable[key] = name
		}
	}
	for _, t := range b.Catalog.Tables {
		key := t.Schema + "." + t.Name
		createIn := b.Inputs[p.inputForTable[key]]
		patch := b.Inputs[p.patchForTable[key]]
		if createIn == nil && patch == nil {
			continue
		}
		rowInputs := []struct {
			in     *schema.Input
			create bool
		}{{createIn, true}, {patch, false}}

		for _, fk := range t.ForeignKeys {
			parent := b.Catalog.Table(fk.RefSchema, fk.RefTable)
			if parent == nil || b.TypeForTable[parent.Schema+"."+parent.Name] == "" {
				continue
			}
			relName := b.ForwardRelationName(t, fk)
			for _, ri := range rowInputs {
				if ri.in == nil || ri.in.Field(relName) != nil {
					continue
				}
				nestedType := p.forwardInput(b, t, parent, fk, relName, ri.in, ri.create)
				if nestedType == "" {
					continue
				}
				p.addNested(ri.in, relName, nestedType, "Nested mutation of the referenced "+fk.RefTable+" row.",
					&nestedSpec{Forward: true, FK: fk, Owner: t, Target: parent, Input: nestedType})
				if ri.create {
					relax(b, ri.in, t, fk)
				}
			}
		}

		for _, child := range b.Catalog.Tables {
			if b.TypeForTable[child.Schema+"."+child.Name] == "" {
				continue
			}
			for _, fk := range child.ForeignKeys {
				if fk.RefSchema != t.Schema || fk.RefTable != t.Name || omit.ManyHidden(child, fk) {
					continue
				}
				relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
					Schema: child.Schema, Table: child.Name, Columns: fk.Columns,
					Constraint: fk.Name,
				})
				for _, ri := range rowInputs {
					if ri.in == nil || ri.in.Field(relName) != nil {
						continue
					}
					nestedType, creates := p.backwardInput(b, t, child, fk, relName, ri.create)
					if nestedType == "" {
						continue
					}
					p.addNested(ri.in, relName, nestedType, "Nested mutation of the referencing "+child.Name+" rows.",
						&nestedSpec{Forward: false, FK: fk, Owner: child, Target: child, Input: nestedType})
					if childIn := b.Inputs[p.inputForTable[child.Schema+"."+child.Name]]; creates && childIn != nil {
						relax(b, childIn, child, fk)
					}
				}
			}
		}
	}
	return nil
}

// addNested adds the nested field relName to a row input.
func (p *Plugin) addNested(in *schema.Input, relName, nestedType, desc string, spec *nestedSpec) {
	in.AddField(&schema.InputField{
		Name:        relName,
		Type:        nestedType,
		Description: desc,
		Nested:      map[string]any{"plugin": p.Name()},
	})
	if p.specs[in.Name] == nil {
		p.specs[in.Name] = map[string]*nestedSpec{}
	}
	p.specs[in.Name][relName] = spec
}

// relax makes the FK columns of t optional in its create input: a nested
// operation can supply them (the database still enforces NOT NULL).
func relax(b *schema.Builder, in *schema.Input, t *introspect.Table, fk *introspect.ForeignKey) {
	for _, pair := range equalityPairs(fk) {
		fieldName := b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: pair.Column})
		if f := in.Field(fieldName); f != nil {
			f.Type = strings.TrimSuffix(f.Type, "!")
		}
	}
}

// writesFK reports whether in has a field for every column fk copies, so a
// nested operation may set them (a column left out, e.g. by `@omit create`
// or `@omit update`, must not be written through a nested field either),
// and whether the referenced columns of parent are readable: copying a
// hidden referenced key into the readable foreign key column would reveal
// it.
func writesFK(in *schema.Input, parent *introspect.Table, fk *introspect.ForeignKey) bool {
	if in == nil || omit.ReferencesHidden(parent, fk) {
		return false
	}
	for _, pair := range equalityPairs(fk) {
		if !slices.ContainsFunc(in.Fields, func(f *schema.InputField) bool { return f.Column == pair.Column }) {
			return false
		}
	}
	return true
}

// nestedTypeName names the nested input type of a relation field:
// `<Type><Rel>NestedInput` on create inputs, `<Type><Rel>NestedPatchInput`
// on patches.
func nestedTypeName(b *schema.Builder, t *introspect.Table, relName string, create bool) string {
	suffix := "NestedPatchInput"
	if create {
		suffix = "NestedInput"
	}
	return b.TypeForTable[t.Schema+"."+t.Name] + coreinflect.UpperCamel(relName) + suffix
}

// forwardInput creates the operations on the row a forward relation of
// owner references, for the owner's create input or patch (rowIn). Create
// inputs get connect and create, which point the new row's FK at the
// parent; patches also get update and delete of the parent the row
// references and disconnect. Each operation takes a single input.
func (p *Plugin) forwardInput(b *schema.Builder, owner, parent *introspect.Table, fk *introspect.ForeignKey, relName string, rowIn *schema.Input, create bool) string {
	name := nestedTypeName(b, owner, relName, create)
	if _, ok := b.Inputs[name]; ok {
		return ""
	}
	desc := "Nested mutation of the " + relName + " relation: at most one of create or connect."
	if !create {
		desc = "Nested mutation of the " + relName + " relation: at most one of create, connect or disconnect, plus updates and deletes of the referenced row."
	}
	in := &schema.Input{Name: name, Description: desc}
	ops := map[string]*opSpec{}
	add := func(field, typ string, op *opSpec) {
		in.AddField(&schema.InputField{Name: field, Type: typ})
		ops[field] = op
	}
	keys := targetKeys(parent)
	node := isNode(b, parent)
	pkey := parent.Schema + "." + parent.Name
	setsFK := writesFK(rowIn, parent, fk)
	if setsFK {
		for _, k := range keys {
			add("connectBy"+coreinflect.ByColumns(k), p.keyInput(b, parent, "Connect", k), &opSpec{kind: opConnect, key: k})
		}
		if eq := refKey(fk); fk.Period && !slices.ContainsFunc(keys, func(k []string) bool { return slices.Equal(k, eq) }) {
			add("connectBy"+coreinflect.ByColumns(eq), p.keyInput(b, parent, "Connect", eq), &opSpec{kind: opConnect, key: eq, entity: true})
		}
		if node {
			add("connectByNodeId", p.nodeIDInput(b, parent, "Connect"), &opSpec{kind: opConnect, nodeID: true})
		}
		if create := p.inputForTable[pkey]; create != "" {
			add("create", create, &opSpec{kind: opCreate})
		}
	}
	if !create {
		if p.patchForTable[pkey] != "" {
			for _, k := range keys {
				add("updateBy"+coreinflect.ByColumns(k), p.updateInput(b, parent, k), &opSpec{kind: opUpdate, key: k})
			}
			if node {
				add("updateByNodeId", p.updateInput(b, parent, nil), &opSpec{kind: opUpdate, nodeID: true})
			}
		}
		if parent.Deletable() {
			for _, k := range keys {
				add("deleteBy"+coreinflect.ByColumns(k), p.keyInput(b, parent, "Delete", k), &opSpec{kind: opDelete, key: k})
			}
			if node {
				add("deleteByNodeId", p.nodeIDInput(b, parent, "Delete"), &opSpec{kind: opDelete, nodeID: true})
			}
		}
		if setsFK && nullableFK(owner, fk) {
			for _, k := range keys {
				add("disconnectBy"+coreinflect.ByColumns(k), p.keyInput(b, parent, "Disconnect", k), &opSpec{kind: opDisconnect, key: k})
			}
			if node {
				add("disconnectByNodeId", p.nodeIDInput(b, parent, "Disconnect"), &opSpec{kind: opDisconnect, nodeID: true})
			}
		}
	}
	return p.register(b, in, ops)
}

// backwardInput creates the operations on the rows of child referencing a
// parent row through fk, for the parent's create input or patch. Create
// inputs get connect and create; patches also get update, delete and
// disconnect of the rows already referencing the parent. Each operation
// takes a list. It reports whether the type offers create.
func (p *Plugin) backwardInput(b *schema.Builder, parent, child *introspect.Table, fk *introspect.ForeignKey, relName string, create bool) (string, bool) {
	name := nestedTypeName(b, parent, relName, create)
	if _, ok := b.Inputs[name]; ok {
		return "", false
	}
	in := &schema.Input{Name: name, Description: "Nested mutation of the " + relName + " relation."}
	ops := map[string]*opSpec{}
	add := func(field, typ string, op *opSpec) {
		in.AddField(&schema.InputField{Name: field, Type: "[" + typ + "!]"})
		ops[field] = op
	}
	keys := targetKeys(child)
	node := isNode(b, child)
	ckey := child.Schema + "." + child.Name
	childPatch := b.Inputs[p.patchForTable[ckey]]
	childCreate := b.Inputs[p.inputForTable[ckey]]
	if writesFK(childPatch, parent, fk) {
		for _, k := range keys {
			add("connectBy"+coreinflect.ByColumns(k), p.keyInput(b, child, "Connect", k), &opSpec{kind: opConnect, key: k})
		}
		if node {
			add("connectByNodeId", p.nodeIDInput(b, child, "Connect"), &opSpec{kind: opConnect, nodeID: true})
		}
	}
	creates := writesFK(childCreate, parent, fk)
	if creates {
		add("create", childCreate.Name, &opSpec{kind: opCreate})
	}
	if !create {
		if childPatch != nil {
			for _, k := range keys {
				add("updateBy"+coreinflect.ByColumns(k), p.updateInput(b, child, k), &opSpec{kind: opUpdate, key: k})
			}
			if node {
				add("updateByNodeId", p.updateInput(b, child, nil), &opSpec{kind: opUpdate, nodeID: true})
			}
		}
		if child.Deletable() {
			for _, k := range keys {
				add("deleteBy"+coreinflect.ByColumns(k), p.keyInput(b, child, "Delete", k), &opSpec{kind: opDelete, key: k})
			}
			if node {
				add("deleteByNodeId", p.nodeIDInput(b, child, "Delete"), &opSpec{kind: opDelete, nodeID: true})
			}
		}
		if writesFK(childPatch, parent, fk) && nullableFK(child, fk) {
			for _, k := range keys {
				add("disconnectBy"+coreinflect.ByColumns(k), p.keyInput(b, child, "Disconnect", k), &opSpec{kind: opDisconnect, key: k})
			}
			if node {
				add("disconnectByNodeId", p.nodeIDInput(b, child, "Disconnect"), &opSpec{kind: opDisconnect, nodeID: true})
			}
		}
	}
	return p.register(b, in, ops), creates
}

// register adds a nested input type with its operations, or drops it when
// it has none.
func (p *Plugin) register(b *schema.Builder, in *schema.Input, ops map[string]*opSpec) string {
	if len(in.Fields) == 0 {
		return ""
	}
	for field, op := range ops {
		if f := in.Field(field); f != nil {
			op.input = strings.Trim(f.Type, "[]!")
		}
	}
	b.Inputs[in.Name] = in
	p.ops[in.Name] = ops
	return in.Name
}

// keyInput creates `<Type><Verb>By<Key>Input`, one required field per key
// column.
func (p *Plugin) keyInput(b *schema.Builder, t *introspect.Table, verb string, key []string) string {
	name := b.TypeForTable[t.Schema+"."+t.Name] + verb + "By" + coreinflect.ByColumns(key) + "Input"
	if _, ok := b.Inputs[name]; !ok {
		in := &schema.Input{Name: name}
		addKeyFields(b, in, t, key)
		b.Inputs[name] = in
	}
	return name
}

// nodeIDInput creates `<Type><Verb>ByNodeIdInput { nodeId: ID! }`.
func (p *Plugin) nodeIDInput(b *schema.Builder, t *introspect.Table, verb string) string {
	name := b.TypeForTable[t.Schema+"."+t.Name] + verb + "ByNodeIdInput"
	if _, ok := b.Inputs[name]; !ok {
		b.Inputs[name] = &schema.Input{Name: name, Fields: []*schema.InputField{
			{Name: "nodeId", Type: "ID!", Description: "The globally unique ID of the row."},
		}}
	}
	return name
}

// updateInput creates `<Type>UpdateBy<Key>Input` (or
// `<Type>UpdateByNodeIdInput` for a nil key): the row's key plus
// `<type>Patch: <Type>Patch!`.
func (p *Plugin) updateInput(b *schema.Builder, t *introspect.Table, key []string) string {
	typeName := b.TypeForTable[t.Schema+"."+t.Name]
	suffix := "NodeId"
	if key != nil {
		suffix = coreinflect.ByColumns(key)
	}
	name := typeName + "UpdateBy" + suffix + "Input"
	if _, ok := b.Inputs[name]; !ok {
		in := &schema.Input{Name: name}
		if key == nil {
			in.AddField(&schema.InputField{Name: "nodeId", Type: "ID!", Description: "The globally unique ID of the row."})
		} else {
			addKeyFields(b, in, t, key)
		}
		in.AddField(&schema.InputField{Name: patchField(typeName), Type: p.patchForTable[t.Schema+"."+t.Name] + "!", Description: "The values to set on the row."})
		b.Inputs[name] = in
	}
	return name
}

func addKeyFields(b *schema.Builder, in *schema.Input, t *introspect.Table, key []string) {
	for _, col := range key {
		in.AddField(&schema.InputField{
			Name:   b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: col}),
			Type:   b.GraphQLInputTypeFor(t.Column(col)) + "!",
			Column: col,
		})
	}
}

// patchField names the patch of an update operation, `<type>Patch`.
func patchField(typeName string) string {
	return coreinflect.LowerCamel(typeName) + "Patch"
}

// targetKeys returns the keys a nested operation can identify a row of t
// by: its primary key, then its unique constraints (the update/delete
// mutation targets), skipping keys naming unknown columns or duplicating an
// earlier key's columns.
func targetKeys(t *introspect.Table) [][]string {
	var out [][]string
	for _, con := range append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...) {
		if con == nil || len(con.Columns) == 0 {
			continue
		}
		ok := true
		for _, cn := range con.Columns {
			if t.Column(cn) == nil {
				ok = false
				break
			}
		}
		if ok && !slices.ContainsFunc(out, func(k []string) bool { return slices.Equal(k, con.Columns) }) {
			out = append(out, con.Columns)
		}
	}
	return out
}

// isNode reports whether t's type implements Node (has a nodeId).
func isNode(b *schema.Builder, t *introspect.Table) bool {
	if t.PrimaryKey == nil {
		return false
	}
	obj := b.Objects[b.TypeForTable[t.Schema+"."+t.Name]]
	return obj != nil && slices.Contains(obj.Interfaces, "Node")
}

// nullableFK reports whether every column a disconnect clears is nullable.
func nullableFK(t *introspect.Table, fk *introspect.ForeignKey) bool {
	for _, pair := range equalityPairs(fk) {
		if c := t.Column(pair.Column); c == nil || c.NotNull {
			return false
		}
	}
	return true
}

func refKey(fk *introspect.ForeignKey) []string {
	var cols []string
	for _, pair := range equalityPairs(fk) {
		cols = append(cols, pair.RefColumn)
	}
	return cols
}

// ---- CompileHook ----

func (p *Plugin) WrapCompile(next compile.Func) compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		meta := metaFor(req)
		if meta == nil {
			return next(ctx, req)
		}
		argName := ""
		switch meta.Kind {
		case schema.KindCreate, schema.KindUpsert, schema.KindCreateMany:
			argName = "input"
		case schema.KindUpdate, schema.KindUpdateMany:
			argName = "patch"
		default:
			return next(ctx, req)
		}
		arg := req.Field.Arguments.ForName(argName)
		if arg == nil {
			return next(ctx, req)
		}
		val, err := arg.Value.Value(req.Vars)
		if err != nil {
			return nil, err
		}
		argType := argTypeName(req.Field, argName)
		if !p.usesNested(argType, val) {
			return next(ctx, req)
		}
		if meta.Kind != schema.KindCreate && meta.Kind != schema.KindUpdate {
			return nil, compile.UserErrorf("nested-mutations: %s does not take nested mutation fields", req.Field.Name)
		}
		input, _ := val.(map[string]any)

		g := &cteGen{p: p, req: req, ps: &compile.ParamSet{}, writeIdx: map[string]int{}, dml: map[string]*dmlCTE{}}
		if meta.Kind == schema.KindCreate {
			err = g.insertRow(meta.Table, argType, input, "__mut", 1, nil, "")
		} else {
			var key rowKey
			if key, err = g.argKey(meta); err == nil {
				err = g.updateRow(meta.Table, argType, input, "__mut", 1, key, nil, "")
			}
			g.guard("__mut", req.Field.Name)
		}
		if err != nil {
			return nil, err
		}
		overrides := g.overrides()
		payload, err := compile.New(req.Built).MutationPayload(req, req.Field, meta, g.ps, overrides)
		if err != nil {
			return nil, err
		}
		return compile.MutationStatement(g.finish(meta.Table, payload), payload.SQL, g.ps, g.guards), nil
	}
}

func metaFor(req *compile.Request) *schema.FieldMeta {
	parent := "Mutation"
	if req.Field.ObjectDefinition != nil {
		parent = req.Field.ObjectDefinition.Name
	}
	if m, ok := req.Built.Meta[parent]; ok {
		return m[req.Field.Name]
	}
	return nil
}

func argTypeName(f *ast.Field, arg string) string {
	if f.Definition == nil {
		return ""
	}
	def := f.Definition.Arguments.ForName(arg)
	if def == nil {
		return ""
	}
	return def.Type.Name()
}

// usesNested reports whether a row input value (or any row of a list of
// them) sets a nested field.
func (p *Plugin) usesNested(inputType string, v any) bool {
	switch v := v.(type) {
	case map[string]any:
		specs := p.specs[inputType]
		for k, fv := range v {
			if _, ok := specs[k]; ok && fv != nil {
				return true
			}
		}
	case []any:
		for _, row := range v {
			if p.usesNested(inputType, row) {
				return true
			}
		}
	}
	return false
}

// cteGen accumulates the WITH chain for one nested mutation.
//
// The RETURNING list of every data-modifying CTE starts as a placeholder
// (retToken) and is resolved by finish once the payload is compiled, to the
// columns the payload, the visibility CTEs and the other CTEs read from it:
// RETURNING needs the SELECT privilege per returned column, so RETURNING *
// would fail for roles holding column-level grants.
type cteGen struct {
	p      *Plugin
	req    *compile.Request
	ps     *compile.ParamSet
	ctes   []string
	guards []string
	n      int
	ops    int
	// writes lists, in emission order, every table the chain writes to;
	// writeIdx indexes it by "schema.table".
	writes   []tableWrites
	writeIdx map[string]int
	// dml holds the data-modifying (and locking) CTEs by name.
	dml map[string]*dmlCTE
}

// dmlCTE is a CTE whose RETURNING (or select) list finish resolves.
type dmlCTE struct {
	table *introspect.Table
	// qual qualifies the returned columns ("" for an INSERT).
	qual string
	need map[string]bool
}

// tableWrites records how the chain changes one table: the CTEs whose
// returned rows are in it once the statement completes (inserted and
// updated rows), and how to exclude from the bare table the rows the chain
// replaced or deleted.
type tableWrites struct {
	table *introspect.Table
	vis   string
	adds  []string
	excl  []exclusion
}

// exclusion removes from a visibility CTE's table half the rows one UPDATE
// or DELETE CTE replaced: by a NOT NULL key the CTE leaves alone, matched
// against its returned rows (stable), or by its key lookup (cond).
type exclusion struct {
	cte    string
	stable []string
	cond   string
}

func retToken(cte string) string { return "%%RET:" + cte + "%%" }

// returning registers cte as a data-modifying CTE over t and returns the
// placeholder of its returned columns.
func (g *cteGen) returning(cte string, t *introspect.Table, qual string) string {
	g.dml[cte] = &dmlCTE{table: t, qual: qual, need: map[string]bool{}}
	return retToken(cte)
}

// need records that cols of cte's table are read from cte; CTEs that are
// not data-modifying project their columns explicitly and are ignored.
func (g *cteGen) need(cte string, cols ...string) {
	if d := g.dml[cte]; d != nil {
		for _, c := range cols {
			d.need[c] = true
		}
	}
}

func (g *cteGen) write(t *introspect.Table) *tableWrites {
	key := t.Schema + "." + t.Name
	i, ok := g.writeIdx[key]
	if !ok {
		i = len(g.writes)
		g.writeIdx[key] = i
		g.writes = append(g.writes, tableWrites{table: t})
	}
	return &g.writes[i]
}

func (g *cteGen) recordInsert(t *introspect.Table, cte string) {
	w := g.write(t)
	w.adds = append(w.adds, cte)
}

// recordChange records an UPDATE (keep) or DELETE CTE over t that targeted
// the row key identifies.
func (g *cteGen) recordChange(t *introspect.Table, cte string, key rowKey, patched map[string]bool, keep bool) {
	w := g.write(t)
	ex := exclusion{cte: cte, stable: stableKey(t, patched)}
	if ex.stable == nil {
		ex.cond = "(" + key.cond(g, t, compile.TableRefSQL(t.Schema, t.Name)) + ") IS NOT TRUE"
	}
	w.excl = append(w.excl, ex)
	if keep {
		w.adds = append(w.adds, cte)
	}
}

// stableKey returns the columns of the first row key of t (primary key, then
// unique constraints) that are all NOT NULL and untouched by patched, or nil.
func stableKey(t *introspect.Table, patched map[string]bool) []string {
	for _, con := range append([]*introspect.Constraint{t.PrimaryKey}, t.Uniques...) {
		if con == nil || len(con.Columns) == 0 {
			continue
		}
		ok := true
		for _, cn := range con.Columns {
			c := t.Column(cn)
			if c == nil || patched[cn] || (!c.NotNull && con != t.PrimaryKey) {
				ok = false
				break
			}
		}
		if ok {
			return con.Columns
		}
	}
	return nil
}

// overrides maps every table the chain writes to its visibility CTE.
func (g *cteGen) overrides() map[string]string {
	out := make(map[string]string, len(g.writes))
	for i := range g.writes {
		w := &g.writes[i]
		w.vis = g.name("vis")
		out[w.table.Schema+"."+w.table.Name] = w.vis
	}
	return out
}

// finish appends the visibility CTEs the payload reads and resolves the
// returned columns of the data-modifying CTEs, returning the CTE chain.
//
// Rows written by a data-modifying CTE are invisible to every other table
// scan of the same statement (they all share its snapshot), so a payload
// relation reading the table would miss the rows created or changed here
// and still see the deleted ones. Each visibility CTE is the table itself
// (still subject to the role's RLS policies) minus the pre-statement
// versions of the rows the chain updated or deleted, UNION ALL the returned
// rows of its inserts and updates: the table as it stands once the
// statement completes. Returned rows already passed the table's SELECT
// policies, since INSERT and UPDATE ... RETURNING raise an error for a new
// row they reject. It projects only the columns the payload reads through
// it; a table the payload does not read gets none, since every column a
// statement names needs the SELECT privilege even in an unread CTE.
//
// NOT MATERIALIZED inlines the CTE at every reference, so the correlated
// relation predicates reach the table's indexes instead of a materialized
// copy of the whole table.
func (g *cteGen) finish(root *introspect.Table, payload *compile.MutationPayload) string {
	g.need("__mut", compile.ReferencedColumns(root, payload.SQL, payload.Aliases["__mut"])...)
	for _, w := range g.writes {
		aliases := payload.Aliases[w.vis]
		if len(aliases) == 0 {
			continue
		}
		cols := compile.ReferencedColumns(w.table, payload.SQL, aliases)
		proj := columnList(w.table, cols, "")
		ref := compile.TableRefSQL(w.table.Schema, w.table.Name)
		table := "SELECT " + proj + " FROM " + ref
		var excl []string
		for _, ex := range w.excl {
			if ex.stable == nil {
				excl = append(excl, ex.cond)
				continue
			}
			g.need(ex.cte, ex.stable...)
			conds := make([]string, len(ex.stable))
			for i, cn := range ex.stable {
				q := compile.QuoteIdent(cn)
				conds[i] = fmt.Sprintf("__m.%s %s %s.%s", q, compile.EqualOperator(g.req.Built, w.table.Column(cn)), ref, q)
			}
			excl = append(excl, fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %s AS __m WHERE %s)", ex.cte, strings.Join(conds, " AND ")))
		}
		if len(excl) > 0 {
			table += " WHERE " + strings.Join(excl, " AND ")
		}
		parts := append(make([]string, 0, len(w.adds)+1), table)
		for _, cte := range w.adds {
			g.need(cte, cols...)
			parts = append(parts, "SELECT "+proj+" FROM "+cte)
		}
		g.ctes = append(g.ctes, fmt.Sprintf("%s AS NOT MATERIALIZED (\n  %s\n)", w.vis, strings.Join(parts, "\n  UNION ALL ")))
	}
	pairs := make([]string, 0, 2*len(g.dml))
	for cte, d := range g.dml {
		cols := make([]string, 0, len(d.need))
		for c := range d.need {
			cols = append(cols, c)
		}
		pairs = append(pairs, retToken(cte), columnList(d.table, cols, d.qual))
	}
	return strings.NewReplacer(pairs...).Replace(strings.Join(g.ctes, ",\n"))
}

// columnList renders the columns of t named in cols, in table order and
// qualified by qual when set, or the literal 1 when there are none.
func columnList(t *introspect.Table, cols []string, qual string) string {
	var out []string
	for _, c := range t.Columns {
		if slices.Contains(cols, c.Name) {
			q := compile.QuoteIdent(c.Name)
			if qual != "" {
				q = qual + "." + q
			}
			out = append(out, q)
		}
	}
	if len(out) == 0 {
		return "1"
	}
	return strings.Join(out, ", ")
}

func (g *cteGen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("__%s_%d", prefix, g.n)
}

// guard requires cte to yield a row. The ELSE branch casts a message built
// from count(*) to boolean, which fails with SQLSTATE 22P02 carrying the
// message; the aggregate keeps the planner from folding the cast into a
// plan-time error.
func (g *cteGen) guard(cte, path string) {
	g.guards = append(g.guards, fmt.Sprintf(
		"(SELECT CASE WHEN count(*) > 0 THEN true ELSE concat(%s, count(*), ' rows')::boolean END FROM %s)",
		compile.QuoteLiteral("nested-mutations: "+path+" matched "), cte))
}

// countOp charges one nested operation against max_operations.
func (g *cteGen) countOp() error {
	g.ops++
	if g.ops > g.p.maxOperations {
		return compile.UserErrorf("nested-mutations: more than %d nested operations", g.p.maxOperations)
	}
	return nil
}

// addParam renders v for the named column (enum labels, jsonb encoding,
// PostGIS and contrib constructors; see compile.ParamSet.Value).
func (g *cteGen) addParam(t *introspect.Table, colName string, v any) (string, error) {
	return g.ps.Value(g.req.Built, t.Column(colName), v)
}

// rowKey is a row lookup: key columns with their rendered values.
type rowKey struct {
	cols   []string
	params []string
}

// cond renders the lookup over ref (a table reference or alias of t).
func (k rowKey) cond(g *cteGen, t *introspect.Table, ref string) string {
	conds := make([]string, len(k.cols))
	for i, cn := range k.cols {
		conds[i] = fmt.Sprintf("%s.%s %s %s", ref, compile.QuoteIdent(cn), compile.EqualOperator(g.req.Built, t.Column(cn)), k.params[i])
	}
	return strings.Join(conds, " AND ")
}

// argKey reads the key arguments of an update mutation, which precede patch
// in KeyColumns order.
func (g *cteGen) argKey(meta *schema.FieldMeta) (rowKey, error) {
	f := g.req.Field
	if f.Definition == nil || len(f.Definition.Arguments) < len(meta.KeyColumns) {
		return rowKey{}, fmt.Errorf("nested-mutations: %s: missing key arguments", f.Name)
	}
	var k rowKey
	for i, cn := range meta.KeyColumns {
		arg := f.Arguments.ForName(f.Definition.Arguments[i].Name)
		if arg == nil {
			return rowKey{}, compile.UserErrorf("nested-mutations: %s: missing argument %s", f.Name, f.Definition.Arguments[i].Name)
		}
		v, err := arg.Value.Value(g.req.Vars)
		if err != nil {
			return rowKey{}, err
		}
		param, err := g.addParam(meta.Table, cn, v)
		if err != nil {
			return rowKey{}, err
		}
		k.cols = append(k.cols, cn)
		k.params = append(k.params, param)
	}
	return k, nil
}

// opKey reads the row key an operation input carries: its key fields, or
// its nodeId, which must identify a row of t.
func (g *cteGen) opKey(t *introspect.Table, op *opSpec, in map[string]any, path string) (rowKey, error) {
	var k rowKey
	if op.nodeID {
		s, _ := in["nodeId"].(string)
		typeName, vals, err := compile.DecodeNodeID(s)
		if err != nil {
			return k, err
		}
		if g.req.Built.TableForType[typeName] != t || t.PrimaryKey == nil {
			return k, compile.UserErrorf("nested-mutations: %s: nodeId does not identify a %s", path, g.req.Built.TypeForTable[t.Schema+"."+t.Name])
		}
		if len(vals) != len(t.PrimaryKey.Columns) {
			return k, compile.UserErrorf("nested-mutations: %s: nodeId has %d key values, want %d", path, len(vals), len(t.PrimaryKey.Columns))
		}
		for i, cn := range t.PrimaryKey.Columns {
			k.cols = append(k.cols, cn)
			k.params = append(k.params, g.ps.KeyValue(g.req.Built, t.Column(cn), vals[i]))
		}
		return k, nil
	}
	byCol := map[string]any{}
	for field, col := range g.req.Built.InputColumns[op.input] {
		if v, ok := in[field]; ok {
			byCol[col] = v
		}
	}
	for _, cn := range op.key {
		v, ok := byCol[cn]
		if !ok || v == nil {
			return k, compile.UserErrorf("nested-mutations: %s: missing key value", path)
		}
		param, err := g.addParam(t, cn, v)
		if err != nil {
			return k, err
		}
		k.cols = append(k.cols, cn)
		k.params = append(k.params, param)
	}
	return k, nil
}

// assignments collects the SET or INSERT column list of one row.
type assignments struct {
	cols    []string
	vals    []string
	from    []string
	patched map[string]bool
}

func (a *assignments) set(col, val, field, path string) error {
	if a.patched[col] {
		return compile.UserErrorf("nested-mutations: %s sets a column already set by the input", strings.TrimSuffix(path+field, "."))
	}
	if a.patched == nil {
		a.patched = map[string]bool{}
	}
	a.patched[col] = true
	a.cols = append(a.cols, compile.QuoteIdent(col))
	a.vals = append(a.vals, val)
	return nil
}

func (a *assignments) source(cte string) {
	if !slices.Contains(a.from, cte) {
		a.from = append(a.from, cte)
	}
}

// parentFill wires the FK columns of a child row to the row inserted by its
// parent's CTE.
type parentFill struct {
	fk  *introspect.ForeignKey
	cte string
}

// childWork is a backward nested field, run once the row's CTE exists.
type childWork struct {
	field string
	spec  *nestedSpec
	input map[string]any
}

// rowFields applies the plain columns and forward nested fields of a row
// input to a, emitting the CTEs of the forward operations (which come
// before the row's own DML), and returns the backward nested fields. owner
// identifies the row for an update and is nil for an insert.
func (g *cteGen) rowFields(t *introspect.Table, inputType string, input map[string]any, a *assignments, depth int, owner *rowKey, path string) ([]childWork, error) {
	specs := g.p.specs[inputType]
	binding := g.req.Built.InputColumns[inputType]
	for _, fname := range sortedKeys(input) {
		col, ok := binding[fname]
		if !ok || (input[fname] == nil && owner == nil) {
			continue
		}
		val, err := g.addParam(t, col, input[fname])
		if err != nil {
			return nil, err
		}
		if err := a.set(col, val, fname, path); err != nil {
			return nil, err
		}
	}
	var children []childWork
	for _, fname := range sortedKeys(input) {
		spec, ok := specs[fname]
		if !ok || input[fname] == nil {
			continue
		}
		nested, _ := input[fname].(map[string]any)
		if !spec.Forward {
			children = append(children, childWork{field: fname, spec: spec, input: nested})
			continue
		}
		if err := g.forward(t, spec, nested, a, depth, owner, path+fname); err != nil {
			return nil, err
		}
	}
	return children, nil
}

// insertRow emits the CTEs inserting one row into t from input: forward
// operations first, then the INSERT named cteName, then the backward
// operations referencing it. A non-nil fill copies the FK columns of a
// child row from its parent's CTE.
func (g *cteGen) insertRow(t *introspect.Table, inputType string, input map[string]any, cteName string, depth int, fill *parentFill, path string) error {
	if depth > g.p.maxDepth {
		return compile.UserErrorf("nested-mutations: nesting depth exceeds limit %d", g.p.maxDepth)
	}
	var a assignments
	children, err := g.rowFields(t, inputType, input, &a, depth, nil, path)
	if err != nil {
		return err
	}
	if fill != nil {
		for _, pair := range equalityPairs(fill.fk) {
			if err := a.set(pair.Column, fill.cte+"."+compile.QuoteIdent(pair.RefColumn), "", path); err != nil {
				return err
			}
			g.need(fill.cte, pair.RefColumn)
		}
		a.source(fill.cte)
	}
	tableSQL := compile.TableRefSQL(t.Schema, t.Name)
	ret := g.returning(cteName, t, "")
	var insert string
	switch {
	case len(a.cols) == 0:
		insert = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING %s", tableSQL, ret)
	case len(a.from) == 0:
		insert = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s",
			tableSQL, strings.Join(a.cols, ", "), strings.Join(a.vals, ", "), ret)
	default:
		insert = fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s RETURNING %s",
			tableSQL, strings.Join(a.cols, ", "), strings.Join(a.vals, ", "), strings.Join(a.from, ", "), ret)
	}
	g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  %s\n)", cteName, insert))
	g.recordInsert(t, cteName)
	return g.children(children, cteName, depth, path)
}

// scope narrows an UPDATE or DELETE of a related row: cond renders the
// restriction over a reference to the row's table, and from names a CTE
// the condition reads ("" for none).
type scope struct {
	from string
	cond func(ref string) string
}

// childScope limits a statement to the rows of child referencing the row
// in parentCTE through fk.
func (g *cteGen) childScope(fk *introspect.ForeignKey, child *introspect.Table, parentCTE string) *scope {
	for _, pair := range fk.JoinPairs() {
		g.need(parentCTE, pair.RefColumn)
	}
	return &scope{from: parentCTE, cond: func(ref string) string { return g.matchCond(fk, child, ref, parentCTE) }}
}

// parentScope limits a statement to the row of fk's referenced table that
// the owner row identified by key currently references.
func (g *cteGen) parentScope(fk *introspect.ForeignKey, owner *introspect.Table, key rowKey) *scope {
	return &scope{cond: func(ref string) string {
		return fmt.Sprintf("EXISTS (SELECT 1 FROM %s AS __o WHERE %s AND %s)",
			compile.TableRefSQL(owner.Schema, owner.Name), key.cond(g, owner, "__o"), g.matchCond(fk, owner, "__o", ref))
	}}
}

// updateRow emits the CTEs updating the row of t that key identifies from
// patch: forward operations first, then the UPDATE named cteName, then the
// backward operations referencing it. A patch setting no column locks the
// row with SELECT ... FOR UPDATE instead, which still needs the UPDATE
// privilege and passes the UPDATE policies. A non-nil sc narrows the row.
func (g *cteGen) updateRow(t *introspect.Table, patchType string, patch map[string]any, cteName string, depth int, key rowKey, sc *scope, path string) error {
	if depth > g.p.maxDepth {
		return compile.UserErrorf("nested-mutations: nesting depth exceeds limit %d", g.p.maxDepth)
	}
	var a assignments
	children, err := g.rowFields(t, patchType, patch, &a, depth, &key, path)
	if err != nil {
		return err
	}
	for _, cw := range children {
		for _, pair := range cw.spec.FK.JoinPairs() {
			if a.patched[pair.RefColumn] {
				return compile.UserErrorf("nested-mutations: %s%s: the patch changes a column the relation references", path, cw.field)
			}
		}
	}
	ref := compile.TableRefSQL(t.Schema, t.Name)
	if len(a.cols) == 0 {
		alias := "__s"
		conds := []string{key.cond(g, t, alias)}
		from := []string{ref + " AS " + alias}
		if sc != nil {
			conds = append(conds, sc.cond(alias))
			if sc.from != "" {
				from = append(from, sc.from)
			}
		}
		g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  SELECT %s FROM %s WHERE %s FOR UPDATE OF %s\n)",
			cteName, g.returning(cteName, t, alias), strings.Join(from, ", "), strings.Join(conds, " AND "), alias))
		return g.children(children, cteName, depth, path)
	}
	conds := []string{key.cond(g, t, ref)}
	if sc != nil {
		conds = append(conds, sc.cond(ref))
		if sc.from != "" {
			a.source(sc.from)
		}
	}
	from := ""
	if len(a.from) > 0 {
		from = " FROM " + strings.Join(a.from, ", ")
	}
	sets := make([]string, len(a.cols))
	for i := range a.cols {
		sets[i] = a.cols[i] + " = " + a.vals[i]
	}
	g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  UPDATE %s SET %s%s WHERE %s RETURNING %s\n)",
		cteName, ref, strings.Join(sets, ", "), from, strings.Join(conds, " AND "), g.returning(cteName, t, ref)))
	g.recordChange(t, cteName, key, a.patched, true)
	return g.children(children, cteName, depth, path)
}

// matchCond renders the join of fk between the rows of t at ref (the
// referencing side) and the referenced row at parent (a CTE or table
// reference).
func (g *cteGen) matchCond(fk *introspect.ForeignKey, t *introspect.Table, ref, parent string) string {
	var conds []string
	for _, pair := range fk.JoinPairs() {
		op := "&&"
		if pair.Op == introspect.JoinEqual {
			op = compile.EqualOperator(g.req.Built, t.Column(pair.Column))
		}
		conds = append(conds, fmt.Sprintf("%s.%s %s %s.%s", ref, compile.QuoteIdent(pair.Column), op, parent, compile.QuoteIdent(pair.RefColumn)))
	}
	return strings.Join(conds, " AND ")
}

// forward emits the operations of a forward nested field and wires the
// owner row's FK columns in a: create and connect point the row at the
// parent they produce, disconnect clears the FK when it references the
// given parent, update and delete act on the parent the row references.
// owner identifies the owner row of an update and is nil for an insert.
func (g *cteGen) forward(t *introspect.Table, spec *nestedSpec, nested map[string]any, a *assignments, depth int, owner *rowKey, path string) error {
	ops := g.p.ops[spec.Input]
	parent := spec.Target
	pkey := parent.Schema + "." + parent.Name
	pref := compile.TableRefSQL(parent.Schema, parent.Name)
	var link, clear string
	setters, total := 0, 0
	for _, fname := range sortedKeys(nested) {
		op := ops[fname]
		obj, _ := nested[fname].(map[string]any)
		if op == nil || obj == nil {
			continue
		}
		total++
		if err := g.countOp(); err != nil {
			return err
		}
		opPath := path + "." + fname
		if owner == nil && (op.kind == opUpdate || op.kind == opDelete || op.kind == opDisconnect) {
			return compile.UserErrorf("nested-mutations: %s applies to update mutations only", opPath)
		}
		switch op.kind {
		case opCreate:
			setters++
			link = g.name("p")
			if err := g.insertRow(parent, g.p.inputForTable[pkey], obj, link, depth+1, nil, opPath+"."); err != nil {
				return err
			}
		case opConnect:
			setters++
			key, err := g.opKey(parent, op, obj, opPath)
			if err != nil {
				return err
			}
			link = g.name("p")
			var proj []string
			for _, pair := range equalityPairs(spec.FK) {
				proj = append(proj, compile.QuoteIdent(pair.RefColumn))
			}
			distinct := ""
			if op.entity {
				distinct = "DISTINCT "
			}
			g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  SELECT %s%s FROM %s WHERE %s\n)",
				link, distinct, strings.Join(proj, ", "), pref, key.cond(g, parent, pref)))
			g.guard(link, opPath)
		case opUpdate:
			key, err := g.opKey(parent, op, obj, opPath)
			if err != nil {
				return err
			}
			cte := g.name("p")
			patch, _ := obj[patchField(g.req.Built.TypeForTable[pkey])].(map[string]any)
			if err := g.updateRow(parent, g.p.patchForTable[pkey], patch, cte, depth+1, key, g.parentScope(spec.FK, t, *owner), opPath+"."); err != nil {
				return err
			}
			g.guard(cte, opPath)
		case opDelete:
			key, err := g.opKey(parent, op, obj, opPath)
			if err != nil {
				return err
			}
			cte := g.name("d")
			g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  DELETE FROM %s WHERE %s AND %s RETURNING %s\n)",
				cte, pref, key.cond(g, parent, pref), g.parentScope(spec.FK, t, *owner).cond(pref), g.returning(cte, parent, pref)))
			g.recordChange(parent, cte, key, nil, false)
			g.guard(cte, opPath)
		case opDisconnect:
			setters++
			key, err := g.opKey(parent, op, obj, opPath)
			if err != nil {
				return err
			}
			clear = g.references(t, spec.FK, parent, key)
		}
	}
	if total == 0 {
		return compile.UserErrorf("nested-mutations: %s: specify an operation", path)
	}
	if setters > 1 {
		return compile.UserErrorf("nested-mutations: %s: specify at most one of create, connect or disconnect", path)
	}
	ownerRef := compile.TableRefSQL(t.Schema, t.Name)
	for _, pair := range equalityPairs(spec.FK) {
		col := compile.QuoteIdent(pair.Column)
		switch {
		case link != "":
			if err := a.set(pair.Column, link+"."+compile.QuoteIdent(pair.RefColumn), "", path); err != nil {
				return err
			}
			g.need(link, pair.RefColumn)
		case clear != "":
			if err := a.set(pair.Column, fmt.Sprintf("CASE WHEN %s THEN NULL ELSE %s.%s END", clear, ownerRef, col), "", path); err != nil {
				return err
			}
		}
	}
	if link != "" {
		a.source(link)
	}
	return nil
}

// references renders a predicate over the owner table holding when its FK
// references the parent row key identifies: a direct comparison of the FK
// columns when the key is the referenced key, an EXISTS lookup otherwise.
func (g *cteGen) references(owner *introspect.Table, fk *introspect.ForeignKey, parent *introspect.Table, key rowKey) string {
	ownerRef := compile.TableRefSQL(owner.Schema, owner.Name)
	pairs := equalityPairs(fk)
	colFor := map[string]string{}
	for _, pair := range pairs {
		colFor[pair.RefColumn] = pair.Column
	}
	direct := len(key.cols) == len(pairs)
	for _, cn := range key.cols {
		if _, ok := colFor[cn]; !ok {
			direct = false
		}
	}
	if direct {
		conds := make([]string, len(key.cols))
		for i, cn := range key.cols {
			col := colFor[cn]
			conds[i] = fmt.Sprintf("%s.%s %s %s", ownerRef, compile.QuoteIdent(col), compile.EqualOperator(g.req.Built, owner.Column(col)), key.params[i])
		}
		return strings.Join(conds, " AND ")
	}
	conds := []string{key.cond(g, parent, "__r")}
	for _, pair := range pairs {
		conds = append(conds, fmt.Sprintf("__r.%s %s %s.%s", compile.QuoteIdent(pair.RefColumn),
			compile.EqualOperator(g.req.Built, parent.Column(pair.RefColumn)), ownerRef, compile.QuoteIdent(pair.Column)))
	}
	return fmt.Sprintf("EXISTS (SELECT 1 FROM %s AS __r WHERE %s)", compile.TableRefSQL(parent.Schema, parent.Name), strings.Join(conds, " AND "))
}

// children emits the backward nested fields of the row in parentCTE.
func (g *cteGen) children(work []childWork, parentCTE string, depth int, path string) error {
	for _, cw := range work {
		if err := g.backward(cw.spec, cw.input, parentCTE, depth, path+cw.field); err != nil {
			return err
		}
	}
	return nil
}

// backward emits the operations of a backward nested field on the children
// of the row in parentCTE: create inserts children referencing it, connect
// points existing rows at it, update, delete and disconnect act on rows
// already referencing it.
func (g *cteGen) backward(spec *nestedSpec, nested map[string]any, parentCTE string, depth int, path string) error {
	if depth+1 > g.p.maxDepth {
		return compile.UserErrorf("nested-mutations: nesting depth exceeds limit %d", g.p.maxDepth)
	}
	ops := g.p.ops[spec.Input]
	child := spec.Owner
	ckey := child.Schema + "." + child.Name
	ref := compile.TableRefSQL(child.Schema, child.Name)
	for _, fname := range sortedKeys(nested) {
		op := ops[fname]
		if op == nil || nested[fname] == nil {
			continue
		}
		rows, ok := nested[fname].([]any)
		if !ok {
			rows = []any{nested[fname]}
		}
		for i, rowVal := range rows {
			obj, _ := rowVal.(map[string]any)
			if obj == nil {
				continue
			}
			if err := g.countOp(); err != nil {
				return err
			}
			opPath := fmt.Sprintf("%s.%s[%d]", path, fname, i)
			if op.kind == opCreate {
				if err := g.insertRow(child, g.p.inputForTable[ckey], obj, g.name("c"), depth+1, &parentFill{fk: spec.FK, cte: parentCTE}, opPath+"."); err != nil {
					return err
				}
				continue
			}
			key, err := g.opKey(child, op, obj, opPath)
			if err != nil {
				return err
			}
			cte := g.name("c")
			if op.kind == opUpdate {
				patch, _ := obj[patchField(g.req.Built.TypeForTable[ckey])].(map[string]any)
				if err := g.updateRow(child, g.p.patchForTable[ckey], patch, cte, depth+1, key, g.childScope(spec.FK, child, parentCTE), opPath+"."); err != nil {
					return err
				}
				g.guard(cte, opPath)
				continue
			}
			patched := map[string]bool{}
			var sets []string
			for _, pair := range equalityPairs(spec.FK) {
				patched[pair.Column] = true
				val := "NULL"
				if op.kind == opConnect {
					val = parentCTE + "." + compile.QuoteIdent(pair.RefColumn)
					g.need(parentCTE, pair.RefColumn)
				}
				sets = append(sets, compile.QuoteIdent(pair.Column)+" = "+val)
			}
			switch op.kind {
			case opConnect:
				g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  UPDATE %s SET %s FROM %s WHERE %s RETURNING %s\n)",
					cte, ref, strings.Join(sets, ", "), parentCTE, key.cond(g, child, ref), g.returning(cte, child, ref)))
				g.recordChange(child, cte, key, patched, true)
				g.guard(cte, opPath)
			case opDelete:
				sc := g.childScope(spec.FK, child, parentCTE)
				g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  DELETE FROM %s USING %s WHERE %s AND %s RETURNING %s\n)",
					cte, ref, parentCTE, key.cond(g, child, ref), sc.cond(ref), g.returning(cte, child, ref)))
				g.recordChange(child, cte, key, nil, false)
				g.guard(cte, opPath)
			case opDisconnect:
				sc := g.childScope(spec.FK, child, parentCTE)
				g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  UPDATE %s SET %s FROM %s WHERE %s AND %s RETURNING %s\n)",
					cte, ref, strings.Join(sets, ", "), parentCTE, key.cond(g, child, ref), sc.cond(ref), g.returning(cte, child, ref)))
				g.recordChange(child, cte, key, patched, true)
			}
		}
	}
	return nil
}

// equalityPairs returns the FK column pairs a nested mutation copies from
// the parent row: every pair except the period of a PERIOD FK, which the
// child supplies itself (its period need only be covered by the parent's).
func equalityPairs(fk *introspect.ForeignKey) []introspect.JoinPair {
	var out []introspect.JoinPair
	for _, pair := range fk.JoinPairs() {
		if pair.Op == introspect.JoinEqual {
			out = append(out, pair)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// ---- RequestHook ----

// BeforeOperation forces a transaction when the operation uses nested
// mutation fields (unless force_transactions is disabled). Root fields are
// collected as the executor runs them, through fragments and @skip/@include.
func (p *Plugin) BeforeOperation(ctx context.Context, op *exec.Operation) (context.Context, error) {
	if !p.forceTx || op.Type != ast.Mutation {
		return ctx, nil
	}
	var frags ast.FragmentDefinitionList
	if op.Document != nil {
		frags = op.Document.Fragments
	}
	for _, f := range exec.CollectRootFields(op.Definition.SelectionSet, frags, op.Vars) {
		if f.Definition == nil {
			continue
		}
		for _, name := range [...]string{"input", "patch"} {
			argDef := f.Definition.Arguments.ForName(name)
			arg := f.Arguments.ForName(name)
			if argDef == nil || arg == nil {
				continue
			}
			v, err := arg.Value.Value(op.Vars)
			if err != nil {
				continue
			}
			if p.usesNested(argDef.Type.Name(), v) {
				op.ForceTx = true
				return ctx, nil
			}
		}
	}
	return ctx, nil
}

func (p *Plugin) AfterOperation(context.Context, *exec.Operation, *exec.Result) {}
