package exec

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/pgexec"
)

// spTx records every statement and answers QueryRow by the statement's SQL.
type spTx struct {
	rows map[string]outcome
	log  []string
}

func (f *spTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *spTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	f.log = append(f.log, sql)
	return pgconn.CommandTag{}, nil
}

func (f *spTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.log = append(f.log, sql)
	return fakeRow{f.rows[sql]}
}

type txRun struct {
	tx    *spTx
	opts  []pgexec.TxOptions
	calls int
	// fail scripts the error RunTx itself returns after fn (commit failure).
	fail []error
}

func (r *txRun) runTx(_ context.Context, _ pgexec.TxBeginner, opts pgexec.TxOptions, _ pgexec.Identity, fn func(pgexec.Querier) error) error {
	r.opts = append(r.opts, opts)
	r.calls++
	if err := fn(r.tx); err != nil {
		return err
	}
	if len(r.fail) >= r.calls && r.fail[r.calls-1] != nil {
		return r.fail[r.calls-1]
	}
	return nil
}

const partialQuery = `mutation { a: createUser(input: {email: "a@b.c"}) { user { id } } b: createUser(input: {email: "b@b.c"}) { user { id } } c: createUser(input: {email: "c@b.c"}) { user { id } } }`

func newPartialExecutor(t *testing.T, opts Options, r *txRun) *Executor {
	t.Helper()
	e := newBatchExecutor(t, opts, &fakeBatch{})
	e.runTx = r.runTx
	return e
}

func TestPartialMutationsRunsEachFieldInASavepoint(t *testing.T) {
	r := &txRun{tx: &spTx{rows: map[string]outcome{
		"a": {raw: `{"user":{"id":1}}`},
		"b": {err: pgFailure},
		"c": {raw: `{"user":{"id":3}}`},
	}}}
	e := newPartialExecutor(t, Options{PartialMutations: true}, r)
	res := e.Execute(context.Background(), Request{Query: partialQuery, PartialMutations: true})
	if got, want := resultJSON(t, res), `{"data":{"a":{"user":{"id":1}},"b":null,"c":{"user":{"id":3}}},"errors":[{"message":"division by zero","path":["b"],"extensions":{"code":"22012"}}]}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
	want := []string{
		"SAVEPOINT pdb_savepoint", "a", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "b", "ROLLBACK TO SAVEPOINT pdb_savepoint", "RELEASE SAVEPOINT pdb_savepoint",
		"SAVEPOINT pdb_savepoint", "c", "RELEASE SAVEPOINT pdb_savepoint",
	}
	if !reflect.DeepEqual(r.tx.log, want) {
		t.Fatalf("statements = %v, want %v", r.tx.log, want)
	}
}

func TestPartialMutationsGoSideAndCompileErrors(t *testing.T) {
	r := &txRun{tx: &spTx{rows: map[string]outcome{
		"a": {noRow: true},
		"c": {raw: `{"user":{"id":3}}`},
	}}}
	e := newPartialExecutor(t, Options{PartialMutations: true}, r)
	q := strings.Replace(partialQuery, "b: createUser", "bad: createUser", 1)
	res := e.Execute(context.Background(), Request{Query: q, PartialMutations: true})
	if len(res.Errors) != 2 || string(res.Data["a"]) != "null" || string(res.Data["bad"]) != "null" || string(res.Data["c"]) != `{"user":{"id":3}}` {
		t.Fatalf("response = %s, want a and bad failed and c committed", resultJSON(t, res))
	}
	for _, s := range r.tx.log {
		if s == "bad" {
			t.Fatalf("a field that failed to compile reached the database: %v", r.tx.log)
		}
	}
}

func TestPartialMutationsNoRowMatchedNamesTheAlias(t *testing.T) {
	r := &txRun{tx: &spTx{rows: map[string]outcome{
		"a": {raw: `{"user":{"id":1}}`},
		"b": {noRow: true},
		"c": {raw: `{"user":{"id":3}}`},
	}}}
	e := newPartialExecutor(t, Options{PartialMutations: true}, r)
	res := e.Execute(context.Background(), Request{Query: partialQuery, PartialMutations: true})
	if got, want := resultJSON(t, res), `{"data":{"a":{"user":{"id":1}},"b":null,"c":{"user":{"id":3}}},"errors":[{"message":"no row matched \"b\"","path":["b"]}]}`; got != want {
		t.Fatalf("response = %s, want %s", got, want)
	}
}

func TestPartialMutationsSerializationFailureAbortsTheTransaction(t *testing.T) {
	r := &txRun{tx: &spTx{rows: map[string]outcome{
		"a": {raw: `{"user":{"id":1}}`},
		"b": {err: pgSerializing},
	}}}
	e := newPartialExecutor(t, Options{PartialMutations: true, TxRetries: 2}, r)
	res := e.Execute(context.Background(), Request{Query: partialQuery, PartialMutations: true})
	if len(res.Data) != 0 || len(res.Errors) != 1 || len(res.Errors[0].Path) != 0 {
		t.Fatalf("response = %s, want only the transaction error", resultJSON(t, res))
	}
	if r.opts[0].MaxRetries != 2 {
		t.Fatalf("RunTx got MaxRetries %d, want the configured 2", r.opts[0].MaxRetries)
	}
	if want := []string{"SAVEPOINT pdb_savepoint", "a", "RELEASE SAVEPOINT pdb_savepoint", "SAVEPOINT pdb_savepoint", "b"}; !reflect.DeepEqual(r.tx.log, want) {
		t.Fatalf("statements = %v, want %v", r.tx.log, want)
	}
}

func TestPartialMutationsCommitFailureDropsEverything(t *testing.T) {
	r := &txRun{
		tx:   &spTx{rows: map[string]outcome{"a": {raw: `{}`}, "b": {raw: `{}`}, "c": {raw: `{}`}}},
		fail: []error{pgSerializing},
	}
	e := newPartialExecutor(t, Options{PartialMutations: true}, r)
	res := e.Execute(context.Background(), Request{Query: partialQuery, PartialMutations: true})
	if len(res.Data) != 0 || len(res.Errors) != 1 {
		t.Fatalf("response = %s, want only the commit error", resultJSON(t, res))
	}
}

func TestPartialMutationsOptIn(t *testing.T) {
	t.Run("rejected while disabled", func(t *testing.T) {
		e := newPartialExecutor(t, Options{}, &txRun{tx: &spTx{}})
		res := e.Execute(context.Background(), Request{Query: partialQuery, PartialMutations: true})
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "partial mutations are disabled") || len(res.Data) != 0 {
			t.Fatalf("response = %s", resultJSON(t, res))
		}
	})
	t.Run("default stays all or nothing", func(t *testing.T) {
		f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"a": {raw: `{"user":{"id":1}}`}}}}}
		e := newBatchExecutor(t, Options{PartialMutations: true, TxMutations: true}, f)
		r := &txRun{tx: &spTx{}}
		e.runTx = r.runTx
		res := e.Execute(context.Background(), Request{Query: `mutation { a: createUser(input: {email: "a@b.c"}) { user { id } } }`})
		if len(res.Errors) != 0 || r.calls != 0 || len(f.calls) != 1 {
			t.Fatalf("response = %s, runTx calls = %d, batches = %d", resultJSON(t, res), r.calls, len(f.calls))
		}
	})
	t.Run("ignored for queries", func(t *testing.T) {
		f := &fakeBatch{}
		e := newBatchExecutor(t, Options{}, f)
		res := e.Execute(context.Background(), Request{Query: `{ __typename }`, PartialMutations: true})
		if len(res.Errors) != 0 {
			t.Fatalf("response = %s", resultJSON(t, res))
		}
	})
	t.Run("forces a transaction when mutations are not transactional", func(t *testing.T) {
		r := &txRun{tx: &spTx{rows: map[string]outcome{"a": {raw: `{}`}}}}
		e := newPartialExecutor(t, Options{PartialMutations: true, TxMutations: false}, r)
		e.Execute(context.Background(), Request{Query: `mutation { a: createUser(input: {email: "a@b.c"}) { user { id } } }`, PartialMutations: true})
		if r.calls != 1 {
			t.Fatalf("runTx calls = %d, want 1", r.calls)
		}
	})
}
