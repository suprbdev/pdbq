// Introspection resolves the __schema / __type / __typename meta fields in Go
// against the built *ast.Schema. These fields have no SQL mapping, so they are
// answered before compilation; everything needed is already in memory.
package exec

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/compile"
)

// IntrospectionQuery is the standard full introspection document (the shape
// graphql-js getIntrospectionQuery() produces). It is resolved entirely in
// memory, so it works on an executor with a nil pool — `pdbq schema print
// --json` relies on that.
const IntrospectionQuery = `
query IntrospectionQuery {
  __schema {
    queryType { name }
    mutationType { name }
    subscriptionType { name }
    types { ...FullType }
    directives {
      name
      description
      locations
      isRepeatable
      args { ...InputValue }
    }
  }
}
fragment FullType on __Type {
  kind
  name
  description
  specifiedByURL
  fields(includeDeprecated: true) {
    name
    description
    args { ...InputValue }
    type { ...TypeRef }
    isDeprecated
    deprecationReason
  }
  inputFields { ...InputValue }
  interfaces { ...TypeRef }
  enumValues(includeDeprecated: true) {
    name
    description
    isDeprecated
    deprecationReason
  }
  possibleTypes { ...TypeRef }
}
fragment InputValue on __InputValue {
  name
  description
  type { ...TypeRef }
  defaultValue
}
fragment TypeRef on __Type {
  kind name ofType { kind name ofType { kind name ofType { kind name ofType { kind name
    ofType { kind name ofType { kind name ofType { kind name ofType { kind name } } } } } } } }
}
`

// isIntrospectionField reports whether a root field is a meta field that must
// be resolved in Go rather than compiled to SQL.
func isIntrospectionField(name string) bool {
	return strings.HasPrefix(name, "__")
}

// resolveIntrospection answers one introspection root field as raw JSON.
func (e *Executor) resolveIntrospection(op *Operation, f *ast.Field) (json.RawMessage, error) {
	in := &introspector{
		schema: e.Built.Schema,
		vars:   op.Vars,
		frags:  op.Document.Fragments,
	}
	var v any
	var err error
	switch f.Name {
	case "__typename":
		v = rootTypeNameFor(e.Built.Schema, op.Type)
	case "__schema":
		v, err = in.object(schemaVal{s: in.schema}, f.SelectionSet)
	case "__type":
		name, _ := in.argValue(f, "name")
		s, _ := name.(string)
		if def, ok := in.schema.Types[s]; ok {
			v, err = in.object(namedType(def), f.SelectionSet)
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func rootTypeNameFor(s *ast.Schema, op ast.Operation) string {
	if op == ast.Mutation && s.Mutation != nil {
		return s.Mutation.Name
	}
	return s.Query.Name
}

// introspector walks selection sets over in-memory schema values.
type introspector struct {
	schema *ast.Schema
	vars   map[string]any
	frags  ast.FragmentDefinitionList
}

// iObject is a resolvable introspection object (one of the __* types).
type iObject interface {
	typeName() string
	field(in *introspector, f *ast.Field) (any, error)
}

// object resolves a selection set against one introspection object.
func (in *introspector) object(v iObject, sel ast.SelectionSet) (map[string]any, error) {
	out := map[string]any{}
	for _, f := range in.expand(sel, v.typeName()) {
		if f.Name == "__typename" {
			out[f.Alias] = v.typeName()
			continue
		}
		fv, err := v.field(in, f)
		if err != nil {
			return nil, err
		}
		cv, err := in.complete(fv, f)
		if err != nil {
			return nil, err
		}
		out[f.Alias] = cv
	}
	return out, nil
}

// complete recurses into nested objects and lists; scalars pass through.
func (in *introspector) complete(v any, f *ast.Field) (any, error) {
	switch tv := v.(type) {
	case nil:
		return nil, nil
	case iObject:
		return in.object(tv, f.SelectionSet)
	case []any:
		out := make([]any, 0, len(tv))
		for _, item := range tv {
			cv, err := in.complete(item, f)
			if err != nil {
				return nil, err
			}
			out = append(out, cv)
		}
		return out, nil
	default:
		return v, nil
	}
}

// expand flattens fragments, keeping only selections whose type condition
// matches the concrete introspection type (they have no interfaces) and
// dropping selections excluded by @skip/@include.
func (in *introspector) expand(sel ast.SelectionSet, typeName string) []*ast.Field {
	var out []*ast.Field
	for _, item := range sel {
		switch v := item.(type) {
		case *ast.Field:
			if compile.SkipByDirectives(v.Directives, in.vars) {
				continue
			}
			out = append(out, v)
		case *ast.InlineFragment:
			if compile.SkipByDirectives(v.Directives, in.vars) {
				continue
			}
			if v.TypeCondition == "" || v.TypeCondition == typeName {
				out = append(out, in.expand(v.SelectionSet, typeName)...)
			}
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, in.vars) {
				continue
			}
			if def := in.frags.ForName(v.Name); def != nil && (def.TypeCondition == "" || def.TypeCondition == typeName) {
				out = append(out, in.expand(def.SelectionSet, typeName)...)
			}
		}
	}
	return out
}

func (in *introspector) argValue(f *ast.Field, name string) (any, bool) {
	arg := f.Arguments.ForName(name)
	if arg == nil {
		return nil, false
	}
	v, err := arg.Value.Value(in.vars)
	if err != nil || v == nil {
		return nil, false
	}
	return v, true
}

func (in *introspector) includeDeprecated(f *ast.Field) bool {
	v, ok := in.argValue(f, "includeDeprecated")
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// nullableString maps "" to JSON null, matching graphql-js descriptions.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// deprecationOf reads the @deprecated directive off a directive list.
func deprecationOf(dl ast.DirectiveList) (bool, any) {
	d := dl.ForName("deprecated")
	if d == nil {
		return false, nil
	}
	reason := "No longer supported"
	if arg := d.Arguments.ForName("reason"); arg != nil && arg.Value != nil {
		reason = arg.Value.Raw
	}
	return true, reason
}

// ---- __Schema ----

type schemaVal struct{ s *ast.Schema }

func (schemaVal) typeName() string { return "__Schema" }

func (v schemaVal) field(in *introspector, f *ast.Field) (any, error) {
	switch f.Name {
	case "description":
		return nullableString(v.s.Description), nil
	case "queryType":
		return namedType(v.s.Query), nil
	case "mutationType":
		if v.s.Mutation == nil {
			return nil, nil
		}
		return namedType(v.s.Mutation), nil
	case "subscriptionType":
		if v.s.Subscription == nil {
			return nil, nil
		}
		return namedType(v.s.Subscription), nil
	case "types":
		names := make([]string, 0, len(v.s.Types))
		for name := range v.s.Types {
			names = append(names, name)
		}
		sort.Strings(names)
		out := make([]any, 0, len(names))
		for _, name := range names {
			out = append(out, namedType(v.s.Types[name]))
		}
		return out, nil
	case "directives":
		names := make([]string, 0, len(v.s.Directives))
		for name := range v.s.Directives {
			names = append(names, name)
		}
		sort.Strings(names)
		out := make([]any, 0, len(names))
		for _, name := range names {
			out = append(out, directiveVal{d: v.s.Directives[name]})
		}
		return out, nil
	}
	return nil, nil
}

// ---- __Type ----

// typeVal is either a named type (def set) or a NON_NULL/LIST wrapper (typ
// set); wrappers unwrap one layer at a time via ofType.
type typeVal struct {
	def *ast.Definition
	typ *ast.Type
}

func namedType(def *ast.Definition) typeVal { return typeVal{def: def} }

// typeFromAST converts an ast.Type reference into the introspection wrapper
// chain: NON_NULL wraps LIST wraps the named type.
func typeFromAST(s *ast.Schema, t *ast.Type) typeVal {
	if t.NonNull || t.NamedType == "" {
		return typeVal{typ: t}
	}
	return typeVal{def: s.Types[t.NamedType]}
}

func (typeVal) typeName() string { return "__Type" }

func (v typeVal) field(in *introspector, f *ast.Field) (any, error) {
	// Wrapper layers only answer kind and ofType.
	if v.typ != nil {
		switch f.Name {
		case "kind":
			if v.typ.NonNull {
				return "NON_NULL", nil
			}
			return "LIST", nil
		case "ofType":
			if v.typ.NonNull {
				inner := *v.typ
				inner.NonNull = false
				return typeFromAST(in.schema, &inner), nil
			}
			return typeFromAST(in.schema, v.typ.Elem), nil
		}
		return nil, nil
	}
	d := v.def
	switch f.Name {
	case "kind":
		return string(d.Kind), nil
	case "name":
		return d.Name, nil
	case "description":
		return nullableString(d.Description), nil
	case "fields":
		if d.Kind != ast.Object && d.Kind != ast.Interface {
			return nil, nil
		}
		includeDep := in.includeDeprecated(f)
		out := []any{}
		for _, fd := range d.Fields {
			if strings.HasPrefix(fd.Name, "__") {
				continue
			}
			if dep, _ := deprecationOf(fd.Directives); dep && !includeDep {
				continue
			}
			out = append(out, fieldVal{d: fd})
		}
		return out, nil
	case "interfaces":
		if d.Kind != ast.Object && d.Kind != ast.Interface {
			return nil, nil
		}
		out := []any{}
		for _, name := range d.Interfaces {
			if def, ok := in.schema.Types[name]; ok {
				out = append(out, namedType(def))
			}
		}
		return out, nil
	case "possibleTypes":
		if d.Kind != ast.Interface && d.Kind != ast.Union {
			return nil, nil
		}
		out := []any{}
		for _, def := range in.schema.PossibleTypes[d.Name] {
			out = append(out, namedType(def))
		}
		return out, nil
	case "enumValues":
		if d.Kind != ast.Enum {
			return nil, nil
		}
		includeDep := in.includeDeprecated(f)
		out := []any{}
		for _, ev := range d.EnumValues {
			if dep, _ := deprecationOf(ev.Directives); dep && !includeDep {
				continue
			}
			out = append(out, enumVal{d: ev})
		}
		return out, nil
	case "inputFields":
		if d.Kind != ast.InputObject {
			return nil, nil
		}
		includeDep := in.includeDeprecated(f)
		out := []any{}
		for _, fd := range d.Fields {
			if dep, _ := deprecationOf(fd.Directives); dep && !includeDep {
				continue
			}
			out = append(out, inputVal{
				name: fd.Name, description: fd.Description, typ: fd.Type,
				def: fd.DefaultValue, directives: fd.Directives,
			})
		}
		return out, nil
	case "ofType", "specifiedByURL", "isOneOf":
		return nil, nil
	}
	return nil, nil
}

// ---- __Field ----

type fieldVal struct{ d *ast.FieldDefinition }

func (fieldVal) typeName() string { return "__Field" }

func (v fieldVal) field(in *introspector, f *ast.Field) (any, error) {
	switch f.Name {
	case "name":
		return v.d.Name, nil
	case "description":
		return nullableString(v.d.Description), nil
	case "args":
		includeDep := in.includeDeprecated(f)
		out := []any{}
		for _, ad := range v.d.Arguments {
			if dep, _ := deprecationOf(ad.Directives); dep && !includeDep {
				continue
			}
			out = append(out, inputVal{
				name: ad.Name, description: ad.Description, typ: ad.Type,
				def: ad.DefaultValue, directives: ad.Directives,
			})
		}
		return out, nil
	case "type":
		return typeFromAST(in.schema, v.d.Type), nil
	case "isDeprecated":
		dep, _ := deprecationOf(v.d.Directives)
		return dep, nil
	case "deprecationReason":
		_, reason := deprecationOf(v.d.Directives)
		return reason, nil
	}
	return nil, nil
}

// ---- __InputValue ----

type inputVal struct {
	name, description string
	typ               *ast.Type
	def               *ast.Value
	directives        ast.DirectiveList
}

func (inputVal) typeName() string { return "__InputValue" }

func (v inputVal) field(in *introspector, f *ast.Field) (any, error) {
	switch f.Name {
	case "name":
		return v.name, nil
	case "description":
		return nullableString(v.description), nil
	case "type":
		return typeFromAST(in.schema, v.typ), nil
	case "defaultValue":
		if v.def == nil {
			return nil, nil
		}
		return v.def.String(), nil
	case "isDeprecated":
		dep, _ := deprecationOf(v.directives)
		return dep, nil
	case "deprecationReason":
		_, reason := deprecationOf(v.directives)
		return reason, nil
	}
	return nil, nil
}

// ---- __EnumValue ----

type enumVal struct{ d *ast.EnumValueDefinition }

func (enumVal) typeName() string { return "__EnumValue" }

func (v enumVal) field(_ *introspector, f *ast.Field) (any, error) {
	switch f.Name {
	case "name":
		return v.d.Name, nil
	case "description":
		return nullableString(v.d.Description), nil
	case "isDeprecated":
		dep, _ := deprecationOf(v.d.Directives)
		return dep, nil
	case "deprecationReason":
		_, reason := deprecationOf(v.d.Directives)
		return reason, nil
	}
	return nil, nil
}

// ---- __Directive ----

type directiveVal struct{ d *ast.DirectiveDefinition }

func (directiveVal) typeName() string { return "__Directive" }

func (v directiveVal) field(in *introspector, f *ast.Field) (any, error) {
	switch f.Name {
	case "name":
		return v.d.Name, nil
	case "description":
		return nullableString(v.d.Description), nil
	case "isRepeatable":
		return v.d.IsRepeatable, nil
	case "locations":
		out := make([]any, 0, len(v.d.Locations))
		for _, loc := range v.d.Locations {
			out = append(out, string(loc))
		}
		return out, nil
	case "args":
		includeDep := in.includeDeprecated(f)
		out := []any{}
		for _, ad := range v.d.Arguments {
			if dep, _ := deprecationOf(ad.Directives); dep && !includeDep {
				continue
			}
			out = append(out, inputVal{
				name: ad.Name, description: ad.Description, typ: ad.Type,
				def: ad.DefaultValue, directives: ad.Directives,
			})
		}
		return out, nil
	}
	return nil, nil
}
