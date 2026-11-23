package exec_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

func newExecutor(t *testing.T) *exec.Executor {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	c := compile.New(built)
	// Pool stays nil: introspection must never reach the database.
	return exec.New(nil, built, c.Compile, nil, exec.Options{})
}

func TestIntrospectionQuery(t *testing.T) {
	e := newExecutor(t)
	// exec.IntrospectionQuery is what `pdbq schema print --json` executes; the
	// nil pool in newExecutor also proves it never reaches the database.
	res := e.Execute(context.Background(), exec.Request{Query: exec.IntrospectionQuery})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	raw, ok := res.Data["__schema"]
	if !ok {
		t.Fatal("missing __schema in data")
	}
	var sch struct {
		QueryType    struct{ Name string } `json:"queryType"`
		MutationType struct{ Name string } `json:"mutationType"`
		Types        []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Fields []struct {
				Name string `json:"name"`
				Type json.RawMessage
			} `json:"fields"`
		} `json:"types"`
		Directives []struct{ Name string } `json:"directives"`
	}
	if err := json.Unmarshal(raw, &sch); err != nil {
		t.Fatal(err)
	}
	if sch.QueryType.Name != "Query" {
		t.Errorf("queryType = %q, want Query", sch.QueryType.Name)
	}
	if sch.MutationType.Name != "Mutation" {
		t.Errorf("mutationType = %q, want Mutation", sch.MutationType.Name)
	}
	byName := map[string]bool{}
	for _, typ := range sch.Types {
		byName[typ.Name] = true
		if typ.Name == "Query" {
			for _, f := range typ.Fields {
				if strings.HasPrefix(f.Name, "__") {
					t.Errorf("meta field %q leaked into Query fields", f.Name)
				}
			}
		}
	}
	for _, want := range []string{"User", "Post", "UserFilter", "Mood", "PageInfo", "String", "__Type"} {
		if !byName[want] {
			t.Errorf("types missing %q", want)
		}
	}
	if len(sch.Directives) == 0 {
		t.Error("no directives returned")
	}
}

func TestNodeInterfaceIntrospection(t *testing.T) {
	e := newExecutor(t)
	res := e.Execute(context.Background(), exec.Request{
		Query: `{
			iface: __type(name: "Node") { kind name possibleTypes { name } }
			user: __type(name: "User") { interfaces { name } }
		}`,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	var iface struct {
		Kind          string `json:"kind"`
		Name          string `json:"name"`
		PossibleTypes []struct {
			Name string `json:"name"`
		} `json:"possibleTypes"`
	}
	if err := json.Unmarshal(res.Data["iface"], &iface); err != nil {
		t.Fatal(err)
	}
	if iface.Kind != "INTERFACE" || iface.Name != "Node" {
		t.Errorf("Node = %s %s, want INTERFACE Node", iface.Kind, iface.Name)
	}
	possible := map[string]bool{}
	for _, p := range iface.PossibleTypes {
		possible[p.Name] = true
	}
	if !possible["User"] || !possible["Post"] {
		t.Errorf("Node possibleTypes missing User/Post: %v", iface.PossibleTypes)
	}
	var user struct {
		Interfaces []struct {
			Name string `json:"name"`
		} `json:"interfaces"`
	}
	if err := json.Unmarshal(res.Data["user"], &user); err != nil {
		t.Fatal(err)
	}
	if len(user.Interfaces) != 1 || user.Interfaces[0].Name != "Node" {
		t.Errorf("User.interfaces = %v, want [Node]", user.Interfaces)
	}
}

func TestTypeByName(t *testing.T) {
	e := newExecutor(t)
	res := e.Execute(context.Background(), exec.Request{
		Query:     `query($n: String!) { __type(name: $n) { kind name fields { name type { kind ofType { kind name } } } } }`,
		Variables: map[string]any{"n": "User"},
	})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	var typ struct {
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		Fields []struct {
			Name string `json:"name"`
			Type struct {
				Kind   string `json:"kind"`
				OfType *struct {
					Kind string `json:"kind"`
					Name string `json:"name"`
				} `json:"ofType"`
			} `json:"type"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(res.Data["__type"], &typ); err != nil {
		t.Fatal(err)
	}
	if typ.Kind != "OBJECT" || typ.Name != "User" {
		t.Errorf("got %s %s, want OBJECT User", typ.Kind, typ.Name)
	}
	if len(typ.Fields) == 0 {
		t.Fatal("no fields on User")
	}
	// id: Int! must render as NON_NULL wrapping Int.
	for _, f := range typ.Fields {
		if f.Name != "id" {
			continue
		}
		if f.Type.Kind != "NON_NULL" || f.Type.OfType == nil || f.Type.OfType.Name != "Int" {
			t.Errorf("id type kind=%s ofType=%+v, want NON_NULL of Int", f.Type.Kind, f.Type.OfType)
		}
	}
}

func TestTypeUnknownReturnsNull(t *testing.T) {
	e := newExecutor(t)
	res := e.Execute(context.Background(), exec.Request{
		Query: `{ __type(name: "Nope") { name } }`,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if string(res.Data["__type"]) != "null" {
		t.Errorf("__type = %s, want null", res.Data["__type"])
	}
}

func TestRootTypename(t *testing.T) {
	e := newExecutor(t)
	res := e.Execute(context.Background(), exec.Request{Query: `{ __typename }`})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if string(res.Data["__typename"]) != `"Query"` {
		t.Errorf("__typename = %s", res.Data["__typename"])
	}
}

// newLockedExecutor mirrors newExecutor with DisableIntrospection set.
func newLockedExecutor(t *testing.T) *exec.Executor {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	c := compile.New(built)
	return exec.New(nil, built, c.Compile, nil, exec.Options{DisableIntrospection: true})
}

func TestDisableIntrospectionRejectsSchemaAndType(t *testing.T) {
	e := newLockedExecutor(t)
	for _, q := range []string{
		`{ __schema { queryType { name } } }`,
		`{ __type(name: "Query") { name } }`,
		// Introspection hidden behind a fragment must be caught too.
		`query { ...f } fragment f on Query { __schema { queryType { name } } }`,
	} {
		res := e.Execute(context.Background(), exec.Request{Query: q})
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "introspection is disabled") {
			t.Errorf("query %q: errors = %v, want introspection-disabled error", q, res.Errors)
		}
	}
}

func TestDisableIntrospectionKeepsTypename(t *testing.T) {
	e := newLockedExecutor(t)
	res := e.Execute(context.Background(), exec.Request{Query: `{ __typename }`})
	if len(res.Errors) > 0 {
		t.Fatalf("errors: %v", res.Errors)
	}
	if string(res.Data["__typename"]) != `"Query"` {
		t.Errorf("__typename = %s, want "+`"Query"`, res.Data["__typename"])
	}
}

func introspectionBytesExecutor(t *testing.T, limit int) *exec.Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return exec.New(nil, built, compile.New(built).Compile, nil, exec.Options{MaxIntrospectionBytes: limit})
}

func TestIntrospectionQueryFitsDefaultLimit(t *testing.T) {
	e := introspectionBytesExecutor(t, 0)
	res := e.Execute(context.Background(), exec.Request{Query: exec.IntrospectionQuery})
	if len(res.Errors) > 0 {
		t.Fatalf("standard introspection query rejected: %v", res.Errors)
	}
	t.Logf("standard introspection response: %d bytes", len(res.Data["__schema"]))
}

func TestIntrospectionAliasAmplificationBounded(t *testing.T) {
	var b strings.Builder
	b.WriteString("{__schema{types{fields{")
	for i := range 2000 {
		fmt.Fprintf(&b, "a%d:name ", i)
	}
	b.WriteString("}}}}")
	e := introspectionBytesExecutor(t, 1<<20)
	res := e.Execute(context.Background(), exec.Request{Query: b.String()})
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "introspection response exceeds") {
		t.Fatalf("errors = %v, want introspection size rejection", res.Errors)
	}

	var roots strings.Builder
	roots.WriteString("{")
	for i := range 200 {
		fmt.Fprintf(&roots, "r%d:__schema{types{name description fields{name description}}} ", i)
	}
	roots.WriteString("}")
	res = e.Execute(context.Background(), exec.Request{Query: roots.String()})
	if len(res.Errors) == 0 {
		t.Fatal("repeated root introspection fields were not bounded across the operation")
	}
}
