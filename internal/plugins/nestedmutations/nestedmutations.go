// Package nestedmutations is a built-in plugin exercising every hook
// surface: it adds nested `create`/`connect` input fields on create-mutation
// inputs following foreign keys (SchemaHook), compiles them into a single
// multi-CTE SQL statement respecting FK direction and insertion order
// (CompileHook), and forces a transaction for nested mutations even when
// transactions are globally disabled (RequestHook), unless
// plugins.nested-mutations.force_transactions is false.
package nestedmutations

import (
	"context"
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/introspect"
	"github.com/suprbdev/pdbq/internal/schema"
)

const defaultMaxDepth = 3

type Plugin struct {
	forceTx  bool
	maxDepth int

	// specs: create-input type name -> nested field name -> spec.
	specs map[string]map[string]*nestedSpec
	// inputForTable: "schema.table" -> create input type name.
	inputForTable map[string]string
}

type nestedSpec struct {
	Forward bool // true: FK on this table points to a parent to create/connect
	FK      *introspect.ForeignKey
	Owner   *introspect.Table // table owning the FK (child)
	Target  *introspect.Table // referenced table (parent) for forward; child table for reverse
}

// New builds the plugin from its `plugins.settings.nested-mutations` map.
func New(settings map[string]any) *Plugin {
	p := &Plugin{forceTx: true, maxDepth: defaultMaxDepth,
		specs: map[string]map[string]*nestedSpec{}, inputForTable: map[string]string{}}
	if v, ok := settings["force_transactions"].(bool); ok {
		p.forceTx = v
	}
	if v, ok := settings["max_depth"].(int); ok && v > 0 {
		p.maxDepth = v
	}
	if v, ok := settings["max_depth"].(float64); ok && v > 0 {
		p.maxDepth = int(v)
	}
	return p
}

func (p *Plugin) Name() string  { return "nested-mutations" }
func (p *Plugin) Priority() int { return 200 }

// ---- SchemaHook ----

func (p *Plugin) TransformSchema(_ context.Context, b *schema.Builder) error {
	for _, t := range b.Catalog.Tables {
		if !t.Insertable() {
			continue
		}
		inputName := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: t.Schema, Table: t.Name})
		in, ok := b.Inputs[inputName]
		if !ok {
			continue
		}
		p.inputForTable[t.Schema+"."+t.Name] = inputName

		// Forward: FKs on t -> nested create/connect of the parent row.
		for _, fk := range t.ForeignKeys {
			parent := b.Catalog.Table(fk.RefSchema, fk.RefTable)
			if parent == nil {
				continue
			}
			relName := b.Inflect(inflect.KindRelationForward, inflect.Input{
				Schema: t.Schema, Table: fk.RefTable, Column: fk.Columns[0], Columns: fk.Columns,
				Constraint: fk.Name,
			})
			nestedType := p.ensureNestedInput(b, t, parent, fk, relName)
			if nestedType == "" || in.Field(relName) != nil {
				continue
			}
			in.AddField(&schema.InputField{
				Name:        relName,
				Type:        nestedType,
				Description: "Nested create/connect of the referenced " + fk.RefTable + " row.",
				Nested:      map[string]any{"plugin": p.Name()},
			})
			p.addSpec(inputName, relName, &nestedSpec{Forward: true, FK: fk, Owner: t, Target: parent})
			// The FK columns can now come from the nested input, so they are
			// no longer required in the GraphQL input (DB still enforces).
			for _, col := range fk.Columns {
				fieldName := b.Inflect(inflect.KindFieldName, inflect.Input{Table: t.Name, Column: col})
				if f := in.Field(fieldName); f != nil {
					f.Type = strings.TrimSuffix(f.Type, "!")
				}
			}
		}

		// Reverse: FKs from other tables to t -> nested create of child rows.
		for _, child := range b.Catalog.Tables {
			if !child.Insertable() {
				continue
			}
			for _, fk := range child.ForeignKeys {
				if fk.RefSchema != t.Schema || fk.RefTable != t.Name {
					continue
				}
				childInput := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: child.Schema, Table: child.Name})
				if _, ok := b.Inputs[childInput]; !ok {
					continue
				}
				relName := b.Inflect(inflect.KindRelationBackward, inflect.Input{
					Schema: child.Schema, Table: child.Name, Columns: fk.Columns,
					Constraint: fk.Name,
				})
				wrapName := p.ensureChildrenInput(b, child, childInput, relName)
				if in.Field(relName) != nil {
					continue
				}
				in.AddField(&schema.InputField{
					Name:        relName,
					Type:        wrapName,
					Description: "Nested create of referencing " + child.Name + " rows.",
					Nested:      map[string]any{"plugin": p.Name()},
				})
				p.addSpec(inputName, relName, &nestedSpec{Forward: false, FK: fk, Owner: child, Target: child})
				// Child FK columns become optional (supplied by the parent).
				childIn := b.Inputs[childInput]
				for _, col := range fk.Columns {
					fieldName := b.Inflect(inflect.KindFieldName, inflect.Input{Table: child.Name, Column: col})
					if f := childIn.Field(fieldName); f != nil {
						f.Type = strings.TrimSuffix(f.Type, "!")
					}
				}
			}
		}
	}
	return nil
}

func (p *Plugin) addSpec(inputName, field string, s *nestedSpec) {
	if p.specs[inputName] == nil {
		p.specs[inputName] = map[string]*nestedSpec{}
	}
	p.specs[inputName][field] = s
}

// ensureNestedInput creates `<Type><Rel>NestedInput { create, connect }`.
func (p *Plugin) ensureNestedInput(b *schema.Builder, owner, parent *introspect.Table, fk *introspect.ForeignKey, relName string) string {
	ownerType := b.TypeForTable[owner.Schema+"."+owner.Name]
	name := ownerType + inflect.UpperCamel(relName) + "NestedInput"
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	in := &schema.Input{Name: name, Description: "Create the referenced row or connect to an existing one (exactly one of the fields)."}
	if parentInput := b.Inflect(inflect.KindCreateInput, inflect.Input{Schema: parent.Schema, Table: parent.Name}); b.Inputs[parentInput] != nil && parent.Insertable() {
		in.AddField(&schema.InputField{Name: "create", Type: parentInput})
	}
	if connect := p.ensureConnectInput(b, parent, fk); connect != "" {
		in.AddField(&schema.InputField{Name: "connect", Type: connect})
	}
	if len(in.Fields) == 0 {
		return ""
	}
	b.Inputs[name] = in
	return name
}

// ensureConnectInput creates `<Parent>ConnectInput` keyed by the columns the
// FK references.
func (p *Plugin) ensureConnectInput(b *schema.Builder, parent *introspect.Table, fk *introspect.ForeignKey) string {
	parentType := b.TypeForTable[parent.Schema+"."+parent.Name]
	name := parentType + "ConnectInput"
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	in := &schema.Input{Name: name}
	for _, col := range fk.RefColumns {
		c := parent.Column(col)
		if c == nil {
			return ""
		}
		fieldName := b.Inflect(inflect.KindFieldName, inflect.Input{Table: parent.Name, Column: col})
		in.AddField(&schema.InputField{Name: fieldName, Type: gqlTypeRequired(b, c), Column: col})
	}
	b.Inputs[name] = in
	return name
}

// ensureChildrenInput creates `<Owner><Rel>NestedChildrenInput { create: [ChildCreateInput!] }`.
func (p *Plugin) ensureChildrenInput(b *schema.Builder, child *introspect.Table, childInput, relName string) string {
	childType := b.TypeForTable[child.Schema+"."+child.Name]
	name := childType + inflect.UpperCamel(relName) + "NestedChildrenInput"
	if _, ok := b.Inputs[name]; ok {
		return name
	}
	b.Inputs[name] = &schema.Input{Name: name, Fields: []*schema.InputField{
		{Name: "create", Type: "[" + childInput + "!]"},
	}}
	return name
}

func gqlTypeRequired(b *schema.Builder, c *introspect.Column) string {
	// Reuse the builder's column typing through a throwaway field on the
	// parent object type: all catalog columns already have generated fields.
	// Fall back to String! if not resolvable.
	for _, obj := range b.Objects {
		for _, f := range obj.Fields {
			if f.Meta != nil && f.Meta.Kind == schema.KindColumn && f.Meta.Column == c {
				return strings.TrimSuffix(f.Type, "!") + "!"
			}
		}
	}
	return "String!"
}

// ---- CompileHook ----

func (p *Plugin) WrapCompile(next compile.Func) compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		meta := metaFor(req)
		if meta == nil || meta.Kind != schema.KindCreate {
			return next(ctx, req)
		}
		inputArg := req.Field.Arguments.ForName("input")
		if inputArg == nil {
			return next(ctx, req)
		}
		inputVal, err := inputArg.Value.Value(req.Vars)
		if err != nil {
			return nil, err
		}
		input, _ := inputVal.(map[string]any)
		inputType := argTypeName(req.Field, "input")
		if !p.hasNested(inputType, input) {
			return next(ctx, req)
		}

		c := compile.New(req.Built)
		ps := &compile.ParamSet{}
		g := &cteGen{p: p, req: req, ps: ps, childCTEs: map[string][]string{}}
		if _, err := g.insertChain(meta.Table, inputType, input, "__mut", 1); err != nil {
			return nil, err
		}
		// Rows inserted by CTEs are invisible to plain table scans within the
		// same statement (single-snapshot rule), so the payload reads nested
		// children from a UNION of their insert CTEs instead of the table.
		overrides := map[string]string{}
		for tableKey, names := range g.childCTEs {
			union := make([]string, len(names))
			for i, n := range names {
				union[i] = "SELECT * FROM " + n
			}
			vis := g.name("vis")
			g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  %s\n)", vis, strings.Join(union, " UNION ALL ")))
			overrides[tableKey] = vis
		}
		return c.MutationWithCTEs(req, req.Field, meta, strings.Join(g.ctes, ",\n"), ps, overrides)
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

func (p *Plugin) hasNested(inputType string, input map[string]any) bool {
	specs := p.specs[inputType]
	for k, v := range input {
		if _, ok := specs[k]; ok && v != nil {
			return true
		}
	}
	return false
}

// cteGen accumulates the WITH chain for one nested mutation.
type cteGen struct {
	p    *Plugin
	req  *compile.Request
	ps   *compile.ParamSet
	ctes []string
	n    int
	// childCTEs collects insert-CTE names per "schema.table" for payload
	// visibility overrides.
	childCTEs map[string][]string
}

func (g *cteGen) name(prefix string) string {
	g.n++
	return fmt.Sprintf("__%s_%d", prefix, g.n)
}

// addParam coerces v for the named column (enum labels, jsonb encoding)
// before registering it as a placeholder.
func (g *cteGen) addParam(t *introspect.Table, colName string, v any) string {
	return g.ps.Add(compile.CoerceInput(g.req.Built, t.Column(colName), v))
}

// insertChain emits CTEs to insert one row into t from input, resolving
// forward nested fields (parents first), then emits child CTEs for reverse
// nested fields. Returns the CTE name holding the inserted row.
func (g *cteGen) insertChain(t *introspect.Table, inputType string, input map[string]any, cteName string, depth int) (string, error) {
	if depth > g.p.maxDepth {
		return "", fmt.Errorf("nested-mutations: nesting depth exceeds limit %d", g.p.maxDepth)
	}
	specs := g.p.specs[inputType]
	binding := g.req.Built.InputColumns[inputType]

	var cols, vals, fromCTEs []string
	// Plain columns, deterministic order.
	for _, fname := range sortedKeys(input) {
		if input[fname] == nil {
			continue
		}
		if col, ok := binding[fname]; ok {
			cols = append(cols, compile.QuoteIdent(col))
			vals = append(vals, g.addParam(t, col, input[fname]))
		}
	}
	// Forward nested: resolve/insert parents first.
	var childrenSpecs []childWork
	for _, fname := range sortedKeys(input) {
		spec, ok := specs[fname]
		if !ok || input[fname] == nil {
			continue
		}
		nested, _ := input[fname].(map[string]any)
		if spec.Forward {
			parentCTE, err := g.resolveParent(spec, nested, depth)
			if err != nil {
				return "", err
			}
			for i, col := range spec.FK.Columns {
				cols = append(cols, compile.QuoteIdent(col))
				vals = append(vals, parentCTE+"."+compile.QuoteIdent(spec.FK.RefColumns[i]))
			}
			fromCTEs = append(fromCTEs, parentCTE)
		} else {
			childrenSpecs = append(childrenSpecs, childWork{spec: spec, input: nested})
		}
	}

	tableSQL := compile.TableRefSQL(t.Schema, t.Name)
	var insert string
	switch {
	case len(cols) == 0:
		insert = fmt.Sprintf("INSERT INTO %s DEFAULT VALUES RETURNING *", tableSQL)
	case len(fromCTEs) == 0:
		insert = fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING *",
			tableSQL, strings.Join(cols, ", "), strings.Join(vals, ", "))
	default:
		insert = fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM %s RETURNING *",
			tableSQL, strings.Join(cols, ", "), strings.Join(vals, ", "), strings.Join(fromCTEs, ", "))
	}
	g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  %s\n)", cteName, insert))

	// Reverse nested: insert children referencing this row.
	for _, cw := range childrenSpecs {
		if err := g.insertChildren(cw.spec, cw.input, cteName, depth); err != nil {
			return "", err
		}
	}
	return cteName, nil
}

type childWork struct {
	spec  *nestedSpec
	input map[string]any
}

// resolveParent emits the CTE providing the parent row for a forward nested
// field: either a lookup (connect) or a recursive insert (create).
func (g *cteGen) resolveParent(spec *nestedSpec, nested map[string]any, depth int) (string, error) {
	connect, hasConnect := nested["connect"].(map[string]any)
	create, hasCreate := nested["create"].(map[string]any)
	if hasConnect == hasCreate {
		return "", fmt.Errorf("nested-mutations: specify exactly one of create/connect for %s", spec.FK.RefTable)
	}
	if hasConnect {
		name := g.name("p")
		connectType := g.req.Built.TypeForTable[spec.Target.Schema+"."+spec.Target.Name] + "ConnectInput"
		binding := g.req.Built.InputColumns[connectType]
		var conds []string
		for _, fname := range sortedKeys(connect) {
			col, ok := binding[fname]
			if !ok {
				continue
			}
			conds = append(conds, compile.QuoteIdent(col)+" = "+g.addParam(spec.Target, col, connect[fname]))
		}
		if len(conds) == 0 {
			return "", fmt.Errorf("nested-mutations: empty connect for %s", spec.FK.RefTable)
		}
		g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  SELECT * FROM %s WHERE %s\n)",
			name, compile.TableRefSQL(spec.Target.Schema, spec.Target.Name), strings.Join(conds, " AND ")))
		return name, nil
	}
	parentInput := g.p.inputForTable[spec.Target.Schema+"."+spec.Target.Name]
	if parentInput == "" {
		return "", fmt.Errorf("nested-mutations: %s is not insertable", spec.FK.RefTable)
	}
	return g.insertChain(spec.Target, parentInput, create, g.name("p"), depth+1)
}

// insertChildren emits one INSERT CTE per nested child row, wiring the FK
// columns to the parent CTE.
func (g *cteGen) insertChildren(spec *nestedSpec, nested map[string]any, parentCTE string, depth int) error {
	if depth+1 > g.p.maxDepth {
		return fmt.Errorf("nested-mutations: nesting depth exceeds limit %d", g.p.maxDepth)
	}
	rows, _ := nested["create"].([]any)
	childInput := g.p.inputForTable[spec.Owner.Schema+"."+spec.Owner.Name]
	binding := g.req.Built.InputColumns[childInput]
	for _, rowVal := range rows {
		row, _ := rowVal.(map[string]any)
		var cols, vals []string
		for _, fname := range sortedKeys(row) {
			if row[fname] == nil {
				continue
			}
			if col, ok := binding[fname]; ok {
				cols = append(cols, compile.QuoteIdent(col))
				vals = append(vals, g.addParam(spec.Owner, col, row[fname]))
			}
		}
		for i, col := range spec.FK.Columns {
			cols = append(cols, compile.QuoteIdent(col))
			vals = append(vals, parentCTE+"."+compile.QuoteIdent(spec.FK.RefColumns[i]))
		}
		cteName := g.name("c")
		g.ctes = append(g.ctes, fmt.Sprintf("%s AS (\n  INSERT INTO %s (%s) SELECT %s FROM %s RETURNING *\n)",
			cteName, compile.TableRefSQL(spec.Owner.Schema, spec.Owner.Name),
			strings.Join(cols, ", "), strings.Join(vals, ", "), parentCTE))
		g.childCTEs[spec.Owner.Schema+"."+spec.Owner.Name] = append(g.childCTEs[spec.Owner.Schema+"."+spec.Owner.Name], cteName)
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

// ---- RequestHook ----

// BeforeOperation forces a transaction when the operation uses nested
// mutation inputs (unless force_transactions is disabled).
func (p *Plugin) BeforeOperation(ctx context.Context, op *exec.Operation) (context.Context, error) {
	if !p.forceTx || op.Type != ast.Mutation {
		return ctx, nil
	}
	for _, sel := range op.Definition.SelectionSet {
		f, ok := sel.(*ast.Field)
		if !ok || f.Definition == nil {
			continue
		}
		argDef := f.Definition.Arguments.ForName("input")
		arg := f.Arguments.ForName("input")
		if argDef == nil || arg == nil {
			continue
		}
		v, err := arg.Value.Value(op.Vars)
		if err != nil {
			continue
		}
		if input, ok := v.(map[string]any); ok && p.hasNested(argDef.Type.Name(), input) {
			op.ForceTx = true
			return ctx, nil
		}
	}
	return ctx, nil
}

func (p *Plugin) AfterOperation(context.Context, *exec.Operation, *exec.Result) {}
