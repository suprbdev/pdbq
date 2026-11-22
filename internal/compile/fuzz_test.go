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
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
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

// FuzzSpatial is FuzzFilter for the PostGIS path: WKT strings and bounding-box
// coordinates are user data, so they must move through the parameter list
// exactly like scalars. A WKT string is the interesting case because it is
// free text that ends up inside a PostGIS constructor.
func FuzzSpatial(f *testing.F) {
	built := build(f)
	compiler := compile.New(built)

	const query = `query ($wkt: GeoJSON!, $x: Float!, $d: Float!) {
		allPlaces(filter: {
			location: {
				intersects: $wkt
				bboxIntersects: {minX: $x, minY: $x, maxX: $x, maxY: $x}
				dwithin: {geometry: $wkt, distance: $d}
				bboxLeftOf: $wkt
				bboxIntersectsND: $wkt
			}
		}) { nodes { id } }
	}`
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		f.Fatal(perr)
	}
	if errs := validator.ValidateWithRules(built.Schema, doc, nil); len(errs) > 0 {
		f.Fatal(errs)
	}
	op := doc.Operations[0]

	compileWith := func(t testing.TB, wkt string, x, d float64) (*compile.Statement, bool) {
		t.Helper()
		vars, verr := validator.VariableValues(built.Schema, op, map[string]any{
			"wkt": wkt, "x": x, "d": d,
		})
		if verr != nil {
			return nil, false
		}
		stmt, err := compiler.Compile(context.Background(), &compile.Request{
			Field:    op.SelectionSet[0].(*ast.Field),
			Vars:     vars,
			Built:    built,
			MaxDepth: 15,
		})
		if err != nil {
			// Malformed geometry is rejected at compile time by design; only a
			// successful compile is subject to the invariant.
			return nil, false
		}
		return stmt, true
	}

	baseline, ok := compileWith(f, "POINT(1 2)", 1.5, 100)
	if !ok {
		f.Fatal("baseline spatial query failed to compile")
	}

	f.Add("POINT(1 2)", 1.5, 100.0)
	f.Add("SRID=4326;POLYGON((0 0,1 0,1 1,0 0))", -0.5, 0.0)
	f.Add(`POINT(1 2)'); DROP TABLE places; --`, 0.0, 1.0)
	f.Add(`'; DROP TABLE places; --`, 1e30, -1.0)
	f.Add("", 0.0, 0.0)

	f.Fuzz(func(t *testing.T, wkt string, x, d float64) {
		stmt, ok := compileWith(t, wkt, x, d)
		if !ok {
			return
		}
		if stmt.SQL != baseline.SQL {
			t.Fatalf("spatial SQL text varied with input values:\n--- baseline ---\n%s\n--- got ---\n%s", baseline.SQL, stmt.SQL)
		}
		if len(stmt.Args) != len(baseline.Args) {
			t.Fatalf("parameter count varied: %d != %d", len(stmt.Args), len(baseline.Args))
		}
	})
}
