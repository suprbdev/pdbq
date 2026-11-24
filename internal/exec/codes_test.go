package exec_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/errcode"
	"github.com/suprbdev/pdbq/internal/exec"
)

// rejectHook refuses every operation with err.
type rejectHook struct{ err error }

func (h rejectHook) BeforeOperation(ctx context.Context, _ *exec.Operation) (context.Context, error) {
	return ctx, h.err
}
func (rejectHook) AfterOperation(context.Context, *exec.Operation, *exec.Result) {}

func failing(err error) compile.Func {
	return func(context.Context, *compile.Request) (*compile.Statement, error) { return nil, err }
}

// TestErrorCodes asserts the extensions.code of every request-level and
// compile-time error the executor produces without a database.
func TestErrorCodes(t *testing.T) {
	ro := &atomic.Bool{}
	ro.Store(true)
	deadline, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name    string
		opts    exec.Options
		compile compile.Func
		hooks   []exec.RequestHook
		ctx     context.Context
		req     exec.Request
		code    errcode.Code
		msg     string
	}{
		{name: "parse", req: exec.Request{Query: `{ allUsers {`}, code: errcode.GraphQLParseFailed},
		{name: "validation", req: exec.Request{Query: `{ nope }`}, code: errcode.GraphQLValidationFailed},
		{name: "variables", req: exec.Request{Query: `query($n: Int) { allUsers(first: $n) { nodes { id } } }`, Variables: map[string]any{"n": "x"}}, code: errcode.GraphQLValidationFailed},
		{name: "unknown operation", req: exec.Request{Query: `query A { __typename }`, OperationName: "B"}, code: errcode.InvalidRequest},
		{name: "tokens", opts: exec.Options{MaxQueryTokens: 5}, req: exec.Request{Query: `{ allUsers { nodes { id email } } }`}, code: errcode.DocumentLimitExceeded, msg: "token limit"},
		{name: "input depth", opts: exec.Options{MaxInputDepth: 2}, req: exec.Request{Query: `{ allUsers(filter: {and: [{and: [{id: {equalTo: 1}}]}]}) { nodes { id } } }`}, code: errcode.DocumentLimitExceeded},
		{name: "variable depth", opts: exec.Options{MaxInputDepth: 2}, req: exec.Request{Query: `query($f: UserFilter) { allUsers(filter: $f) { nodes { id } } }`, Variables: map[string]any{"f": map[string]any{"and": []any{map[string]any{"and": []any{map[string]any{}}}}}}}, code: errcode.DocumentLimitExceeded},
		{name: "selections", opts: exec.Options{MaxSelections: 3}, req: exec.Request{Query: `{ a: __typename b: __typename c: __typename d: __typename }`}, code: errcode.DocumentLimitExceeded},
		{name: "depth", opts: exec.Options{MaxDepth: 2}, req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.DepthExceeded},
		{name: "cost", opts: exec.Options{MaxCost: 2}, req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.CostExceeded},
		{name: "page size", opts: exec.Options{MaxPageSize: 10}, req: exec.Request{Query: `{ allUsers(first: 11) { nodes { id } } }`}, code: errcode.PageSizeExceeded},
		{name: "cursor", req: exec.Request{Query: `{ allUsers(after: "bm9wZQ==") { nodes { id } } }`}, code: errcode.InvalidCursor},
		{name: "argument", req: exec.Request{Query: `{ allUsers(first: -1) { nodes { id } } }`}, code: errcode.InvalidArgument},
		{name: "introspection disabled", opts: exec.Options{DisableIntrospection: true}, req: exec.Request{Query: `{ __schema { queryType { name } } }`}, code: errcode.IntrospectionDisabled},
		{name: "introspection bytes", opts: exec.Options{MaxIntrospectionBytes: 64}, req: exec.Request{Query: exec.IntrospectionQuery}, code: errcode.IntrospectionLimitExceeded},
		{name: "read only", opts: exec.Options{ReadOnly: ro}, req: exec.Request{Query: `mutation { __typename }`}, code: errcode.ReadOnly},
		{name: "partial disabled", req: exec.Request{Query: `mutation { __typename }`, PartialMutations: true}, code: errcode.PartialMutationsDisabled},
		{name: "partial max items", opts: exec.Options{PartialMutations: true, PartialMaxItems: 1}, req: exec.Request{Query: `mutation { a: __typename b: __typename }`, PartialMutations: true}, code: errcode.DocumentLimitExceeded},
		{name: "subscription over http", req: exec.Request{Query: `subscription { __typename }`}, code: errcode.GraphQLValidationFailed},
		{name: "hook", hooks: []exec.RequestHook{rejectHook{errors.New("not on the allowlist")}}, req: exec.Request{Query: `{ __typename }`}, code: errcode.OperationRejected, msg: "not on the allowlist"},
		{name: "hook code", hooks: []exec.RequestHook{rejectHook{errcode.Errorf(errcode.AuthRequired, "log in")}}, req: exec.Request{Query: `{ __typename }`}, code: errcode.AuthRequired},
		{name: "hook user error", hooks: []exec.RequestHook{rejectHook{compile.UserErrorCodef(errcode.CostExceeded, "too dear")}}, req: exec.Request{Query: `{ __typename }`}, code: errcode.CostExceeded},
		{name: "cancelled", ctx: deadline, req: exec.Request{Query: `{ __typename }`}, code: errcode.Internal},
		{name: "pg", compile: failing(&pgconn.PgError{Code: "23505", Message: "dup"}), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.PG("23505")},
		{name: "nested not found", compile: failing(&pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type boolean: "nested_mutations: team.connectBySlug matched 0 rows"`}), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.NotFound, msg: "nested_mutations: team.connectBySlug matched 0 rows"},
		{name: "other 22P02", compile: failing(&pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type boolean: "x"`}), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.PG("22P02")},
		{name: "deadline", compile: failing(fmt.Errorf("query: %w", context.DeadlineExceeded)), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.Timeout, msg: "request timeout"},
		{name: "connection", compile: failing(fmt.Errorf("dial: %w", syscall.ECONNREFUSED)), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.Unavailable, msg: "database unavailable"},
		{name: "eof", compile: failing(fmt.Errorf("read: %w", io.ErrUnexpectedEOF)), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.Unavailable},
		{name: "internal", compile: failing(errors.New("boom")), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.Internal, msg: "internal error"},
		{name: "go-side gqlerror", compile: failing(gqlerror.Errorf("plain")), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.Internal},
		{name: "nested depth", compile: failing(compile.UserErrorCodef(errcode.DepthExceeded, "nested_mutations: nesting depth exceeds limit 3")), req: exec.Request{Query: `{ allUsers { nodes { id } } }`}, code: errcode.DepthExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newErrorExecutor(t, tc.compile, tc.opts)
			e.Hooks = tc.hooks
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			res := e.Execute(ctx, tc.req)
			if len(res.Errors) == 0 {
				t.Fatalf("no error, data %v", res.Data)
			}
			got := res.Errors[0]
			if errcode.Of(got) != tc.code {
				t.Errorf("code = %q, want %q (%s)", errcode.Of(got), tc.code, got.Message)
			}
			if tc.msg != "" && !strings.Contains(got.Message, tc.msg) {
				t.Errorf("message = %q, want %q", got.Message, tc.msg)
			}
		})
	}
}

func TestErrorListAlwaysCarriesCode(t *testing.T) {
	for _, err := range []error{
		errors.New("x"),
		gqlerror.Errorf("x"),
		gqlerror.List{gqlerror.Errorf("x"), errcode.Errorf(errcode.ReadOnly, "y")},
		context.DeadlineExceeded,
	} {
		for _, gerr := range exec.ErrorList(err) {
			if errcode.Of(gerr) == "" {
				t.Errorf("%v: error %q has no code", err, gerr.Message)
			}
		}
	}
	if got := errcode.Of(exec.ErrorList(context.DeadlineExceeded)[0]); got != errcode.Timeout {
		t.Errorf("deadline code = %q", got)
	}
	if got := errcode.Of(exec.ErrorList(gqlerror.List{errcode.Errorf(errcode.ReadOnly, "y")})[0]); got != errcode.ReadOnly {
		t.Errorf("kept code = %q", got)
	}
}
