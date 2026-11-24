package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

const setofSchemaSQL = `
DROP SCHEMA IF EXISTS setof_e2e CASCADE;
CREATE SCHEMA setof_e2e;
CREATE FUNCTION setof_e2e.many_users() RETURNS SETOF public.users STABLE LANGUAGE sql AS $$
    SELECT u.* FROM public.users u, generate_series(1, 2000)
$$;
CREATE FUNCTION setof_e2e.users_many_posts(u public.users) RETURNS SETOF public.posts STABLE LANGUAGE sql AS $$
    SELECT p.* FROM public.posts p, generate_series(1, 500)
$$;
CREATE FUNCTION setof_e2e.many_numbers() RETURNS SETOF integer STABLE LANGUAGE sql AS $$
    SELECT generate_series(1, 5000)
$$;
CREATE FUNCTION setof_e2e.bulk_users() RETURNS SETOF public.users VOLATILE LANGUAGE sql AS $$
    SELECT u.* FROM public.users u, generate_series(1, 2000)
$$;
CREATE FUNCTION setof_e2e.bulk_numbers() RETURNS SETOF integer VOLATILE LANGUAGE sql AS $$
    SELECT generate_series(1, 5000)
$$;
CREATE FUNCTION setof_e2e.bulk_pairs() RETURNS TABLE (a integer, b text) VOLATILE LANGUAGE sql AS $$
    SELECT g, 'x' FROM generate_series(1, 5000) g
$$;
GRANT USAGE ON SCHEMA setof_e2e TO anonymous, app_user;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA setof_e2e TO anonymous, app_user;
`

// TestSetReturningListsBounded: a list returned by a set-returning function
// (a root function, a computed column, SETOF a scalar) holds at most
// limits.max_page_size items, and the cost estimate charges it that many
// rows, so a nested connection below it is priced as through a connection.
func TestSetReturningListsBounded(t *testing.T) {
	execAll(t, testConn(t), setofSchemaSQL)
	t.Cleanup(func() { execAll(t, testConn(t), `DROP SCHEMA IF EXISTS setof_e2e CASCADE`) })
	serve := func(mutate func(*config.Config)) string {
		return testServer(t, func(c *config.Config) {
			c.Schema.Schemas = []string{"public", "setof_e2e"}
			if mutate != nil {
				mutate(c)
			}
		}).URL
	}
	count := func(t *testing.T, url, query, path string) int {
		t.Helper()
		var out struct {
			Data   map[string]json.RawMessage `json:"data"`
			Errors []json.RawMessage          `json:"errors"`
		}
		if err := json.Unmarshal([]byte(postRaw(t, url, query, nil)), &out); err != nil || len(out.Errors) > 0 {
			t.Fatalf("%s: %v %s", query, err, out.Errors)
		}
		raw := out.Data[path]
		if path == "userById" {
			var u struct {
				ManyPosts []json.RawMessage `json:"manyPosts"`
			}
			if err := json.Unmarshal(raw, &u); err != nil {
				t.Fatal(err)
			}
			return len(u.ManyPosts)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("%s: %v: %s", query, err, raw)
		}
		return len(items)
	}

	def := serve(nil)
	small := serve(func(c *config.Config) { c.Limits.MaxPageSize = 7 })
	for _, tc := range []struct{ name, query, path string }{
		{"root function", `{ manyUsers { id postsByAuthorId(first: 1) { nodes { id } } } }`, "manyUsers"},
		{"computed column", `{ userById(id: 1) { manyPosts { id } } }`, "userById"},
		{"scalar set", `{ manyNumbers }`, "manyNumbers"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := count(t, def, tc.query, tc.path); n != 100 {
				t.Errorf("default page size: %d items, want 100", n)
			}
			if n := count(t, small, tc.query, tc.path); n != 7 {
				t.Errorf("max_page_size 7: %d items, want 7", n)
			}
		})
	}

	t.Run("cost", func(t *testing.T) {
		url := serve(func(c *config.Config) { c.Limits.MaxCost = 10000 })
		for _, q := range []string{
			`{ manyUsers { id email postsByAuthorId { totalCount nodes { id title body } } } }`,
			`{ userById(id: 1) { manyPosts { id commentsByPostId { nodes { id body } } } } }`,
		} {
			if got := postRaw(t, url, q, nil); !strings.Contains(got, `"code":"cost_exceeded"`) {
				t.Errorf("%s: %s, want cost_exceeded", q, got)
			}
		}
	})
}

// TestSetReturningMutationResultBounded: the result list of a volatile SETOF
// function exposed as a custom mutation holds at most limits.max_page_size
// items, whether the payload reads tables (a second statement) or not, and
// the cost estimate charges it that many rows.
func TestSetReturningMutationResultBounded(t *testing.T) {
	execAll(t, testConn(t), setofSchemaSQL)
	t.Cleanup(func() { execAll(t, testConn(t), `DROP SCHEMA IF EXISTS setof_e2e CASCADE`) })
	serve := func(mutate func(*config.Config)) string {
		return testServer(t, func(c *config.Config) {
			c.Schema.Schemas = []string{"public", "setof_e2e"}
			if mutate != nil {
				mutate(c)
			}
		}).URL
	}
	count := func(t *testing.T, url, query, field, result string) int {
		t.Helper()
		var out struct {
			Data   map[string]map[string]json.RawMessage `json:"data"`
			Errors []json.RawMessage                     `json:"errors"`
		}
		if err := json.Unmarshal([]byte(postRaw(t, url, query, nil)), &out); err != nil || len(out.Errors) > 0 {
			t.Fatalf("%s: %v %s", query, err, out.Errors)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(out.Data[field][result], &items); err != nil {
			t.Fatalf("%s: %v: %s", query, err, out.Data[field][result])
		}
		return len(items)
	}

	def := serve(nil)
	small := serve(func(c *config.Config) { c.Limits.MaxPageSize = 7 })
	for _, tc := range []struct{ name, query, field, result string }{
		{"rows", `mutation { bulkUsers(input: {}) { users { id } } }`, "bulkUsers", "users"},
		{"rows with relations", `mutation { bulkUsers(input: {}) { users { id postsByAuthorId(first: 1) { nodes { id } } } } }`, "bulkUsers", "users"},
		{"scalars", `mutation { bulkNumbers(input: {}) { integers } }`, "bulkNumbers", "integers"},
		{"composites", `mutation { bulkPairs(input: {}) { results { a b } } }`, "bulkPairs", "results"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := count(t, def, tc.query, tc.field, tc.result); n != 100 {
				t.Errorf("default page size: %d items, want 100", n)
			}
			if n := count(t, small, tc.query, tc.field, tc.result); n != 7 {
				t.Errorf("max_page_size 7: %d items, want 7", n)
			}
		})
	}

	t.Run("cost", func(t *testing.T) {
		url := serve(func(c *config.Config) { c.Limits.MaxCost = 10000 })
		q := `mutation { bulkUsers(input: {}) { users { id email postsByAuthorId { totalCount nodes { id title body } } } } }`
		if got := postRaw(t, url, q, nil); !strings.Contains(got, `"code":"cost_exceeded"`) {
			t.Errorf("%s: %s, want cost_exceeded", q, got)
		}
	})
}
