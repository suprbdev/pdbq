package exec

import (
	"testing"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// cost parses+validates query against the fixture schema and returns the
// measured cost with a page cap of 100.
func cost(t *testing.T, query string, rawVars map[string]any) int {
	t.Helper()
	return costWith(t, testutil.FixtureCatalog(), query, rawVars)
}

// costWith is cost against a caller-supplied catalog, so tests can set
// database COMMENTs (e.g. @costMultiplier) before the schema is built.
func costWith(t *testing.T, cat *introspect.Catalog, query string, rawVars map[string]any) int {
	t.Helper()
	b := schema.New(cat, inflect.Default, schema.Options{
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
	_, c := measure(op.SelectionSet, doc.Fragments, vars, 100, 1, built.Meta)
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

// withReplyCostMultiplier returns the fixture catalog with the comment on the
// self-referential comments.parent_id FK replaced by tag (the shared fixture
// ships it as "@costMultiplier 3"; "" gives the untagged baseline).
func withReplyCostMultiplier(tag string) *introspect.Catalog {
	cat := testutil.FixtureCatalog()
	for _, fk := range cat.Table("public", "comments").ForeignKeys {
		if fk.Name == "comments_parent_id_fkey" {
			fk.Comment = tag
		}
	}
	return cat
}

// threadQuery is a 4-level comment thread, the shape that makes the default
// page^depth estimate explode.
const threadQuery = `{
  allComments(first: 20) { nodes { id
    commentsByParentId(first: 20) { nodes { id
      commentsByParentId(first: 20) { nodes { id
        commentsByParentId(first: 20) { nodes { id } } } } } } } }
}`

func TestCostMultiplierLowersRecursiveEstimate(t *testing.T) {
	plain := costWith(t, withReplyCostMultiplier(""), threadQuery, nil)
	tagged := costWith(t, withReplyCostMultiplier("@costMultiplier 3"), threadQuery, nil)
	if tagged >= plain {
		t.Fatalf("@costMultiplier did not lower cost: tagged=%d plain=%d", tagged, plain)
	}
	// The point of the tag is that a deep thread fits under a sane max_cost.
	if tagged > 10000 {
		t.Errorf("tagged cost %d still exceeds the default max_cost 10000", tagged)
	}
	if plain <= 10000 {
		t.Errorf("untagged cost %d unexpectedly fits the default limit; fixture no longer exercises the blowup", plain)
	}
}

func TestCostMultiplierNeverRaisesCost(t *testing.T) {
	// A query asking for fewer rows than the declared average fan-out must be
	// charged the smaller requested page, not the average.
	const small = `{ allComments(first: 20) { nodes { id
	    commentsByParentId(first: 2) { nodes { id } } } } }`
	plain := costWith(t, withReplyCostMultiplier(""), small, nil)
	tagged := costWith(t, withReplyCostMultiplier("@costMultiplier 10"), small, nil)
	if tagged != plain {
		t.Fatalf("@costMultiplier raised cost above the requested page: tagged=%d plain=%d", tagged, plain)
	}
}

func TestCostMultiplierIgnoresInvalid(t *testing.T) {
	plain := costWith(t, withReplyCostMultiplier(""), threadQuery, nil)
	for _, tag := range []string{"@costMultiplier 0", "@costMultiplier -5", "@costMultiplier abc", "@costMultiplier"} {
		if got := costWith(t, withReplyCostMultiplier(tag), threadQuery, nil); got != plain {
			t.Errorf("tag %q changed cost to %d, want unchanged %d", tag, got, plain)
		}
	}
}
