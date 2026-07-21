package compile_test

import (
	"context"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
)

// FuzzFilter drives arbitrary values through the filter-input -> SQL path
// and asserts the invariant that makes injection impossible: user values
// never appear in the SQL text, only in the parameter list.
func FuzzFilter(f *testing.F) {
	built := build(f)
	compiler := compile.New(built)

	const query = `query ($e: String, $tag: [String!], $n: Int) {
		allUsers(filter: {
			and: [
				{email: {like: $e, startsWith: $e}}
				{or: [{id: {greaterThan: $n}}, {tags: {contains: $tag}}]}
				{settings: {pathExists: $e, pathMatch: $e}}
			]
		}) { nodes { id } }
	}`
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		f.Fatal(perr)
	}
	if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
		f.Fatal(errs)
	}
	op := doc.Operations[0]

	compileWith := func(t testing.TB, email, tag string, n int) *compile.Statement {
		t.Helper()
		vars, verr := validator.VariableValues(built.Schema, op, map[string]any{
			"e": email, "tag": []any{tag}, "n": n,
		})
		if verr != nil {
			t.Skipf("rejected variables: %v", verr)
		}
		stmt, err := compiler.Compile(context.Background(), &compile.Request{
			Field:    op.SelectionSet[0].(*ast.Field),
			Vars:     vars,
			Built:    built,
			MaxDepth: 15,
		})
		if err != nil {
			t.Fatalf("compile error on valid input: %v", err)
		}
		return stmt
	}
	// The invariant that makes injection impossible: the SQL text is a pure
	// function of the query shape — values only move through the parameter
	// list, so any two value sets must produce byte-identical SQL.
	baseline := compileWith(f, "a@example.com", "admin", 5)

	f.Add("a@example.com", "admin", 5)
	f.Add(`'; DROP TABLE users; --`, `"]}`, -1)
	f.Add("%_\\", "'", 0)
	f.Add("", "", 1<<30)

	f.Fuzz(func(t *testing.T, email, tag string, n int) {
		stmt := compileWith(t, email, tag, n)
		if stmt.SQL != baseline.SQL {
			t.Fatalf("SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}
