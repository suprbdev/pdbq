package compile_test

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
)

// rootField parses and validates query against the fixture schema and
// returns its first root field.
func rootField(t *testing.T, query string) *ast.Field {
	t.Helper()
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.ValidateWithRules(build(t).Schema, doc, nil); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	return doc.Operations[0].SelectionSet[0].(*ast.Field)
}

// windowAt returns a copy of f whose descendant at path (response keys
// below f; the n-th field with the last key when it repeats) is windowed.
func windowAt(f *ast.Field, path []string, nth, id, start, count int) *ast.Field {
	if len(path) == 0 {
		return compile.StreamWindow(f, id, start, count)
	}
	c := *f
	c.SelectionSet = slices.Clone(f.SelectionSet)
	seen := 0
	for i, item := range c.SelectionSet {
		sub, ok := item.(*ast.Field)
		if !ok || sub.Alias != path[0] {
			continue
		}
		if len(path) == 1 && seen < nth {
			seen++
			continue
		}
		c.SelectionSet[i] = windowAt(sub, path[1:], 0, id, start, count)
		break
	}
	return &c
}

func compileField(t *testing.T, f *ast.Field) *compile.Statement {
	t.Helper()
	built := build(t)
	stmt, err := compile.New(built).Compile(context.Background(), &compile.Request{
		Field: f, Vars: map[string]any{}, Built: built, MaxDepth: 15, MaxPageSize: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return stmt
}

// TestStreamWindowIsAPage checks that a window over a connection whose only
// selection is the streamed list compiles exactly like the page it covers.
func TestStreamWindowIsAPage(t *testing.T) {
	cases := []struct {
		name, query, page string
		start, count      int
	}{
		{"middle", `{ allUsers(first: 10, orderBy: [EMAIL_ASC]) { nodes { id email } } }`, `{ allUsers(first: 3, offset: 2, orderBy: [EMAIL_ASC]) { nodes { id email } } }`, 2, 3},
		{"clamped to first", `{ allUsers(first: 4) { nodes { id } } }`, `{ allUsers(first: 1, offset: 3) { nodes { id } } }`, 3, 5},
		{"past the page", `{ allUsers(first: 4) { nodes { id } } }`, `{ allUsers(first: 0, offset: 6) { nodes { id } } }`, 6, 5},
		{"offset argument", `{ allUsers(offset: 5, first: 20) { edges { cursor node { id } } } }`, `{ allUsers(offset: 7, first: 4) { edges { cursor node { id } } } }`, 2, 4},
		{"default page size", `{ allPosts { __typename nodes { title } } }`, `{ allPosts(first: 10, offset: 1) { __typename nodes { title } } }`, 1, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := rootField(t, tc.query)
			key := "nodes"
			if strings.Contains(tc.query, "edges") {
				key = "edges"
			}
			got := compileField(t, windowAt(f, []string{key}, 0, 7, tc.start, tc.count))
			want := compileField(t, rootField(t, tc.page))
			if got.SQL != want.SQL || !reflect.DeepEqual(got.Args, want.Args) {
				t.Errorf("windowed:\n%s\n%v\nwant:\n%s\n%v", got.SQL, got.Args, want.SQL, want.Args)
			}
			if !reflect.DeepEqual(got.Streamed, []int{7}) {
				t.Errorf("Streamed = %v, want [7]", got.Streamed)
			}
		})
	}
}

// TestStreamWindowBesideOtherSelections checks that the windowed list of a
// connection that selects more than the list reads its own page while the
// rest of the connection keeps the whole one.
func TestStreamWindowBesideOtherSelections(t *testing.T) {
	f := rootField(t, `{ allUsers(first: 10) { totalCount pageInfo { hasNextPage } nodes { id } } }`)
	plain := compileField(t, f)
	got := compileField(t, windowAt(f, []string{"nodes"}, 0, 3, 0, 2))
	if !reflect.DeepEqual(got.Streamed, []int{3}) {
		t.Fatalf("Streamed = %v, want [3]", got.Streamed)
	}
	if !strings.Contains(got.SQL, ")->'nodes'") {
		t.Errorf("windowed list not compiled apart:\n%s", got.SQL)
	}
	if strings.Count(got.SQL, "count(*)") != strings.Count(plain.SQL, "count(*)") {
		t.Errorf("windowing changed the page aggregates:\n%s", got.SQL)
	}
}

func TestStreamWindowNested(t *testing.T) {
	f := rootField(t, `{ userById(id: 1) { email postsByAuthorId(first: 5) { nodes { title } } } }`)
	got := compileField(t, windowAt(f, []string{"postsByAuthorId", "nodes"}, 0, 4, 1, 2))
	if !reflect.DeepEqual(got.Streamed, []int{4}) {
		t.Fatalf("Streamed = %v, want [4]", got.Streamed)
	}
}

// TestStreamWindowMergedField checks that the window survives the merge of
// fields sharing its response key.
func TestStreamWindowMergedField(t *testing.T) {
	f := rootField(t, `{ allUsers(first: 5) { nodes { id } nodes { email } } }`)
	got := compileField(t, windowAt(f, []string{"nodes"}, 1, 9, 0, 1))
	if !reflect.DeepEqual(got.Streamed, []int{9}) {
		t.Fatalf("Streamed = %v, want [9]", got.Streamed)
	}
}

// TestStreamWindowIgnored covers the lists the compiler returns whole.
func TestStreamWindowIgnored(t *testing.T) {
	cases := map[string]struct {
		query string
		path  []string
	}{
		"backward page":   {`{ allUsers(last: 3) { nodes { id } } }`, []string{"nodes"}},
		"scalar list":     {`{ userById(id: 1) { tags } }`, []string{"tags"}},
		"not a page list": {`{ allUsers(first: 2) { totalCount } }`, []string{"totalCount"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := rootField(t, tc.query)
			plain := compileField(t, f)
			got := compileField(t, windowAt(f, tc.path, 0, 1, 1, 1))
			if len(got.Streamed) != 0 {
				t.Errorf("Streamed = %v, want none", got.Streamed)
			}
			if got.SQL != plain.SQL {
				t.Errorf("ignored window changed the SQL:\n%s", got.SQL)
			}
		})
	}
}
