package exec_test

import (
	"context"
	"strings"
	"testing"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// suggestionProbes are invalid documents whose default validation errors
// name schema elements that the document does not mention.
var suggestionProbes = []struct {
	name  string
	query string
	leaks []string
}{
	{"root field", `mutation { createUs }`, []string{`"createUser"`}},
	{"nested field", `{ allUsers { nodes { emai } } }`, []string{`"email"`}},
	{"field on interface", `{ node(nodeId: "x") { email } }`, []string{`"User"`}},
	{"argument", `{ allUsers(firs: 1) { totalCount } }`, []string{`"first"`}},
	{"fragment type", `{ ...f } fragment f on Usr { id }`, []string{`"User"`}},
	{"enum value", `{ allUsers(filter: {mood: {equalTo: HAPP}}) { totalCount } }`, []string{`"HAPPY"`}},
	{"input field", `{ allUsers(filter: {emai: {equalTo: "x"}}) { totalCount } }`, []string{`"email"`}},
	{"missing selection", `{ allUsers }`, []string{"Did you mean"}},
}

func newSuggestionExecutor(t *testing.T, disableIntrospection bool, cacheSize int) *exec.Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	return exec.New(nil, built, compile.New(built).Compile, nil, exec.Options{
		DisableIntrospection: disableIntrospection,
		DocumentCacheSize:    cacheSize,
	})
}

func validationErrors(t *testing.T, e *exec.Executor, query string) string {
	t.Helper()
	res := e.Execute(context.Background(), exec.Request{Query: query})
	if len(res.Errors) == 0 {
		t.Fatalf("query %q: no errors", query)
	}
	msgs := make([]string, len(res.Errors))
	for i, e := range res.Errors {
		msgs[i] = e.Message
	}
	return strings.Join(msgs, "\n")
}

func TestValidationSuggestionsWithIntrospection(t *testing.T) {
	e := newSuggestionExecutor(t, false, 0)
	for _, p := range suggestionProbes {
		got := validationErrors(t, e, p.query)
		for _, leak := range p.leaks {
			if !strings.Contains(got, leak) {
				t.Errorf("%s: errors %s do not suggest %s", p.name, got, leak)
			}
		}
	}
}

func TestDisableIntrospectionHidesSuggestions(t *testing.T) {
	for _, cacheSize := range []int{0, 100} {
		e := newSuggestionExecutor(t, true, cacheSize)
		for _, p := range suggestionProbes {
			for range 2 {
				got := validationErrors(t, e, p.query)
				if strings.Contains(got, "Did you mean") {
					t.Errorf("cache %d, %s: errors carry a suggestion: %s", cacheSize, p.name, got)
				}
				for _, leak := range p.leaks {
					if strings.Contains(got, leak) {
						t.Errorf("cache %d, %s: errors name %s: %s", cacheSize, p.name, leak, got)
					}
				}
			}
		}
	}
}
