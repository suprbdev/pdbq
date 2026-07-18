package exec

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// cost parses+validates query against the fixture schema and returns the
// measured cost with a page cap of 100.
func cost(t *testing.T, query string, rawVars map[string]any) int {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	doc, perr := parser.ParseQuery(&ast.Source{Input: query})
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	if errs := validator.Validate(built.Schema, doc); len(errs) > 0 {
		t.Fatalf("validate: %v", errs)
	}
	op := doc.Operations[0]
	vars, verr := validator.VariableValues(built.Schema, op, rawVars)
	if verr != nil {
		t.Fatalf("variables: %v", verr)
	}
	_, c := measure(op.SelectionSet, doc.Fragments, vars, 100, 1)
	return c
}

func TestCostScalesWithFirst(t *testing.T) {
	small := cost(t, `{ allUsers(first: 1) { nodes { id } } }`, nil)
	big := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	if big <= small {
		t.Fatalf("cost(first:100)=%d not > cost(first:1)=%d", big, small)
	}
}

func TestCostClampedToPageCap(t *testing.T) {
	capped := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	huge := cost(t, `{ allUsers(first: 100000) { nodes { id } } }`, nil)
	if huge != capped {
		t.Fatalf("cost(first:100000)=%d != cost(first:100)=%d; page cap not applied", huge, capped)
	}
}

func TestCostResolvesVariableFirst(t *testing.T) {
	literal := cost(t, `{ allUsers(first: 50) { nodes { id } } }`, nil)
	variable := cost(t, `query($n: Int) { allUsers(first: $n) { nodes { id } } }`,
		map[string]any{"n": 50})
	if variable != literal {
		t.Fatalf("variable first cost %d != literal cost %d", variable, literal)
	}
}

func TestCostFirstOneCheaperThanUnpaginated(t *testing.T) {
	def := cost(t, `{ allUsers(first: 10) { nodes { postsByAuthorId { nodes { id } } } } }`, nil)
	one := cost(t, `{ allUsers(first: 10) { nodes { postsByAuthorId(first: 1) { nodes { id } } } } }`, nil)
	if one >= def {
		t.Fatalf("cost(first:1)=%d not < cost(no first)=%d; explicit small page must not cost more", one, def)
	}
}

func TestCostPaginationNotChargedTwice(t *testing.T) {
	// The page multiplier applies once at the connection field; nodes under
	// it must not add the ×10 list default on top. An explicit first equal
	// to the default-page assumption (10) therefore costs the same as no
	// first at all.
	def := cost(t, `{ allUsers { nodes { id } } }`, nil)
	ten := cost(t, `{ allUsers(first: 10) { nodes { id } } }`, nil)
	if ten != def {
		t.Fatalf("cost(first:10)=%d != cost(no first)=%d; pagination charged twice", ten, def)
	}
}

func TestCostNestedPaginationCompounds(t *testing.T) {
	flat := cost(t, `{ allUsers(first: 100) { nodes { id } } }`, nil)
	nested := cost(t, `{ allUsers(first: 100) { nodes { postsByAuthorId(first: 100) { nodes { id } } } } }`, nil)
	if nested < flat*50 {
		t.Fatalf("nested pagination cost %d does not compound over flat %d", nested, flat)
	}
}
