package exec

import (
	"context"
	"errors"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// TestCompileSeesOperationContext proves a compile.Func (the surface
// CompileHook plugins wrap) can reach the operation and claims via context.
func TestCompileSeesOperationContext(t *testing.T) {
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	var seen *Operation
	stop := errors.New("stop before touching the database")
	compileFn := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		seen = OperationFromContext(ctx)
		return nil, stop
	}
	ex := New(nil, built, compileFn, nil, Options{})
	res := ex.Execute(context.Background(), Request{
		Query:  `{ allUsers { nodes { id } } }`,
		Claims: map[string]any{"user_id": "42"},
		Role:   "app_user",
	})
	if len(res.Errors) == 0 {
		t.Fatal("expected the sentinel compile error")
	}
	if seen == nil {
		t.Fatal("compile func did not see the operation in its context")
	}
	if seen.Role != "app_user" {
		t.Fatalf("Role = %q, want app_user", seen.Role)
	}
	if got := ClaimsFromContext(WithOperation(context.Background(), seen)); got["user_id"] != "42" {
		t.Fatalf("claims = %v, want user_id=42", got)
	}
}

func TestOperationFromContextEmpty(t *testing.T) {
	if op := OperationFromContext(context.Background()); op != nil {
		t.Fatalf("expected nil, got %v", op)
	}
	if c := ClaimsFromContext(context.Background()); c != nil {
		t.Fatalf("expected nil claims, got %v", c)
	}
}
