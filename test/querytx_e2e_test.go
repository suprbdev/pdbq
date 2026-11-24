package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/server"
)

const qtxSchemaSQL = `
DROP SCHEMA IF EXISTS qtx CASCADE;
CREATE SCHEMA qtx;
CREATE TABLE qtx.qtx_log (id serial PRIMARY KEY, note text NOT NULL);
CREATE SEQUENCE qtx.qtx_seq;
CREATE FUNCTION qtx.qtx_ok(n integer) RETURNS integer STABLE LANGUAGE sql AS $$ SELECT n $$;
CREATE FUNCTION qtx.qtx_fail() RETURNS integer STABLE LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'qtx failure' USING ERRCODE = '22012';
END $$;
CREATE FUNCTION qtx.qtx_whoami() RETURNS text STABLE LANGUAGE sql AS $$
    SELECT current_user || ':' || coalesce(current_setting('jwt.claims.user_id', true), '')
        || ':' || current_setting('transaction_isolation')
$$;
CREATE FUNCTION qtx.qtx_whoami_write() RETURNS text VOLATILE LANGUAGE sql AS $$
    SELECT current_user || ':' || coalesce(current_setting('jwt.claims.user_id', true), '')
        || ':' || current_setting('transaction_isolation')
$$;
CREATE FUNCTION qtx.qtx_serial() RETURNS integer STABLE LANGUAGE plpgsql AS $$
DECLARE n bigint;
BEGIN
    n := nextval('qtx.qtx_seq');
    IF n % 2 = 1 THEN
        RAISE EXCEPTION 'qtx serialization failure' USING ERRCODE = '40001';
    END IF;
    RETURN n::integer;
END $$;
CREATE FUNCTION qtx.qtx_bump(note text) RETURNS integer VOLATILE LANGUAGE sql AS $$
    INSERT INTO qtx.qtx_log (note) VALUES (note) RETURNING id
$$;
GRANT USAGE ON SCHEMA qtx TO anonymous, app_user;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA qtx TO anonymous, app_user;
GRANT SELECT, INSERT ON qtx.qtx_log TO anonymous, app_user;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA qtx TO anonymous, app_user;
`

// qtxDB creates the qtx schema (probe functions for the transaction path)
// and returns a connection to the test database; both are dropped when the
// test ends.
func qtxDB(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, qtxSchemaSQL); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS qtx CASCADE`)
		_ = conn.Close(ctx)
	})
	return conn
}

func qtxServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	hs := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{"public", "qtx"}
		if mutate != nil {
			mutate(c)
		}
	})
	return hs.URL
}

// postRaw posts query with the given headers and returns the response body
// with insignificant whitespace removed.
func postRaw(t *testing.T, url, query string, headers map[string]string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query})
	req, err := http.NewRequest("POST", url+"/graphql", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		t.Fatalf("bad response: %v: %s", err, raw)
	}
	return buf.String()
}

func qtxLogNotes(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var notes string
	err := conn.QueryRow(context.Background(), `SELECT coalesce(string_agg(note, ',' ORDER BY id), '') FROM qtx.qtx_log`).Scan(&notes)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

func qtxSetSeq(t *testing.T, conn *pgx.Conn, last int) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), `SELECT setval('qtx.qtx_seq', $1)`, last); err != nil {
		t.Fatal(err)
	}
}

const (
	qtxFailDev    = `{"message":"qtx failure","path":["b"],"extensions":{"code":"pg:22012","constraint":"","detail":"","hint":"","table":""}}`
	qtxCompileErr = `{"message":"compile: first must be \u003e= 0","path":["e"],"extensions":{"code":"invalid_argument"}}`
)

func qtxAbortedDev(field string) string {
	return `{"message":"current transaction is aborted, commands ignored until end of transaction block","path":["` + field +
		`"],"extensions":{"code":"pg:25P02","constraint":"","detail":"","hint":"","table":""}}`
}

// TestTxQueryFieldErrors pins the response of a query document mixing
// succeeding fields, a SQL error, later SQL fields, an in-memory field and a
// compile error. Without a transaction every field is independent; in one,
// the operation runs again with each field in its own savepoint once the
// SQL error cut the batch short, so the response is the same: only the
// failing fields report errors, and none without a path.
func TestTxQueryFieldErrors(t *testing.T) {
	qtxDB(t)
	const query = `{
		a: qtxOk(n: 1)
		b: qtxFail
		c: qtxOk(n: 3)
		d: __typename
		e: allUsers(first: -1) { totalCount }
		f: qtxOk(n: 6)
	}`
	isolated := `{"data":{"a":1,"b":null,"c":3,"d":"Query","e":null,"f":6},"errors":[` + qtxFailDev + `,` + qtxCompileErr + `]}`
	cases := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"no transaction", nil, isolated},
		{"per request", func(c *config.Config) { c.TX.PerRequest = true }, isolated},
		{"per request serializable", func(c *config.Config) {
			c.TX.PerRequest = true
			c.TX.Isolation = "serializable"
		}, isolated},
		{"per request prod errors", func(c *config.Config) {
			c.TX.PerRequest = true
			c.Errors.Detail = "prod"
		}, `{"data":{"a":1,"b":null,"c":3,"d":"Query","e":null,"f":6},"errors":[` +
			`{"message":"qtx failure","path":["b"],"extensions":{"code":"pg:22012"}},` +
			`{"message":"compile: first must be \u003e= 0","path":["e"],"extensions":{"code":"invalid_argument"}}]}`},
		{"per request without failure", func(c *config.Config) { c.TX.PerRequest = true }, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := qtxServer(t, tc.mutate)
			q, want := query, tc.want
			if want == "" {
				q = `{ a: qtxOk(n: 1) d: __typename e: allUsers(first: -1) { totalCount } f: qtxOk(n: 6) }`
				want = `{"data":{"a":1,"d":"Query","e":null,"f":6},"errors":[` + qtxCompileErr + `]}`
			}
			if got := postRaw(t, url, q, nil); got != want {
				t.Errorf("got  %s\nwant %s", got, want)
			}
		})
	}
}

// TestTxQueryRetry covers serialization-failure retries of a transactional
// query: every field runs again and the response holds only the last
// attempt. Exhausted retries return the field errors without the commit
// error.
func TestTxQueryRetry(t *testing.T) {
	conn := qtxDB(t)
	url := qtxServer(t, func(c *config.Config) {
		c.TX.PerRequest = true
		c.TX.MaxRetries = 1
	})

	qtxSetSeq(t, conn, 10)
	want := `{"data":{"a":1,"b":12,"c":3}}`
	if got := postRaw(t, url, `{ a: qtxOk(n: 1) b: qtxSerial c: qtxOk(n: 3) }`, nil); got != want {
		t.Errorf("retried query:\ngot  %s\nwant %s", got, want)
	}

	qtxSetSeq(t, conn, 10)
	want = `{"data":{"a":12,"b":null,"c":null,"d":"Query"},"errors":[` +
		`{"message":"qtx serialization failure","path":["b"],"extensions":{"code":"pg:40001","constraint":"","detail":"","hint":"","table":""}},` +
		qtxAbortedDev("c") + `]}`
	if got := postRaw(t, url, `{ a: qtxSerial b: qtxSerial c: qtxOk(n: 3) d: __typename }`, nil); got != want {
		t.Errorf("exhausted retries:\ngot  %s\nwant %s", got, want)
	}
}

// TestTxMutationRollback pins mutation documents whose second field fails
// in SQL, in Go after its statement ran (no row matched) or at compile
// time: the first field's write is rolled back and the third field is left
// out of the response without running (the qtx_log ids show which
// statements ran). Single-statement mutations, which run as one batch, roll
// back and commit the same way.
func TestTxMutationRollback(t *testing.T) {
	conn := qtxDB(t)
	url := qtxServer(t, nil)
	cases := []struct {
		name, second, want string
	}{
		{"sql error", `b: addNumbers(input: {a: 2147483647, b: 1}) { integer }`,
			`{"data":{"a":{"integer":1},"b":null},"errors":[{"message":"integer out of range","path":["b"],"extensions":{"code":"pg:22003","constraint":"","detail":"","hint":"","table":""}}]}`},
		{"no row matched", `b: updateUserById(input: {id: 999999, userPatch: {fullName: "x"}}) { user { id } }`,
			`{"data":{"a":{"integer":2},"b":null},"errors":[{"message":"no row matched \"b\"","path":["b"],"extensions":{"code":"not_found"}}]}`},
		{"compile error", `b: createUser(input: {user: {email: "qtx@example.com"}}) { user { postsByAuthorId(first: -1) { totalCount } } }`,
			`{"data":{"a":{"integer":3},"b":null},"errors":[{"message":"compile: first must be \u003e= 0","path":["b"],"extensions":{"code":"invalid_argument"}}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := postRaw(t, url, `mutation {
				a: qtxBump(input: {note: "a"}) { integer }
				`+tc.second+`
				c: qtxBump(input: {note: "c"}) { integer }
			}`, nil)
			if got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
			if notes := qtxLogNotes(t, conn); notes != "" {
				t.Errorf("qtx_log = %q after a failed mutation, want it rolled back", notes)
			}
		})
	}

	want := `{"data":{"b":null},"errors":[{"message":"integer out of range","path":["b"],"extensions":{"code":"pg:22003","constraint":"","detail":"","hint":"","table":""}}]}`
	if got := postRaw(t, url, `mutation { b: addNumbers(input: {a: 2147483647, b: 1}) { integer } d: __typename }`, nil); got != want {
		t.Errorf("single failing mutation:\ngot  %s\nwant %s", got, want)
	}

	want = `{"data":{"a":{"integer":4},"d":"Mutation","c":{"integer":5}}}`
	if got := postRaw(t, url, `mutation { a: qtxBump(input: {note: "a"}) { integer } d: __typename c: qtxBump(input: {note: "c"}) { integer } }`, nil); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if notes := qtxLogNotes(t, conn); notes != "a,c" {
		t.Errorf("qtx_log = %q, want %q", notes, "a,c")
	}
	want = `{"data":{"s":{"integer":6}}}`
	if got := postRaw(t, url, `mutation { s: qtxBump(input: {note: "s"}) { integer } }`, nil); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if notes := qtxLogNotes(t, conn); notes != "a,c,s" {
		t.Errorf("qtx_log = %q, want %q", notes, "a,c,s")
	}
}

// TestTxRLSIdentity checks that every transactional request runs with its
// own role, claims and isolation level, for queries and mutations, over a
// single pooled connection so a leaked identity would show up in the next
// request.
func TestTxRLSIdentity(t *testing.T) {
	qtxDB(t)
	for _, tc := range []struct{ isolation, level string }{
		{"read_committed", "read committed"},
		{"repeatable_read", "repeatable read"},
		{"serializable", "serializable"},
	} {
		t.Run(tc.isolation, func(t *testing.T) {
			url := qtxServer(t, func(c *config.Config) {
				c.Database.MaxConns = 1
				headersIdentity(c)
				c.TX.Isolation = tc.isolation
			})
			user := func(id string) map[string]string {
				return map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": id}
			}
			steps := []struct {
				query   string
				headers map[string]string
				want    string
			}{
				{`{ a: qtxWhoami b: qtxWhoami }`, user("7"),
					`{"data":{"a":"app_user:7:` + tc.level + `","b":"app_user:7:` + tc.level + `"}}`},
				{`{ a: qtxWhoami }`, nil, `{"data":{"a":"anonymous::` + tc.level + `"}}`},
				{`mutation { a: qtxWhoamiWrite(input: {}) { string } }`, user("8"),
					`{"data":{"a":{"string":"app_user:8:` + tc.level + `"}}}`},
				{`mutation { a: qtxWhoamiWrite(input: {}) { string } }`, nil,
					`{"data":{"a":{"string":"anonymous::` + tc.level + `"}}}`},
				{`{ __typename }`, map[string]string{"X-Pdbq-Claim-Role": "no_such_role"},
					`{"errors":[{"message":"role \"no_such_role\" does not exist","extensions":{"code":"pg:22023","constraint":"","detail":"","hint":"","table":""}}]}`},
				{`{ a: qtxWhoami }`, nil, `{"data":{"a":"anonymous::` + tc.level + `"}}`},
			}
			for i, s := range steps {
				if got := postRaw(t, url, s.query, s.headers); got != s.want {
					t.Errorf("step %d %s:\ngot  %s\nwant %s", i, s.query, got, s.want)
				}
			}
		})
	}
}

// lockedBuffer is a bytes.Buffer safe for concurrent writes by a logger.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// loggedServer is testServer with the executor logging into the returned
// buffer.
func loggedServer(t *testing.T, mutate func(*config.Config)) (string, *lockedBuffer) {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuffer{}
	app := pdbq.New(cfg, pdbq.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	t.Cleanup(app.Close)
	ctx := context.Background()
	cat, err := app.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	built, err := app.BuildSchema(ctx, cat)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := server.New(cfg, mustAuthenticator(t, cfg), app.Executor(built), built.SDL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs.URL, logs
}

// TestTxQueryFieldErrorUnderRoleSwitching: under role switching (a
// transaction for every request), a client-caused database error in one
// query root field is that field's error alone. The other root fields keep
// their values, no 25P02 or pathless internal error is added and nothing is
// logged at ERROR, as without a transaction.
func TestTxQueryFieldErrorUnderRoleSwitching(t *testing.T) {
	url, logs := loggedServer(t, func(c *config.Config) { jwtIdentity(c, "e2e-role-switch-secret") })
	for _, tc := range []struct{ name, query, want string }{
		{"invalid input", `{ bad: node(nodeId: "WyJVc2VyIiwieCJd") { nodeId } allUsers(orderBy: [ID_ASC], first: 1) { nodes { id } } }`,
			`{"data":{"bad":null,"allUsers":{"nodes":[{"id":1}]}},"errors":[` +
				`{"message":"invalid input syntax for type integer: \"x\"","path":["bad"],"extensions":{"code":"pg:22P02"}}]}`},
		{"permission denied", `{ allComments { nodes { id } } allUsers(orderBy: [ID_ASC], first: 1) { nodes { id } } }`,
			`{"data":{"allComments":null,"allUsers":{"nodes":[{"id":1}]}},"errors":[` +
				`{"message":"permission denied","path":["allComments"],"extensions":{"code":"pg:42501"}}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := postRaw(t, url, tc.query, nil); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
	if l := logs.String(); strings.Contains(l, "level=ERROR") {
		t.Errorf("client errors were logged at ERROR:\n%s", l)
	}
}
