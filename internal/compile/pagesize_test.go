package compile_test

import (
	"context"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
)

// compileOne compiles the first root field of query against the fixture
// schema with the given page-size cap (0 = compiler default).
func compileOne(t *testing.T, query string, maxPageSize int) (*compile.Statement, error) {
	t.Helper()
	built := build(t)
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	op := doc.Operations[0]
	f := op.SelectionSet[0].(*ast.Field)
	return compile.New(built).Compile(context.Background(), &compile.Request{
		Field:       f,
		Fragments:   doc.Fragments,
		Vars:        map[string]any{},
		Built:       built,
		MaxDepth:    15,
		MaxPageSize: maxPageSize,
	})
}

func hasArg(stmt *compile.Statement, want int) bool {
	for _, a := range stmt.Args {
		if n, ok := a.(int); ok && n == want {
			return true
		}
	}
	return false
}

func TestNegativePaginationRejected(t *testing.T) {
	cases := map[string]string{
		"first":  `{ allUsers(first: -1) { nodes { id } } }`,
		"last":   `{ allUsers(last: -1) { nodes { id } } }`,
		"offset": `{ allUsers(offset: -1) { nodes { id } } }`,
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := compileOne(t, q, 100); err == nil {
				t.Fatalf("negative %s accepted; want compile error", name)
			}
		})
	}
}

func TestFirstClampedToMaxPageSize(t *testing.T) {
	stmt, err := compileOne(t, `{ allUsers(first: 100000) { nodes { id } } }`, 100)
	if err != nil {
		t.Fatal(err)
	}
	// Keyset mode scans cap+1 rows to detect hasNextPage and trims to cap.
	if !hasArg(stmt, 101) || !hasArg(stmt, 100) {
		t.Fatalf("first=100000 not clamped to cap 100; args=%v", stmt.Args)
	}
}

func TestMissingFirstDefaultsToMaxPageSize(t *testing.T) {
	stmt, err := compileOne(t, `{ allUsers { nodes { id } } }`, 25)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stmt.SQL, "LIMIT ") {
		t.Fatalf("no LIMIT emitted without first; sql=\n%s", stmt.SQL)
	}
	if !hasArg(stmt, 26) || !hasArg(stmt, 25) {
		t.Fatalf("default limit not the cap 25; args=%v", stmt.Args)
	}
}

func TestZeroMaxPageSizeFallsBackToDefault(t *testing.T) {
	stmt, err := compileOne(t, `{ allUsers { nodes { id } } }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(stmt, 101) || !hasArg(stmt, 100) {
		t.Fatalf("compiler default cap 100 not applied; args=%v", stmt.Args)
	}
}

func TestFirstUnderCapUnchanged(t *testing.T) {
	stmt, err := compileOne(t, `{ allUsers(first: 5) { nodes { id } } }`, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !hasArg(stmt, 6) || !hasArg(stmt, 5) {
		t.Fatalf("first=5 altered; args=%v", stmt.Args)
	}
}
