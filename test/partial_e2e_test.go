package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

type partialResp struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
}

func postPartial(t *testing.T, url, query string, partial bool, headers map[string]string) partialResp {
	t.Helper()
	payload := map[string]any{"query": query}
	if partial {
		payload["extensions"] = map[string]any{"partialMutations": true}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, url+"/graphql", bytes.NewReader(body))
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
	raw, _ := io.ReadAll(resp.Body)
	var out partialResp
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("bad response: %v: %s", err, raw)
	}
	return out
}

func countWhere(t *testing.T, sql string, args ...any) int {
	t.Helper()
	conn := testConn(t)
	var n int
	if err := conn.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func partialPathStrings(path []any) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = fmt.Sprint(p)
	}
	return strings.Join(parts, ".")
}

func TestPartialMutations(t *testing.T) {
	run := time.Now().UnixNano()
	pattern := fmt.Sprintf("%%-%d@partial.example", run)
	sweepUsers(t, pattern)
	email := func(p string) string { return fmt.Sprintf("%s-%d@partial.example", p, run) }
	create := func(alias, em string) string {
		return fmt.Sprintf(`%s: createUser(input: {user: {email: %q}}) { user { id email } }`, alias, em)
	}
	query := func(fields ...string) string { return "mutation { " + strings.Join(fields, "\n") + " }" }

	t.Run("a failing field leaves the others committed", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true })
		res := postPartial(t, hs.URL, query(create("a", email("ok1")), create("b", email("ok1")), create("c", email("ok2"))), true, nil)
		if len(res.Errors) != 1 || partialPathStrings(res.Errors[0].Path) != "b" {
			t.Fatalf("errors = %+v, want one error at b", res.Errors)
		}
		if string(res.Data["b"]) != "null" || string(res.Data["a"]) == "null" || string(res.Data["c"]) == "null" {
			t.Fatalf("data = %v, want a and c set and b null", res.Data)
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email IN ($1, $2)`, email("ok1"), email("ok2")); n != 2 {
			t.Fatalf("committed %d users, want 2", n)
		}
	})

	t.Run("not found and compile-time failures are field errors", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true })
		res := postPartial(t, hs.URL, query(
			`gone: updateUserByEmail(input: {email: "nobody-`+fmt.Sprint(run)+`@partial.example", userPatch: {fullName: "x"}}) { user { id } }`,
			create("a", email("nf")),
		), true, nil)
		if len(res.Errors) != 1 || string(res.Data["gone"]) != "null" || string(res.Data["a"]) == "null" {
			t.Fatalf("response = %+v / %v", res.Errors, res.Data)
		}
		if partialPathStrings(res.Errors[0].Path) != "gone" || res.Errors[0].Message != `no row matched "gone"` {
			t.Fatalf("error = %+v, want no row matched at gone", res.Errors[0])
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email = $1`, email("nf")); n != 1 {
			t.Fatalf("the field after the failed one did not commit (%d)", n)
		}
	})

	t.Run("default mode stays all or nothing", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true })
		res := postPartial(t, hs.URL, query(create("a", email("aon1")), create("b", email("aon1")), create("c", email("aon2"))), false, nil)
		if len(res.Errors) == 0 {
			t.Fatal("expected the duplicate to fail")
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email IN ($1, $2)`, email("aon1"), email("aon2")); n != 0 {
			t.Fatalf("%d users survived the rollback", n)
		}
	})

	t.Run("rejected while the config is off", func(t *testing.T) {
		hs := testServer(t, nil)
		res := postPartial(t, hs.URL, query(create("a", email("off"))), true, nil)
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "partial mutations are disabled") {
			t.Fatalf("errors = %+v", res.Errors)
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email = $1`, email("off")); n != 0 {
			t.Fatal("a rejected request wrote")
		}
	})

	t.Run("rejected above transactions.partial.max_items", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true; c.TX.Partial.MaxItems = 1 })
		res := postPartial(t, hs.URL, query(create("a", email("max1")), create("b", email("max2"))), true, nil)
		if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "exceed transactions.partial.max_items (1)") {
			t.Fatalf("errors = %+v", res.Errors)
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email IN ($1, $2)`, email("max1"), email("max2")); n != 0 {
			t.Fatal("a rejected request wrote")
		}
	})

	t.Run("serialization failure retries the whole transaction", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true; c.TX.MaxRetries = 1 })
		res := postPartial(t, hs.URL, query(create("a", email("retry")), `p: retryProbe(input: {}) { integer }`), true, nil)
		if len(res.Errors) != 0 {
			t.Fatalf("errors = %+v", res.Errors)
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email = $1`, email("retry")); n != 1 {
			t.Fatalf("user committed %d times, want once", n)
		}
	})

	t.Run("serialization failure without retries commits nothing", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) { c.TX.Partial.Enabled = true })
		var res partialResp
		for range 2 {
			res = postPartial(t, hs.URL, query(create("a", email("noretry")), `p: retryProbe(input: {}) { integer }`), true, nil)
			if len(res.Errors) != 0 {
				break
			}
			if _, err := testConn(t).Exec(context.Background(), `DELETE FROM users WHERE email = $1`, email("noretry")); err != nil {
				t.Fatal(err)
			}
		}
		if len(res.Errors) != 1 || len(res.Errors[0].Path) != 0 || len(res.Data) != 0 {
			t.Fatalf("response = %+v / %v, want one transaction error and no data", res.Errors, res.Data)
		}
		if n := countWhere(t, `SELECT count(*) FROM users WHERE email = $1`, email("noretry")); n != 0 {
			t.Fatal("a failed transaction committed the other field")
		}
	})

	t.Run("RLS rejects one field, the rest commit under the same identity", func(t *testing.T) {
		hs := testServer(t, func(c *config.Config) {
			c.TX.Partial.Enabled = true
			headersIdentity(c)
		})
		title := fmt.Sprintf("partial-rls-%d", run)
		t.Cleanup(func() {
			conn, err := pgx.Connect(context.Background(), os.Getenv("PDBQ_TEST_DATABASE_URL"))
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = conn.Close(context.Background()) }()
			_, _ = conn.Exec(context.Background(), `DELETE FROM posts WHERE title LIKE $1`, title+"%")
		})
		post := func(alias string, author int, suffix string) string {
			return fmt.Sprintf(`%s: createPost(input: {post: {authorId: %d, title: %q}}) { post { id } }`, alias, author, title+suffix)
		}
		res := postPartial(t, hs.URL, query(post("mine", 1, "-a"), post("theirs", 2, "-b"), post("mine2", 1, "-c")), true, map[string]string{
			"X-Pdbq-Claim-Role":    "app_user",
			"X-Pdbq-Claim-User-Id": "1",
		})
		if len(res.Errors) != 1 || partialPathStrings(res.Errors[0].Path) != "theirs" {
			t.Fatalf("errors = %+v, want one at theirs", res.Errors)
		}
		if string(res.Data["theirs"]) != "null" || string(res.Data["mine"]) == "null" || string(res.Data["mine2"]) == "null" {
			t.Fatalf("data = %v", res.Data)
		}
		if n := countWhere(t, `SELECT count(*) FROM posts WHERE title LIKE $1`, title+"%"); n != 2 {
			t.Fatalf("committed %d posts, want 2", n)
		}

		var other int
		if err := testConn(t).QueryRow(context.Background(), `SELECT id FROM posts WHERE author_id = 2 ORDER BY id LIMIT 1`).Scan(&other); err != nil {
			t.Fatal(err)
		}
		update := func(alias string) string {
			return fmt.Sprintf(`%s: updatePostById(input: {id: %d, postPatch: {title: "hijack"}}) { post { id } }`, alias, other)
		}
		res = postPartial(t, hs.URL, query(post("x", 1, "-d"), update("c"), update("d")), true, map[string]string{
			"X-Pdbq-Claim-Role":    "app_user",
			"X-Pdbq-Claim-User-Id": "1",
		})
		if len(res.Errors) != 2 || string(res.Data["x"]) == "null" || string(res.Data["c"]) != "null" || string(res.Data["d"]) != "null" {
			t.Fatalf("response = %+v / %v, want x committed and c, d failed", res.Errors, res.Data)
		}
		for i, alias := range []string{"c", "d"} {
			if got := res.Errors[i]; partialPathStrings(got.Path) != alias || got.Message != fmt.Sprintf("no row matched %q", alias) {
				t.Errorf("error %d = %+v, want no row matched at %s", i, got, alias)
			}
		}
	})
}
