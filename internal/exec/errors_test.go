package exec_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// newErrorExecutor builds an executor over the fixture schema with a nil
// pool; compile errors surface before anything touches the database.
func newErrorExecutor(t *testing.T, compileFn compile.Func, dev bool) *exec.Executor {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true,
		Functions:         true,
	})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if compileFn == nil {
		compileFn = compile.New(built).Compile
	}
	return exec.New(nil, built, compileFn, nil, exec.Options{DevErrors: dev})
}

func TestProdPassesThroughUserSafeCompileErrors(t *testing.T) {
	e := newErrorExecutor(t, nil, false)
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers(first: -1) { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "first must be >= 0") {
		t.Fatalf("error = %q, want pass-through validation message", res.Errors[0].Message)
	}
}

func TestProdSanitizesInternalCompileErrors(t *testing.T) {
	leaky := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, fmt.Errorf("compile: filter column %q not found", "secret_salary")
	}
	e := newErrorExecutor(t, leaky, false)
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if got := res.Errors[0].Message; got != "compile: invalid query" {
		t.Fatalf("error = %q, want sanitized %q", got, "compile: invalid query")
	}
}

func TestProdSanitizesNonCompileErrors(t *testing.T) {
	boom := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, fmt.Errorf("pool: connection refused to host db.internal")
	}
	e := newErrorExecutor(t, boom, false)
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if got := res.Errors[0].Message; got != "internal error" {
		t.Fatalf("error = %q, want %q", got, "internal error")
	}
}

func TestDevPassesThroughInternalCompileErrors(t *testing.T) {
	leaky := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, fmt.Errorf("compile: filter column %q not found", "secret_salary")
	}
	e := newErrorExecutor(t, leaky, true)
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "secret_salary") {
		t.Fatalf("error = %q, want full detail in dev mode", res.Errors[0].Message)
	}
}
