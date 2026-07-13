package compile_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

var update = flag.Bool("update", false, "rewrite golden files")

// build compiles the fixture catalog into a Built schema with default naming.
func build(t testing.TB) *schema.Built {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatalf("build schema: %v", err)
	}
	return built
}

// TestGolden compiles each testdata/*.graphql document and compares the SQL
// (plus parameters) against the .sql golden next to it. Run with -update to
// regenerate.
func TestGolden(t *testing.T) {
	built := build(t)
	compiler := compile.New(built)

	files, err := filepath.Glob(filepath.Join("testdata", "*.graphql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no golden inputs found: %v", err)
	}
	for _, file := range files {
		name := strings.TrimSuffix(filepath.Base(file), ".graphql")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			query, vars := splitVariables(t, string(src))

			doc, perr := parser.ParseQuery(&ast.Source{Name: name, Input: query})
			if perr != nil {
				t.Fatalf("parse: %v", perr)
			}
			if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
				t.Fatalf("validate: %v", errs)
			}
			op := doc.Operations[0]
			coerced, verr := validator.VariableValues(built.Schema, op, vars)
			if verr != nil {
				t.Fatalf("variables: %v", verr)
			}

			var out strings.Builder
			for _, sel := range op.SelectionSet {
				f, ok := sel.(*ast.Field)
				if !ok {
					continue
				}
				stmt, err := compiler.Compile(context.Background(), &compile.Request{
					Field:     f,
					Fragments: doc.Fragments,
					Vars:      coerced,
					Built:     built,
					MaxDepth:  15,
				})
				if err != nil {
					fmt.Fprintf(&out, "-- field: %s\n-- error: %v\n\n", f.Name, err)
					continue
				}
				argsJSON, _ := json.Marshal(stmt.Args)
				fmt.Fprintf(&out, "-- field: %s\n%s\n-- args: %s\n\n", f.Name, stmt.SQL, argsJSON)
			}

			goldenPath := filepath.Join("testdata", name+".sql")
			if *update {
				if err := os.WriteFile(goldenPath, []byte(out.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden file %s (run with -update): %v", goldenPath, err)
			}
			if string(want) != out.String() {
				t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", name, want, out.String())
			}
		})
	}
}

// splitVariables extracts an optional leading `# variables: {...}` comment.
func splitVariables(t *testing.T, src string) (string, map[string]any) {
	t.Helper()
	vars := map[string]any{}
	for _, line := range strings.Split(src, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "# variables:"); ok {
			if err := json.Unmarshal([]byte(strings.TrimSpace(rest)), &vars); err != nil {
				t.Fatalf("bad variables comment: %v", err)
			}
		}
	}
	return src, vars
}
