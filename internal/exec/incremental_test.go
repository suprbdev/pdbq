package exec

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
)

// renderSelection prints sel compactly: fields by response key, windowed
// fields as key@start+count, inline fragments as ...{ }.
func renderSelection(sel ast.SelectionSet) string {
	parts := make([]string, 0, len(sel))
	for _, item := range sel {
		switch v := item.(type) {
		case *ast.Field:
			s := v.Alias
			if d := v.Directives.ForName("__pdbqStreamWindow"); d != nil {
				s += "@" + d.Arguments.ForName("start").Value.Raw + "+" + d.Arguments.ForName("count").Value.Raw
			}
			if len(v.SelectionSet) > 0 {
				s += "{" + renderSelection(v.SelectionSet) + "}"
			}
			parts = append(parts, s)
		case *ast.InlineFragment:
			parts = append(parts, "...{"+renderSelection(v.SelectionSet)+"}")
		case *ast.FragmentSpread:
			parts = append(parts, "..."+v.Name)
		}
	}
	return strings.Join(parts, " ")
}

// renderCompile compiles a root field to its rendering, reporting every
// window it carries as applied, except on fields named in noWindow.
func renderCompile(_ context.Context, req *compile.Request) (*compile.Statement, error) {
	sel := ast.SelectionSet{req.Field}
	stmt := &compile.Statement{SQL: renderSelection(sel)}
	var walk func(ast.SelectionSet)
	walk = func(sel ast.SelectionSet) {
		for _, item := range sel {
			switch v := item.(type) {
			case *ast.Field:
				if d := v.Directives.ForName("__pdbqStreamWindow"); d != nil {
					id, _ := strconv.Atoi(d.Arguments.ForName("id").Value.Raw)
					stmt.Streamed = append(stmt.Streamed, id)
				}
				walk(v.SelectionSet)
			case *ast.InlineFragment:
				walk(v.SelectionSet)
			}
		}
	}
	walk(sel)
	return stmt, nil
}

// fakeTx answers QueryRow by the statement's SQL (a renderSelection
// rendering) from rows, and records the statements and tx options.
type fakeTx struct {
	rows  map[string]outcome
	sql   []string
	opts  []pgexec.TxOptions
	onRow func(sql string)
}

func (f *fakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (f *fakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (f *fakeTx) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	f.sql = append(f.sql, sql)
	if f.onRow != nil {
		f.onRow(sql)
	}
	o, ok := f.rows[sql]
	if !ok {
		return fakeRow{outcome{err: fmt.Errorf("unscripted statement %q", sql)}}
	}
	return fakeRow{o}
}

func (f *fakeTx) runTx(ctx context.Context, _ pgexec.TxBeginner, opts pgexec.TxOptions, _ pgexec.Identity, fn func(pgexec.Querier) error) error {
	f.opts = append(f.opts, opts)
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn(f)
}

func newIncExecutor(t *testing.T, opts Options, tx *fakeTx) *Executor {
	t.Helper()
	built, err := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{
		FilterIndexedOnly: true, Functions: true, IncrementalDelivery: true,
	}).Build()
	if err != nil {
		t.Fatal(err)
	}
	opts.Incremental.Enabled = true
	e := New(nil, built, renderCompile, nil, opts)
	e.runTx = tx.runTx
	return e
}

type emitted struct {
	body        string
	incremental bool
}

func runIncremental(t *testing.T, e *Executor, ctx context.Context, query string, vars map[string]any, format IncrementalFormat) ([]emitted, error) {
	t.Helper()
	var out []emitted
	err := e.ExecuteIncrementalRequest(ctx, Request{Query: query, Variables: vars}, format, func(body []byte, inc bool) error {
		out = append(out, emitted{string(body), inc})
		return nil
	})
	return out, err
}

func checkPayloads(t *testing.T, got []emitted, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		for _, g := range got {
			t.Logf("got %s", g.body)
		}
		t.Fatalf("%d payloads, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].body != want[i] {
			t.Errorf("payload %d:\n got %s\nwant %s", i, got[i].body, want[i])
		}
		if !got[i].incremental && len(want) > 1 {
			t.Errorf("payload %d not marked incremental", i)
		}
	}
}

func TestIncrementalDeferInList(t *testing.T) {
	tx := &fakeTx{rows: map[string]outcome{
		`allUsers{nodes{id}}`:                                    {raw: `{"nodes":[{"id":1},{"id":2},null]}`},
		`allUsers{nodes{...{email}}}`:                            {raw: `{"nodes":[{"email":"a"},{"email":"b"},null]}`},
		`allUsers{nodes{...{...{postsByAuthorId{totalCount}}}}}`: {raw: `{"nodes":[{"postsByAuthorId":1},{"postsByAuthorId":2},null]}`},
	}}
	e := newIncExecutor(t, Options{}, tx)
	q := `{ allUsers(first: 3) { nodes { id ...@defer(label: "e") { email ... @defer { postsByAuthorId { totalCount } } } } } }`
	got, err := runIncremental(t, e, context.Background(), q, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	checkPayloads(t, got,
		`{"data":{"allUsers":{"nodes":[{"id":1},{"id":2},null]}},"pending":[{"id":"0","path":["allUsers","nodes",0],"label":"e"},{"id":"1","path":["allUsers","nodes",1],"label":"e"}],"hasNext":true}`,
		`{"hasNext":true,"pending":[{"id":"2","path":["allUsers","nodes",0]},{"id":"3","path":["allUsers","nodes",1]}],"incremental":[{"id":"0","data":{"email":"a"}},{"id":"1","data":{"email":"b"}}],"completed":[{"id":"0"},{"id":"1"}]}`,
		`{"hasNext":false,"incremental":[{"id":"2","data":{"postsByAuthorId":1}},{"id":"3","data":{"postsByAuthorId":2}}],"completed":[{"id":"2"},{"id":"3"}]}`,
	)
	if len(tx.opts) == 0 || tx.opts[len(tx.opts)-1].Isolation != pgx.RepeatableRead || tx.opts[len(tx.opts)-1].MaxRetries != 0 {
		t.Errorf("tx options = %+v, want repeatable read without retries", tx.opts)
	}
}

func TestIncrementalLegacyFormat(t *testing.T) {
	tx := &fakeTx{rows: map[string]outcome{
		`allUsers{nodes@0+1{id}}`: {raw: `{"nodes":[{"id":1}]}`},
		`allUsers{nodes@1+2{id}}`: {raw: `{"nodes":[{"id":2},{"id":3}]}`},
		`allUsers{nodes@3+2{id}}`: {raw: `{"nodes":[{"id":4}]}`},
		`userById{...{email}}`:    {raw: `{"email":"a"}`},
		`userById{id}`:            {raw: `{"id":1}`},
	}}
	e := newIncExecutor(t, Options{Incremental: IncrementalOptions{StreamBatchSize: 2}}, tx)
	q := `{ allUsers(first: 9) { nodes @stream(initialCount: 1, label: "s") { id } } userById(id: 1) { id ... @defer(label: "d") { email } } }`
	got, err := runIncremental(t, e, context.Background(), q, nil, IncrementalDefer20220824)
	if err != nil {
		t.Fatal(err)
	}
	checkPayloads(t, got,
		`{"data":{"allUsers":{"nodes":[{"id":1}]},"userById":{"id":1}},"hasNext":true}`,
		`{"hasNext":true,"incremental":[{"items":[{"id":2},{"id":3}],"path":["allUsers","nodes",1],"label":"s"}]}`,
		`{"hasNext":true,"incremental":[{"items":[{"id":4}],"path":["allUsers","nodes",3],"label":"s"}]}`,
		`{"hasNext":false,"incremental":[{"data":{"email":"a"},"path":["userById"],"label":"d"}]}`,
	)

	tx.sql = nil
	got, err = runIncremental(t, e, context.Background(), q, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	checkPayloads(t, got,
		`{"data":{"allUsers":{"nodes":[{"id":1}]},"userById":{"id":1}},"pending":[{"id":"0","path":["allUsers","nodes"],"label":"s"},{"id":"1","path":["userById"],"label":"d"}],"hasNext":true}`,
		`{"hasNext":true,"incremental":[{"id":"0","items":[{"id":2},{"id":3}]}]}`,
		`{"hasNext":true,"incremental":[{"id":"0","items":[{"id":4}]}],"completed":[{"id":"0"}]}`,
		`{"hasNext":false,"incremental":[{"id":"1","data":{"email":"a"}}],"completed":[{"id":"1"}]}`,
	)
}

func TestIncrementalRootDefer(t *testing.T) {
	tx := &fakeTx{rows: map[string]outcome{
		`postById{title}`: {raw: `{"title":"t"}`},
	}}
	e := newIncExecutor(t, Options{}, tx)
	got, err := runIncremental(t, e, context.Background(), `{ __typename ... @defer { postById(id: 1) { title } } }`, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	checkPayloads(t, got,
		`{"data":{"__typename":"Query"},"pending":[{"id":"0","path":[]}],"hasNext":true}`,
		`{"hasNext":false,"incremental":[{"id":"0","data":{"postById":{"title":"t"}}}],"completed":[{"id":"0"}]}`,
	)
}

// TestIncrementalNothingDeferred covers operations answered by one result:
// a stream whose list fits its initialCount, a defer under a null object, if
// false, a mutation, and a stream the compiler does not window.
func TestIncrementalNothingDeferred(t *testing.T) {
	cases := map[string]struct {
		query string
		rows  map[string]outcome
		want  string
	}{
		"short list": {
			`{ allUsers { nodes @stream(initialCount: 2) { id } } }`,
			map[string]outcome{`allUsers{nodes@0+2{id}}`: {raw: `{"nodes":[{"id":1}]}`}},
			`{"data":{"allUsers":{"nodes":[{"id":1}]}}}`,
		},
		"null parent": {
			`{ userById(id: 9) { id ... @defer { email } } }`,
			map[string]outcome{`userById{id}`: {noRow: true}},
			`{"data":{"userById":null}}`,
		},
		"if false": {
			`query($d: Boolean!) { userById(id: 1) { id ... @defer(if: $d) { email } } }`,
			nil,
			`{"data":{"userById":{"id":1}}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tx := &fakeTx{rows: tc.rows}
			e := newIncExecutor(t, Options{}, tx)
			e.queryTx = (&fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"userById": {raw: `{"id":1}`}}}}}).queryTx
			e.Compile = func(ctx context.Context, req *compile.Request) (*compile.Statement, error) {
				if name == "if false" {
					return fakeCompile(ctx, req)
				}
				return renderCompile(ctx, req)
			}
			e.Opts.TxPerRequest = true
			got, err := runIncremental(t, e, context.Background(), tc.query, map[string]any{"d": false}, IncrementalV02)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].incremental || got[0].body != tc.want {
				t.Fatalf("got %+v, want one result %s", got, tc.want)
			}
		})
	}
}

func TestIncrementalErrors(t *testing.T) {
	boom := &pgconn.PgError{Code: "22012", Message: "division by zero"}
	tx := &fakeTx{rows: map[string]outcome{
		`userById{id}`:             {raw: `{"id":1}`},
		`userById{...{email}}`:     {err: boom},
		`allUsers{nodes@0+0{id}}`:  {raw: `{"nodes":[]}`},
		`allUsers{nodes@0+10{id}}`: {err: boom},
	}}
	e := newIncExecutor(t, Options{DevErrors: true}, tx)
	got, err := runIncremental(t, e, context.Background(), `{ userById(id: 1) { id ... @defer { email } } allUsers { nodes @stream { id } } }`, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	ext := `"extensions":{"code":"22012","constraint":"","detail":"","hint":"","table":""}`
	checkPayloads(t, got,
		`{"data":{"userById":{"id":1},"allUsers":{"nodes":[]}},"pending":[{"id":"0","path":["userById"]},{"id":"1","path":["allUsers","nodes"]}],"hasNext":true}`,
		`{"hasNext":true,"completed":[{"id":"0","errors":[{"message":"division by zero","path":["userById"],`+ext+`}]}]}`,
		`{"hasNext":false,"completed":[{"id":"1","errors":[{"message":"division by zero","path":["allUsers","nodes"],`+ext+`}]}]}`,
	)
}

func TestIncrementalTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tx := &fakeTx{rows: map[string]outcome{
		`allUsers{nodes@0+1{id}}`: {raw: `{"nodes":[{"id":1}]}`},
		`allUsers{nodes@1+1{id}}`: {raw: `{"nodes":[{"id":2}]}`},
		`userById{id}`:            {raw: `{"id":1}`},
	}}
	tx.onRow = func(sql string) {
		if sql == `allUsers{nodes@1+1{id}}` {
			cancel()
		}
	}
	e := newIncExecutor(t, Options{Incremental: IncrementalOptions{StreamBatchSize: 1}}, tx)
	got, err := runIncremental(t, e, ctx, `{ allUsers { nodes @stream(initialCount: 1) { id } } userById(id: 1) { id ... @defer { email } } }`, nil, IncrementalV02)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	checkPayloads(t, got,
		`{"data":{"allUsers":{"nodes":[{"id":1}]},"userById":{"id":1}},"pending":[{"id":"0","path":["allUsers","nodes"]},{"id":"1","path":["userById"]}],"hasNext":true}`,
		`{"hasNext":false,"completed":[{"id":"0","errors":[{"message":"request timeout","path":["allUsers","nodes"]}]},{"id":"1","errors":[{"message":"request timeout","path":["userById"]}]}]}`,
	)
}

// TestIncrementalEmitError checks that a failed write (the client went
// away) stops the delivery.
func TestIncrementalEmitError(t *testing.T) {
	tx := &fakeTx{rows: map[string]outcome{
		`userById{id}`:         {raw: `{"id":1}`},
		`userById{...{email}}`: {raw: `{"email":"a"}`},
	}}
	e := newIncExecutor(t, Options{}, tx)
	gone := errors.New("client gone")
	calls := 0
	err := e.ExecuteIncrementalRequest(context.Background(), Request{Query: `{ userById(id: 1) { id ... @defer { email } } }`}, IncrementalV02, func([]byte, bool) error {
		calls++
		return gone
	})
	if !errors.Is(err, gone) || calls != 1 {
		t.Fatalf("err = %v after %d emits, want the emit error after 1", err, calls)
	}
	if len(tx.sql) != 1 {
		t.Errorf("ran %v after the client went away", tx.sql)
	}
}

func TestIncrementalValidation(t *testing.T) {
	cases := map[string]struct {
		query, want string
		vars        map[string]any
	}{
		"stream on non-list":    {`{ userById(id: 1) { email @stream } }`, `cannot be used on non-list field \"User.email\"`, nil},
		"negative initialCount": {`{ allUsers { nodes @stream(initialCount: -1) { id } } }`, "initialCount must be a positive integer", nil},
		"duplicate label":       {`{ a: userById(id: 1) { ... @defer(label: "x") { id } } b: userById(id: 2) { ... @defer(label: "x") { id } } }`, "label argument must be unique", nil},
		"variable label":        {`query($l: String) { userById(id: 1) { ... @defer(label: $l) { id } } }`, `must be a static string`, map[string]any{"l": "x"}},
		"too many":              {`{ userById(id: 1) { ... @defer { id } ... @defer { email } ... @defer { mood } } }`, "more than 2 selections", nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := newIncExecutor(t, Options{Incremental: IncrementalOptions{MaxDeferred: 2}}, &fakeTx{})
			got, err := runIncremental(t, e, context.Background(), tc.query, tc.vars, IncrementalV02)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || !strings.Contains(got[0].body, tc.want) {
				t.Fatalf("got %+v, want an error containing %q", got, tc.want)
			}
		})
	}
}

// TestIncrementalSpreadTwice checks that a fragment spread at two positions
// defers at both, each with its own statement.
func TestIncrementalSpreadTwice(t *testing.T) {
	tx := &fakeTx{rows: map[string]outcome{
		`a{id}`:              {raw: `{"id":1}`},
		`b{id}`:              {raw: `{"id":2}`},
		`a{...{...{email}}}`: {raw: `{"email":"x"}`},
		`b{...{...{email}}}`: {raw: `{"email":"y"}`},
	}}
	e := newIncExecutor(t, Options{}, tx)
	got, err := runIncremental(t, e, context.Background(), `{ a: userById(id: 1) { id ...F } b: userById(id: 2) { id ...F } } fragment F on User { ... @defer { email } }`, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	checkPayloads(t, got,
		`{"data":{"a":{"id":1},"b":{"id":2}},"pending":[{"id":"0","path":["a"]},{"id":"1","path":["b"]}],"hasNext":true}`,
		`{"hasNext":true,"incremental":[{"id":"0","data":{"email":"x"}}],"completed":[{"id":"0"}]}`,
		`{"hasNext":false,"incremental":[{"id":"1","data":{"email":"y"}}],"completed":[{"id":"1"}]}`,
	)
}

// TestIncrementalDisabled checks that without the option a deferred
// fragment is delivered inline and nothing is planned.
func TestIncrementalDisabled(t *testing.T) {
	tx := &fakeTx{}
	e := newIncExecutor(t, Options{}, tx)
	e.Opts.Incremental.Enabled = false
	f := &fakeBatch{attempts: []attempt{{stmts: map[string]outcome{"userById": {raw: `{"id":1,"email":"a"}`}}}}}
	e.queryTx = f.queryTx
	e.Compile = fakeCompile
	e.Opts.TxPerRequest = true
	got, err := runIncremental(t, e, context.Background(), `{ userById(id: 1) { id ... @defer { email } } }`, nil, IncrementalV02)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].incremental || got[0].body != `{"data":{"userById":{"id":1,"email":"a"}}}` {
		t.Fatalf("got %+v", got)
	}
}
