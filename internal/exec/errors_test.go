package exec_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/testutil"
)

// newErrorExecutor builds an executor over the fixture schema with a nil
// pool; compile errors surface before anything touches the database.
func newErrorExecutor(t *testing.T, compileFn compile.Func, opts exec.Options) *exec.Executor {
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
	return exec.New(nil, built, compileFn, nil, opts)
}

func TestProdPassesThroughUserSafeCompileErrors(t *testing.T) {
	e := newErrorExecutor(t, nil, exec.Options{})
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
	e := newErrorExecutor(t, leaky, exec.Options{})
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
	e := newErrorExecutor(t, boom, exec.Options{})
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if got := res.Errors[0].Message; got != "internal error" {
		t.Fatalf("error = %q, want %q", got, "internal error")
	}
}

// uniqueViolation mimics the PG error for a duplicate key: the message embeds
// the constraint name, which prod passes through and strict must hide.
func uniqueViolation() compile.Func {
	return func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, &pgconn.PgError{
			Code:           "23505",
			Message:        `duplicate key value violates unique constraint "users_email_key"`,
			ConstraintName: "users_email_key",
		}
	}
}

func TestProdPassesThroughConstraintViolationMessage(t *testing.T) {
	e := newErrorExecutor(t, uniqueViolation(), exec.Options{})
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "users_email_key") {
		t.Fatalf("error = %q, want constraint message pass-through in prod", res.Errors[0].Message)
	}
}

func TestStrictHidesConstraintViolationMessage(t *testing.T) {
	e := newErrorExecutor(t, uniqueViolation(), exec.Options{StrictErrors: true})
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if got := res.Errors[0].Message; got != "constraint violation" {
		t.Fatalf("error = %q, want %q", got, "constraint violation")
	}
}

func TestStrictHidesDataExceptionMessage(t *testing.T) {
	invalid := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "abc" in column "salary"`}
	}
	e := newErrorExecutor(t, invalid, exec.Options{StrictErrors: true})
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if got := res.Errors[0].Message; got != "invalid input value" {
		t.Fatalf("error = %q, want %q", got, "invalid input value")
	}
}

func TestDevPassesThroughInternalCompileErrors(t *testing.T) {
	leaky := func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
		return nil, fmt.Errorf("compile: filter column %q not found", "secret_salary")
	}
	e := newErrorExecutor(t, leaky, exec.Options{DevErrors: true})
	res := e.Execute(context.Background(), exec.Request{Query: `{ allUsers { nodes { id } } }`})
	if len(res.Errors) != 1 {
		t.Fatalf("errors = %v, want exactly one", res.Errors)
	}
	if !strings.Contains(res.Errors[0].Message, "secret_salary") {
		t.Fatalf("error = %q, want full detail in dev mode", res.Errors[0].Message)
	}
}
