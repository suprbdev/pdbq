package exec

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbq/internal/compile"
)

// fakeQuerier answers QueryRow with the outcome scripted for the SQL and
// records the arguments of every call.
type fakeQuerier struct {
	rows  map[string]outcome
	calls []queryCall
}

type queryCall struct {
	sql  string
	args []any
}

func (q *fakeQuerier) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

func (q *fakeQuerier) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}

func (q *fakeQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.calls = append(q.calls, queryCall{sql, args})
	return fakeRow{q.rows[sql]}
}

func TestQueryStatementRunsThen(t *testing.T) {
	stmt := &compile.Statement{
		SQL: "first", Args: []any{7},
		Then: &compile.Statement{SQL: "second", Args: []any{"cm"}},
	}
	for _, tc := range []struct {
		name  string
		rows  map[string]outcome
		want  string
		err   error
		calls []queryCall
	}{{
		name:  "then reads the first result as $1",
		rows:  map[string]outcome{"first": {raw: `["(1,a)"]`}, "second": {raw: `{"post":{"id":1}}`}},
		want:  `{"post":{"id":1}}`,
		calls: []queryCall{{"first", []any{7}}, {"second", []any{`["(1,a)"]`, "cm"}}},
	}, {
		name:  "a failing first statement skips then",
		rows:  map[string]outcome{"first": {err: pgFailure}},
		err:   pgFailure,
		calls: []queryCall{{"first", []any{7}}},
	}, {
		name:  "a failing then is the field's error",
		rows:  map[string]outcome{"first": {raw: `{}`}, "second": {err: pgFailure}},
		err:   pgFailure,
		calls: []queryCall{{"first", []any{7}}, {"second", []any{`{}`, "cm"}}},
	}, {
		name:  "no row from then is null",
		rows:  map[string]outcome{"first": {raw: `[]`}, "second": {noRow: true}},
		want:  `null`,
		calls: []queryCall{{"first", []any{7}}, {"second", []any{`[]`, "cm"}}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newBatchExecutor(t, Options{}, &fakeBatch{})
			q := &fakeQuerier{rows: tc.rows}
			op := &Operation{Type: ast.Mutation, Document: &ast.QueryDocument{}}
			raw, err := e.queryStatement(context.Background(), q, op, &ast.Field{Name: "f", Alias: "f"}, stmt)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if tc.err == nil && string(raw) != tc.want {
				t.Errorf("raw = %s, want %s", raw, tc.want)
			}
			if !reflect.DeepEqual(q.calls, tc.calls) {
				t.Errorf("calls = %+v, want %+v", q.calls, tc.calls)
			}
		})
	}
}
