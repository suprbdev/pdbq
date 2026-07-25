package exec_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

const readOnlyMutation = `mutation {
  createUser(input: {email: "a@b.c"}) { user { id } }
}`

// newReadOnlyExecutor builds an executor over the fixture schema with a nil
// pool: the gate must reject mutations before anything touches the database.
func newReadOnlyExecutor(t *testing.T, ro *atomic.Bool) *exec.Executor {
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
	return exec.New(nil, built, c.Compile, nil, exec.Options{ReadOnly: ro})
}

func TestReadOnlyRejectsMutation(t *testing.T) {
	var ro atomic.Bool
	ro.Store(true)
	e := newReadOnlyExecutor(t, &ro)
	res := e.Execute(context.Background(), exec.Request{Query: readOnlyMutation})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "read-only") {
		t.Fatalf("error = %q, want read-only message", res.Errors[0].Message)
	}
}

func TestReadOnlyAllowsQueries(t *testing.T) {
	var ro atomic.Bool
	ro.Store(true)
	e := newReadOnlyExecutor(t, &ro)
	// Introspection resolves in memory, proving reads pass the gate without
	// needing a database.
	res := e.Execute(context.Background(), exec.Request{Query: `{ __typename }`})
	if len(res.Errors) > 0 {
		t.Fatalf("query errored under read-only mode: %v", res.Errors)
	}
	if string(res.Data["__typename"]) != `"Query"` {
		t.Fatalf("__typename = %s, want \"Query\"", res.Data["__typename"])
	}
}

func TestReadOnlyToggleAndNilPointer(t *testing.T) {
	// Flipping the shared flag off lets the same executor pass the gate again:
	// with a nil pool the mutation now proceeds far enough to panic on the
	// database call, which is the proof it was not rejected.
	var ro atomic.Bool
	ro.Store(true)
	e := newReadOnlyExecutor(t, &ro)
	if res := e.Execute(context.Background(), exec.Request{Query: readOnlyMutation}); len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want rejection while read-only", res.Errors)
	}
	ro.Store(false)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("mutation did not reach the database layer after toggle off")
			}
		}()
		e.Execute(context.Background(), exec.Request{Query: readOnlyMutation})
	}()

	// A nil ReadOnly pointer means the gate is simply absent.
	e = newReadOnlyExecutor(t, nil)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("mutation did not reach the database layer with nil ReadOnly")
			}
		}()
		e.Execute(context.Background(), exec.Request{Query: readOnlyMutation})
	}()
}
