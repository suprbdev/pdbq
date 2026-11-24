package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// outcome is what the fake server answers for one statement: a result row,
// no row, or a statement error.
type outcome struct {
	raw   string
	noRow bool
	err   error
}

// batchCall is one recorded pgexec.QueryTx call.
type batchCall struct {
	opts pgexec.TxOptions
	id   pgexec.Identity
	sql  []string
}

// fakeBatch stands in for pgexec.QueryTx. attempts[n] scripts call n: the
// outcome of each statement by its SQL, and errors before the statements
// (begin/identity) or after them (commit).
type fakeBatch struct {
	attempts []attempt
	calls    []batchCall
}

type attempt struct {
	before, after error
	stmts         map[string]outcome
}

type fakeRow struct{ o outcome }

func (r fakeRow) Scan(dest ...any) error {
	switch {
	case r.o.err != nil:
		return r.o.err
	case r.o.noRow:
		return pgx.ErrNoRows
	}
	*dest[0].(*[]byte) = []byte(r.o.raw)
	return nil
}

func (f *fakeBatch) queryTx(_ context.Context, _ pgexec.ConnAcquirer, opts pgexec.TxOptions, id pgexec.Identity, stmts ...pgexec.Stmt) error {
	call := batchCall{opts: opts, id: id}
	for _, s := range stmts {
		call.sql = append(call.sql, s.SQL)
	}
	f.calls = append(f.calls, call)
	a := f.attempts[len(f.calls)-1]
	if a.before != nil {
		return a.before
	}
	for _, s := range stmts {
		o, ok := a.stmts[s.SQL]
		if !ok {
			return fmt.Errorf("unscripted statement %q", s.SQL)
		}
		if err := s.Scan(fakeRow{o}); err != nil {
			return err
		}
		if o.err != nil {
			return o.err
		}
	}
	return a.after
}

// fakeCompile compiles every root field to its alias as SQL and fails the
// fields whose alias starts with "bad".
func fakeCompile(_ context.Context, req *compile.Request) (*compile.Statement, error) {
	if strings.HasPrefix(req.Field.Alias, "bad") {
		return nil, errors.New("bad field")
	}
	return &compile.Statement{SQL: req.Field.Alias, Mutation: req.Field.ObjectDefinition.Name == "Mutation"}, nil
}

func newBatchExecutor(t *testing.T, opts Options, f *fakeBatch) *Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Functions: true}).Build()
	if err != nil {
		t.Fatal(err)
	}
	e := New(nil, built, fakeCompile, nil, opts)
	e.queryTx = f.queryTx
	return e
}

func resultJSON(t *testing.T, res *Result) string {
	t.Helper()
	b, err := res.AppendJSON(nil)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var (
	pgFailure     = &pgconn.PgError{Code: "22012", Message: "division by zero"}
	pgSerializing = &pgconn.PgError{Code: "40001", Message: "could not serialize access"}
)

func TestBatchTxSingleField(t *testing.T) {
	for _, tc := range []struct {
		name string
		iso  Options
		want pgx.TxIsoLevel
	}{
		{"read committed", Options{}, ""},
		{"repeatable read", Options{Isolation: pgx.RepeatableRead}, pgx.RepeatableRead},
		{"serializable", Options{Isolation: pgx.Serializable}, pgx.Serializable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"a": {raw: `{"id":1}`}}}}}
			opts := tc.iso
			opts.ClaimsPrefix, opts.ClaimsJSONSetting, opts.TxRetries = "app.claims", "request.jwt.claims", 3
			e := newBatchExecutor(t, opts, f)
			res := e.Execute(context.Background(), Request{
				Query:  `{ a: userById(id: 1) { id } }`,
				Role:   "app_user",
				Claims: map[string]any{"user_id": 7},
			})
			if got, want := resultJSON(t, res), `{"data":{"a":{"id":1}}}`; got != want {
				t.Fatalf("response = %s, want %s", got, want)
			}
			if len(f.calls) != 1 {
				t.Fatalf("QueryTx calls = %d, want 1", len(f.calls))
			}
			c := f.calls[0]
			if c.opts.Isolation != tc.want || !c.opts.RLS || c.opts.ScanBeforeCommit || c.opts.MaxRetries != 0 {
				t.Errorf("opts = %+v, want isolation %q, RLS, no ScanBeforeCommit, retries done by the executor", c.opts, tc.want)
			}
			if c.id.Role != "app_user" || c.id.Claims["user_id"] != 7 || c.id.ClaimsPrefix != "app.claims" || c.id.ClaimsJSONSetting != "request.jwt.claims" {
				t.Errorf("identity = %+v", c.id)
			}
			if strings.Join(c.sql, ",") != "a" {
				t.Errorf("statements = %v, want [a]", c.sql)
			}
		})
	}
}

func TestBatchTxOnlyWhenTransactional(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{}}}
	e := newBatchExecutor(t, Options{ClaimsPrefix: "jwt.claims"}, f)
	res := e.Execute(context.Background(), Request{Query: `{ __typename }`})
	if got, want := resultJSON(t, res), `{"data":{"__typename":"Query"}}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	if len(f.calls) != 0 {
		t.Fatalf("QueryTx called without an identity or transaction policy: %+v", f.calls)
	}

	res = e.Execute(context.Background(), Request{Query: `{ __typename }`, Role: "app_user"})
	if got, want := resultJSON(t, res), `{"data":{"__typename":"Query"}}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	if len(f.calls) != 1 || len(f.calls[0].sql) != 0 {
		t.Fatalf("calls = %+v, want one batch without statements (the identity alone)", f.calls)
	}
}

func TestBatchTxAlwaysWithClaimsJSONSetting(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{}}}
	e := newBatchExecutor(t, Options{ClaimsJSONSetting: "request.jwt.claims"}, f)
	res := e.Execute(context.Background(), Request{Query: `{ __typename }`})
	if got, want := resultJSON(t, res), `{"data":{"__typename":"Query"}}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	if len(f.calls) != 1 || !f.calls[0].opts.RLS || f.calls[0].id.ClaimsJSONSetting != "request.jwt.claims" {
		t.Fatalf("calls = %+v, want one batch setting the JSON claims", f.calls)
	}
}

// TestBatchTxQueryFieldErrors reruns a query whose batch a field's database
// error cut short with each field in its own savepoint: the failing field
// reports its own error, the fields after it their values, and the response
// has no error without a path.
func TestBatchTxQueryFieldErrors(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{
		"a": {raw: `1`},
		"b": {err: pgFailure},
	}}}}
	r := &txRun{tx: &spTx{rows: map[string]outcome{
		"a": {raw: `1`},
		"b": {err: pgFailure},
		"c": {raw: `3`},
		"d": {raw: `5`},
	}}}
	e := newBatchExecutor(t, Options{TxPerRequest: true, DevErrors: true}, f)
	e.runTx = r.runTx
	res := e.Execute(context.Background(), Request{Query: `{
		a: userById(id: 1) { id }
		b: userById(id: 2) { id }
		c: userById(id: 3) { id }
		t: __typename
		bad: userById(id: 4) { id }
		d: userById(id: 5) { id }
	}`})
	want := `{"data":{"a":1,"b":null,"c":3,"t":"Query","bad":null,"d":5},"errors":[` +
		`{"message":"division by zero","path":["b"],"extensions":{"code":"pg:22012","constraint":"","detail":"","hint":"","table":""}},` +
		`{"message":"bad field","path":["bad"],"extensions":{"code":"internal"}}]}`
	if got := resultJSON(t, res); got != want {
		t.Fatalf("response:\n got %s\nwant %s", got, want)
	}
	if got := strings.Join(f.calls[0].sql, ","); got != "a,b,c,d" {
		t.Errorf("statements = %s, want a,b,c,d (compile errors left out)", got)
	}
	if r.calls != 1 {
		t.Fatalf("savepoint transactions = %d, want 1", r.calls)
	}
	wantLog := []string{
		"SAVEPOINT pdb_savepoint", "a", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "b", "ROLLBACK TO SAVEPOINT pdb_savepoint", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "c", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "d", "RELEASE SAVEPOINT pdb_savepoint",
	}
	if got := strings.Join(r.tx.log, ","); got != strings.Join(wantLog, ",") {
		t.Errorf("savepoint statements = %s, want %s", got, strings.Join(wantLog, ","))
	}
}

// TestBatchTxQueryLastFieldError keeps the batch's results when the failing
// field was the last statement: nothing was skipped, so nothing reruns.
func TestBatchTxQueryLastFieldError(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{
		"a": {raw: `1`},
		"b": {err: pgFailure},
	}}}}
	r := &txRun{tx: &spTx{}}
	e := newBatchExecutor(t, Options{TxPerRequest: true}, f)
	e.runTx = r.runTx
	res := e.Execute(context.Background(), Request{Query: `{ a: userById(id: 1) { id } b: userById(id: 2) { id } }`})
	want := `{"data":{"a":1,"b":null},"errors":[{"message":"division by zero","path":["b"],"extensions":{"code":"pg:22012"}}]}`
	if got := resultJSON(t, res); got != want {
		t.Fatalf("response:\n got %s\nwant %s", got, want)
	}
	if r.calls != 0 {
		t.Errorf("savepoint transactions = %d, want 0", r.calls)
	}
}

func TestBatchTxQueryReadError(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{
		"a": {err: errors.New("conn closed")},
		"b": {raw: `2`},
	}}}}
	r := &txRun{tx: &spTx{}}
	e := newBatchExecutor(t, Options{TxPerRequest: true, DevErrors: true}, f)
	e.runTx = r.runTx
	res := e.Execute(context.Background(), Request{Query: `{ a: userById(id: 1) { id } b: userById(id: 2) { id } }`})
	want := `{"data":{"a":null,"b":null},"errors":[` +
		`{"message":"conn closed","path":["a"],"extensions":{"code":"internal"}},` +
		`{"message":"current transaction is aborted, commands ignored until end of transaction block","path":["b"],"extensions":{"code":"pg:25P02","constraint":"","detail":"","hint":"","table":""}}]}`
	if got := resultJSON(t, res); got != want {
		t.Fatalf("response:\n got %s\nwant %s", got, want)
	}
	if r.calls != 0 {
		t.Errorf("savepoint transactions = %d, want 0 after a non-database error", r.calls)
	}
}

func TestBatchTxRetries(t *testing.T) {
	query := `{ a: userById(id: 1) { id } b: userById(id: 2) { id } t: __typename }`
	t.Run("succeeds on retry", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{
			{stmts: map[string]outcome{"a": {raw: `"stale"`}, "b": {err: pgSerializing}}},
			{stmts: map[string]outcome{"a": {raw: `1`}, "b": {raw: `2`}}},
		}}
		e := newBatchExecutor(t, Options{TxPerRequest: true, TxRetries: 2}, f)
		res := e.Execute(context.Background(), Request{Query: query})
		if got, want := resultJSON(t, res), `{"data":{"a":1,"b":2,"t":"Query"}}`; got != want {
			t.Fatalf("response = %s, want %s", got, want)
		}
		if len(f.calls) != 2 {
			t.Fatalf("QueryTx calls = %d, want 2", len(f.calls))
		}
	})
	t.Run("exhausted", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{
			{stmts: map[string]outcome{"a": {err: pgSerializing}}},
			{stmts: map[string]outcome{"a": {raw: `1`}, "b": {err: pgSerializing}}},
		}}
		e := newBatchExecutor(t, Options{TxPerRequest: true, TxRetries: 1, DevErrors: true}, f)
		res := e.Execute(context.Background(), Request{Query: query})
		want := `{"data":{"a":1,"b":null,"t":"Query"},"errors":[` +
			`{"message":"could not serialize access","path":["b"],"extensions":{"code":"pg:40001","constraint":"","detail":"","hint":"","table":""}}]}`
		if got := resultJSON(t, res); got != want {
			t.Fatalf("response:\n got %s\nwant %s", got, want)
		}
	})
	t.Run("retry fails before the statements", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{
			{stmts: map[string]outcome{"a": {raw: `"stale"`}, "b": {err: pgSerializing}}},
			{before: fmt.Errorf("set identity: %w", &pgconn.PgError{Code: "22023", Message: "role does not exist"})},
		}}
		e := newBatchExecutor(t, Options{TxPerRequest: true, TxRetries: 1, DevErrors: true}, f)
		res := e.Execute(context.Background(), Request{Query: query})
		want := `{"errors":[{"message":"role does not exist","extensions":{"code":"pg:22023","constraint":"","detail":"","hint":"","table":""}}]}`
		if got := resultJSON(t, res); got != want {
			t.Fatalf("response:\n got %s\nwant %s", got, want)
		}
	})
	t.Run("non-retryable errors are not retried", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"a": {err: pgFailure}}}}}
		r := &txRun{tx: &spTx{rows: map[string]outcome{"a": {err: pgFailure}, "b": {raw: `2`}}}}
		e := newBatchExecutor(t, Options{TxPerRequest: true, TxRetries: 3}, f)
		e.runTx = r.runTx
		e.Execute(context.Background(), Request{Query: query})
		if len(f.calls) != 1 || r.calls != 1 {
			t.Fatalf("QueryTx calls = %d, savepoint transactions = %d, want 1 each", len(f.calls), r.calls)
		}
	})
}

func TestBatchTxCommitFailure(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{
		stmts: map[string]outcome{"a": {raw: `1`}},
		after: &pgconn.PgError{Code: "23503", Message: "deferred constraint violated"},
	}}}
	e := newBatchExecutor(t, Options{TxPerRequest: true, DevErrors: true}, f)
	res := e.Execute(context.Background(), Request{Query: `{ a: userById(id: 1) { id } }`})
	want := `{"data":{"a":1},"errors":[{"message":"deferred constraint violated","extensions":{"code":"pg:23503","constraint":"","detail":"","hint":"","table":""}}]}`
	if got := resultJSON(t, res); got != want {
		t.Fatalf("response:\n got %s\nwant %s", got, want)
	}
}

func TestBatchTxMutation(t *testing.T) {
	const mutation = `mutation { a: createUser(input: {user: {email: "a@b.c"}}) { user { id } } t: __typename }`
	t.Run("commit after scan", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"a": {raw: `{"user":{"id":1}}`}}}}}
		e := newBatchExecutor(t, Options{}, f)
		res := e.Execute(context.Background(), Request{Query: mutation})
		if got, want := resultJSON(t, res), `{"data":{"a":{"user":{"id":1}},"t":"Mutation"}}`; got != want {
			t.Fatalf("response = %s, want %s", got, want)
		}
		if len(f.calls) != 1 || !f.calls[0].opts.ScanBeforeCommit || f.calls[0].opts.Isolation != "" {
			t.Fatalf("calls = %+v, want one ScanBeforeCommit batch at the default isolation", f.calls)
		}
	})
	for name, o := range map[string]outcome{
		"no row matched": {noRow: true},
		"sql error":      {err: pgFailure},
	} {
		t.Run(name, func(t *testing.T) {
			var scanErr error
			f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"a": o}}}}
			e := newBatchExecutor(t, Options{}, f)
			e.queryTx = func(ctx context.Context, db pgexec.ConnAcquirer, opts pgexec.TxOptions, id pgexec.Identity, stmts ...pgexec.Stmt) error {
				scanErr = f.queryTx(ctx, db, opts, id, stmts...)
				return scanErr
			}
			res := e.Execute(context.Background(), Request{Query: mutation})
			if scanErr == nil {
				t.Fatal("QueryTx returned nil: the mutation would commit")
			}
			if len(res.Errors) != 1 || string(res.Data["a"]) != "null" {
				t.Fatalf("response = %s, want one field error", resultJSON(t, res))
			}
			if _, ok := res.Data["t"]; ok {
				t.Errorf("fields after the failed mutation must be left out: %s", resultJSON(t, res))
			}
		})
	}
}

func TestBatchTxMutationIdentityFailure(t *testing.T) {
	f := &fakeBatch{attempts: []attempt{{before: errors.New("invalid role \"x y\"")}}}
	e := newBatchExecutor(t, Options{DevErrors: true}, f)
	res := e.Execute(context.Background(), Request{Query: `mutation { a: createUser(input: {user: {email: "a@b.c"}}) { user { id } } }`})
	if got, want := resultJSON(t, res), `{"errors":[{"message":"invalid role \"x y\"","extensions":{"code":"internal"}}]}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
}

func TestBatchIsolation(t *testing.T) {
	for in, want := range map[pgx.TxIsoLevel]pgx.TxIsoLevel{
		"":                 "",
		pgx.ReadCommitted:  "",
		pgx.RepeatableRead: pgx.RepeatableRead,
		pgx.Serializable:   pgx.Serializable,
	} {
		if got := batchIsolation(in); got != want {
			t.Errorf("batchIsolation(%q) = %q, want %q", in, got, want)
		}
	}
}
