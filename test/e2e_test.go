// Package e2e runs the whole pipeline (introspect -> schema -> HTTP ->
// compiled SQL -> Postgres) against a real database.
//
// It needs PDBQ_TEST_DATABASE_URL pointing at a database initialized with
// db/init/01-schema.sql (make test-e2e brings one up); the test is
// skipped otherwise, keeping `go test ./...` hermetic.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/server"
)

func testServer(t *testing.T, mutate func(*config.Config)) *httptest.Server {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.RLS.Enabled = false
	cfg.Errors.Detail = "dev"
	// The e2e suite asserts against the verbose default naming; a dedicated
	// subtest covers simple-names separately.
	cfg.Plugins.Disabled = []string{"simple-names"}
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
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
	srv, err := server.New(cfg.Server, server.NewAuthenticator(cfg.RLS), app.Executor(built), built.SDL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return hs
}

type gqlResp struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func post(t *testing.T, url, query string, vars map[string]any) gqlResp {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	r, err := httpPost(url+"/graphql", body)
	if err != nil {
		t.Fatal(err)
	}
	var out gqlResp
	if err := json.Unmarshal(r, &out); err != nil {
		t.Fatalf("bad response: %v: %s", err, r)
	}
	return out
}

func httpPost(url string, body []byte) ([]byte, error) {
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func httpGet(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func TestQueries(t *testing.T) {
	hs := testServer(t, nil)

	t.Run("single row by unique", func(t *testing.T) {
		res := post(t, hs.URL, `{userByEmail(email: "ada@example.com") {fullName mood tags}}`, nil)
		requireNoErrors(t, res)
		want := `{"fullName":"Ada Lovelace","mood":"HAPPY","tags":["admin","founder"]}`
		got := normalize(t, res.Data["userByEmail"])
		if got != normalizeStr(t, want) {
			t.Errorf("got %s want %s", got, want)
		}
	})

	t.Run("skip and include directives", func(t *testing.T) {
		res := post(t, hs.URL, `query ($more: Boolean!) {
			userByEmail(email: "ada@example.com") {
				fullName
				mood @skip(if: true)
				tags @include(if: $more)
			}
			allPosts(first: 1) @skip(if: true) { totalCount }
		}`, map[string]any{"more": false})
		requireNoErrors(t, res)
		got := normalize(t, res.Data["userByEmail"])
		want := `{"fullName":"Ada Lovelace"}`
		if got != normalizeStr(t, want) {
			t.Errorf("got %s want %s", got, want)
		}
		if _, present := res.Data["allPosts"]; present {
			t.Errorf("allPosts should be absent when the root field is skipped, got %s", res.Data["allPosts"])
		}
	})

	t.Run("relations and filters", func(t *testing.T) {
		res := post(t, hs.URL, `{
			allUsers(filter: {email: {endsWith: "@example.com"}}, orderBy: [EMAIL_ASC], first: 2) {
				nodes {
					email
					postsByAuthorId(orderBy: [ID_ASC]) { nodes { title published } }
				}
			}
		}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			Nodes []struct {
				Email string `json:"email"`
				Posts struct {
					Nodes []struct {
						Title string `json:"title"`
					} `json:"nodes"`
				} `json:"postsByAuthorId"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &conn)
		if len(conn.Nodes) != 2 || conn.Nodes[0].Email != "ada@example.com" {
			t.Fatalf("unexpected users: %+v", conn)
		}
		if len(conn.Nodes[0].Posts.Nodes) != 2 {
			t.Errorf("ada should have 2 posts (RLS off), got %d", len(conn.Nodes[0].Posts.Nodes))
		}
	})

	t.Run("offset pagination", func(t *testing.T) {
		// Regression: the keyset trim filter compared __rn (numbered before
		// OFFSET applies) against `limit`, so first+offset returned
		// max(0, first-offset) rows instead of first.
		type conn struct {
			Nodes []struct {
				Email string `json:"email"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage     bool `json:"hasNextPage"`
				HasPreviousPage bool `json:"hasPreviousPage"`
			} `json:"pageInfo"`
		}
		page := func(offset int) conn {
			res := post(t, hs.URL, fmt.Sprintf(`{
				allUsers(orderBy: [EMAIL_ASC], first: 1, offset: %d) {
					nodes { email }
					pageInfo { hasNextPage hasPreviousPage }
				}
			}`, offset), nil)
			requireNoErrors(t, res)
			var c conn
			mustUnmarshal(t, res.Data["allUsers"], &c)
			return c
		}
		for i, want := range []struct {
			email            string
			hasNext, hasPrev bool
		}{
			{"ada@example.com", true, false},
			{"alan@example.com", true, true},
			{"grace@example.com", false, true},
		} {
			c := page(i)
			if len(c.Nodes) != 1 || c.Nodes[0].Email != want.email {
				t.Fatalf("offset %d: want [%s], got %+v", i, want.email, c.Nodes)
			}
			if c.PageInfo.HasNextPage != want.hasNext || c.PageInfo.HasPreviousPage != want.hasPrev {
				t.Errorf("offset %d: pageInfo got next=%v prev=%v want next=%v prev=%v",
					i, c.PageInfo.HasNextPage, c.PageInfo.HasPreviousPage, want.hasNext, want.hasPrev)
			}
		}
		if c := page(3); len(c.Nodes) != 0 {
			t.Errorf("offset past end: want 0 rows, got %+v", c.Nodes)
		}
	})

	t.Run("set-returning computed columns", func(t *testing.T) {
		res := post(t, hs.URL, `{userByEmail(email: "ada@example.com") {
			tagWords
			recentPosts(n: 1) { title }
		}}`, nil)
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"tagWords":["admin","founder"],"recentPosts":[{"title":"Unpublished draft"}]}`)
		if got := normalize(t, res.Data["userByEmail"]); got != want {
			t.Errorf("got %s want %s", got, want)
		}
	})

	t.Run("distinctOn", func(t *testing.T) {
		res := post(t, hs.URL, `{
			allUsers(distinctOn: [MOOD], orderBy: [MOOD_ASC]) {
				totalCount
				nodes { mood }
			}
		}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			TotalCount int `json:"totalCount"`
			Nodes      []struct {
				Mood *string `json:"mood"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &conn)
		if len(conn.Nodes) == 0 || len(conn.Nodes) != conn.TotalCount {
			t.Fatalf("distinct nodes (%d) should match totalCount (%d)", len(conn.Nodes), conn.TotalCount)
		}
		seen := map[string]bool{}
		for _, n := range conn.Nodes {
			key := "<null>"
			if n.Mood != nil {
				key = *n.Mood
			}
			if seen[key] {
				t.Fatalf("duplicate mood %q in distinctOn result: %+v", key, conn.Nodes)
			}
			seen[key] = true
		}
	})

	t.Run("jsonb path filters", func(t *testing.T) {
		res := post(t, hs.URL, `{
			allUsers(filter: {settings: {pathExists: "$.theme", pathMatch: "$.theme == \"dark\""}}) {
				nodes { email }
			}
		}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			Nodes []struct {
				Email string `json:"email"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &conn)
		if len(conn.Nodes) != 1 || conn.Nodes[0].Email != "ada@example.com" {
			t.Fatalf("expected only ada to match jsonb path filter, got: %+v", conn)
		}
	})

	t.Run("connection pagination", func(t *testing.T) {
		res := post(t, hs.URL, `{
			allUsers(first: 2) {
				totalCount
				edges { cursor node { email } }
				pageInfo { hasNextPage hasPreviousPage endCursor }
			}
		}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			TotalCount int `json:"totalCount"`
			Edges      []struct {
				Cursor string `json:"cursor"`
			} `json:"edges"`
			PageInfo struct {
				HasNextPage     bool   `json:"hasNextPage"`
				HasPreviousPage bool   `json:"hasPreviousPage"`
				EndCursor       string `json:"endCursor"`
			} `json:"pageInfo"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &conn)
		if conn.TotalCount < 3 || !conn.PageInfo.HasNextPage || conn.PageInfo.HasPreviousPage {
			t.Fatalf("unexpected connection: %+v", conn)
		}
		if len(conn.Edges) != 2 || conn.Edges[1].Cursor != conn.PageInfo.EndCursor {
			t.Fatalf("edge cursors inconsistent: %+v", conn)
		}
		// Follow the cursor.
		res = post(t, hs.URL, fmt.Sprintf(`{
			allUsers(first: 10, after: %q) { nodes { email } pageInfo { hasPreviousPage } }
		}`, conn.PageInfo.EndCursor), nil)
		requireNoErrors(t, res)
		if !strings.Contains(string(res.Data["allUsers"]), "alan@example.com") {
			t.Errorf("cursor page should contain remaining users: %s", res.Data["allUsers"])
		}
		if !strings.Contains(string(res.Data["allUsers"]), `"hasPreviousPage":true`) {
			t.Errorf("page after a cursor should report hasPreviousPage: %s", res.Data["allUsers"])
		}
	})

	t.Run("node round-trip", func(t *testing.T) {
		res := post(t, hs.URL, `{userByEmail(email: "ada@example.com") { nodeId id }}`, nil)
		requireNoErrors(t, res)
		var row struct {
			NodeID string `json:"nodeId"`
			ID     int    `json:"id"`
		}
		mustUnmarshal(t, res.Data["userByEmail"], &row)
		if row.NodeID == "" {
			t.Fatal("missing nodeId")
		}
		res = post(t, hs.URL, `query($id: ID!) {
			node(nodeId: $id) { __typename nodeId ... on User { id email } ... on Post { title } }
		}`, map[string]any{"id": row.NodeID})
		requireNoErrors(t, res)
		var node struct {
			Typename string `json:"__typename"`
			NodeID   string `json:"nodeId"`
			ID       int    `json:"id"`
			Email    string `json:"email"`
		}
		mustUnmarshal(t, res.Data["node"], &node)
		if node.Typename != "User" || node.ID != row.ID || node.Email != "ada@example.com" {
			t.Errorf("node round-trip mismatch: %+v", node)
		}
		if node.NodeID != row.NodeID {
			t.Errorf("nodeId not stable across node(): %q vs %q", node.NodeID, row.NodeID)
		}
		// Unknown type resolves to null, not an error.
		res = post(t, hs.URL, `{node(nodeId: "WyJOb3BlIiwxXQ==") { __typename }}`, nil)
		requireNoErrors(t, res)
		if string(res.Data["node"]) != "null" {
			t.Errorf("unknown-type nodeId should be null: %s", res.Data["node"])
		}
	})

	t.Run("keyset page walk", func(t *testing.T) {
		// mood is nullable and indexed: exercises the null-aware keyset
		// predicate. Walk one row at a time and compare with the full fetch.
		full := fetchIDs(t, hs.URL, `{allUsers(first: 100, orderBy: [MOOD_ASC]) { nodes { id } }}`)
		if len(full) < 3 {
			t.Fatalf("fixture too small: %v", full)
		}
		var walked []int
		after := ""
		for i := 0; i <= len(full)+1; i++ {
			vars := map[string]any{}
			if after != "" {
				vars["a"] = after
			}
			res := post(t, hs.URL, `query($a: Cursor) {
				allUsers(first: 1, after: $a, orderBy: [MOOD_ASC]) {
					nodes { id }
					pageInfo { hasNextPage endCursor }
				}
			}`, vars)
			requireNoErrors(t, res)
			var page struct {
				Nodes    []struct{ ID int }
				PageInfo struct {
					HasNextPage bool
					EndCursor   string
				}
			}
			mustUnmarshal(t, res.Data["allUsers"], &page)
			for _, n := range page.Nodes {
				walked = append(walked, n.ID)
			}
			if !page.PageInfo.HasNextPage {
				break
			}
			after = page.PageInfo.EndCursor
		}
		if fmt.Sprint(walked) != fmt.Sprint(full) {
			t.Errorf("page walk diverged:\nfull:   %v\nwalked: %v", full, walked)
		}
	})

	t.Run("backward pagination", func(t *testing.T) {
		res := post(t, hs.URL, `{allUsers(first: 100, orderBy: [EMAIL_ASC]) { edges { cursor node { email } } }}`, nil)
		requireNoErrors(t, res)
		var conn struct {
			Edges []struct {
				Cursor string `json:"cursor"`
				Node   struct {
					Email string `json:"email"`
				} `json:"node"`
			} `json:"edges"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &conn)
		if len(conn.Edges) < 3 {
			t.Fatalf("fixture too small: %+v", conn)
		}
		lastCursor := conn.Edges[len(conn.Edges)-1].Cursor
		res = post(t, hs.URL, fmt.Sprintf(`{
			allUsers(last: 2, before: %q, orderBy: [EMAIL_ASC]) {
				nodes { email }
				pageInfo { hasNextPage hasPreviousPage }
			}
		}`, lastCursor), nil)
		requireNoErrors(t, res)
		var back struct {
			Nodes []struct {
				Email string `json:"email"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage     bool `json:"hasNextPage"`
				HasPreviousPage bool `json:"hasPreviousPage"`
			} `json:"pageInfo"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &back)
		if len(back.Nodes) != 2 {
			t.Fatalf("last: 2 should return 2 rows: %+v", back)
		}
		// Rows stay in forward order: the two immediately before the anchor.
		want0 := conn.Edges[len(conn.Edges)-3].Node.Email
		want1 := conn.Edges[len(conn.Edges)-2].Node.Email
		if back.Nodes[0].Email != want0 || back.Nodes[1].Email != want1 {
			t.Errorf("backward page mismatch: got %+v want [%s %s]", back.Nodes, want0, want1)
		}
		if !back.PageInfo.HasNextPage {
			t.Error("before-page should report hasNextPage")
		}
	})

	t.Run("composite columns", func(t *testing.T) {
		res := post(t, hs.URL, `{userByEmail(email: "ada@example.com") {
			address { street city mood __typename }
			prevAddresses { city mood }
		}}`, nil)
		requireNoErrors(t, res)
		want := `{
			"address": {"street": "12 St James Sq", "city": "London", "mood": "HAPPY", "__typename": "Address"},
			"prevAddresses": [{"city": "Surrey", "mood": "OK"}]
		}`
		if got := normalize(t, res.Data["userByEmail"]); got != normalizeStr(t, want) {
			t.Errorf("got %s want %s", got, normalizeStr(t, want))
		}
		// NULL composite and NULL composite array stay null.
		res = post(t, hs.URL, `{userByEmail(email: "grace@example.com") { address { city } prevAddresses { city } }}`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["userByEmail"]); got != `{"address":null,"prevAddresses":null}` {
			t.Errorf("null composites: got %s", got)
		}
	})

	t.Run("function", func(t *testing.T) {
		res := post(t, hs.URL, `{searchPosts(term: "compilers") { title author { email } }}`, nil)
		requireNoErrors(t, res)
		if !strings.Contains(string(res.Data["searchPosts"]), "grace@example.com") {
			t.Errorf("searchPosts: %s", res.Data["searchPosts"])
		}
	})

	t.Run("computed columns", func(t *testing.T) {
		// postCount is bigint -> BigInt (string-serialized). Ada has 2 posts.
		res := post(t, hs.URL, `{userByEmail(email: "ada@example.com") { postCount }}`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["userByEmail"]); got != `{"postCount":"2"}` {
			t.Errorf("postCount: got %s", got)
		}
		// Extra function argument surfaces as a GraphQL field argument.
		res = post(t, hs.URL, `{postById(id: 1) { excerpt(maxChars: 5) }}`, nil)
		requireNoErrors(t, res)
		if got := normalize(t, res.Data["postById"]); got != `{"excerpt":"First"}` {
			t.Errorf("excerpt: got %s", got)
		}
	})
}

// TestAdvancedFilters covers the advanced-filters built-in: relation filters
// (EXISTS subqueries) and computed-column filtering/ordering.
func TestAdvancedFilters(t *testing.T) {
	hs := testServer(t, nil)

	t.Run("forward relation filter", func(t *testing.T) {
		got := emails(t, hs.URL, `{allPosts(filter: {author: {email: {equalTo: "grace@example.com"}}}) { nodes { title } }}`, "allPosts", "title")
		if len(got) != 1 || got[0] != "Compilers 101" {
			t.Errorf("author filter: got %v", got)
		}
	})

	t.Run("backward relation quantifiers", func(t *testing.T) {
		got := emails(t, hs.URL, `{allUsers(filter: {postsByAuthorId: {some: {title: {startsWith: "Compilers"}}}}) { nodes { email } }}`, "allUsers", "email")
		if len(got) != 1 || got[0] != "grace@example.com" {
			t.Errorf("some: got %v", got)
		}
		got = emails(t, hs.URL, `{allUsers(filter: {postsByAuthorId: {none: {title: {startsWith: "Notes"}}}}, orderBy: [EMAIL_ASC]) { nodes { email } }}`, "allUsers", "email")
		if len(got) != 2 || got[0] != "alan@example.com" || got[1] != "grace@example.com" {
			t.Errorf("none: got %v", got)
		}
		got = emails(t, hs.URL, `{allUsers(filter: {postsByAuthorId: {every: {title: {notEqualTo: "Unpublished draft"}}}}, orderBy: [EMAIL_ASC]) { nodes { email } }}`, "allUsers", "email")
		if len(got) != 2 || got[0] != "alan@example.com" || got[1] != "grace@example.com" {
			t.Errorf("every: got %v", got)
		}
	})

	t.Run("computed column filter and order", func(t *testing.T) {
		got := emails(t, hs.URL, `{allUsers(filter: {postCount: {greaterThanOrEqualTo: "2"}}) { nodes { email } }}`, "allUsers", "email")
		if len(got) != 1 || got[0] != "ada@example.com" {
			t.Errorf("postCount filter: got %v", got)
		}
		got = emails(t, hs.URL, `{allUsers(orderBy: [POST_COUNT_DESC, EMAIL_ASC], first: 3) { nodes { email } }}`, "allUsers", "email")
		want := []string{"ada@example.com", "alan@example.com", "grace@example.com"}
		if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Errorf("computed order: got %v want %v", got, want)
		}
	})

	t.Run("computed order keyset pagination", func(t *testing.T) {
		res := post(t, hs.URL, `{allUsers(orderBy: [POST_COUNT_DESC, EMAIL_ASC], first: 1) {
			nodes { email } pageInfo { endCursor hasNextPage }
		}}`, nil)
		requireNoErrors(t, res)
		var page struct {
			Nodes    []struct{ Email string }
			PageInfo struct {
				EndCursor   string `json:"endCursor"`
				HasNextPage bool   `json:"hasNextPage"`
			} `json:"pageInfo"`
		}
		mustUnmarshal(t, res.Data["allUsers"], &page)
		if len(page.Nodes) != 1 || page.Nodes[0].Email != "ada@example.com" || !page.PageInfo.HasNextPage {
			t.Fatalf("first page: %+v", page)
		}
		got := emails(t, hs.URL, fmt.Sprintf(`{allUsers(orderBy: [POST_COUNT_DESC, EMAIL_ASC], first: 1, after: %q) { nodes { email } }}`, page.PageInfo.EndCursor), "allUsers", "email")
		if len(got) != 1 || got[0] != "alan@example.com" {
			t.Errorf("after computed-order cursor: got %v", got)
		}
	})

	t.Run("disabled plugin removes the surface", func(t *testing.T) {
		hs := testServer(t, func(cfg *config.Config) {
			cfg.Plugins.Disabled = append(cfg.Plugins.Disabled, "advanced-filters")
		})
		res := post(t, hs.URL, `{allUsers(filter: {postCount: {greaterThanOrEqualTo: "2"}}) { nodes { email } }}`, nil)
		if len(res.Errors) == 0 {
			t.Error("postCount filter should be rejected when advanced-filters is disabled")
		}
	})
}

// emails extracts one string field from each node of a connection result.
func emails(t *testing.T, url, query, root, field string) []string {
	t.Helper()
	res := post(t, url, query, nil)
	requireNoErrors(t, res)
	var conn struct {
		Nodes []map[string]string `json:"nodes"`
	}
	mustUnmarshal(t, res.Data[root], &conn)
	out := make([]string, len(conn.Nodes))
	for i, n := range conn.Nodes {
		out[i] = n[field]
	}
	return out
}

func TestMutationsAndRollback(t *testing.T) {
	hs := testServer(t, nil)
	// Unique emails keep reruns against a persistent dev DB idempotent.
	run := time.Now().UnixNano()
	email := func(prefix string) string { return fmt.Sprintf("%s-%d@example.com", prefix, run) }

	res := post(t, hs.URL, fmt.Sprintf(`mutation {
		createUser(input: {email: %q, mood: OK}) { user { id email } }
	}`, email("e2e")), nil)
	requireNoErrors(t, res)
	var payload struct {
		User struct {
			ID    int    `json:"id"`
			Email string `json:"email"`
		} `json:"user"`
	}
	mustUnmarshal(t, res.Data["createUser"], &payload)
	id := payload.User.ID

	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		updateUserById(id: %d, patch: {fullName: "E2E"}) { user { fullName } }
	}`, id), nil)
	requireNoErrors(t, res)

	// Update by unique constraint instead of the primary key.
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		updateUserByEmail(email: %q, patch: {fullName: "E2E ByEmail"}) { user { id fullName } }
	}`, payload.User.Email), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["updateUserByEmail"]); got != fmt.Sprintf(`{"user":{"fullName":"E2E ByEmail","id":%d}}`, id) {
		t.Errorf("update by unique: got %s", got)
	}

	// Upsert: first call inserts, second updates the same row on the email
	// unique conflict.
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		upsertUserByEmail(input: {email: %q, fullName: "Upsert v1"}) { user { fullName } }
	}`, email("upsert")), nil)
	requireNoErrors(t, res)
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		upsertUserByEmail(input: {email: %q, fullName: "Upsert v2"}) { user { fullName } }
	}`, email("upsert")), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["upsertUserByEmail"]); got != `{"user":{"fullName":"Upsert v2"}}` {
		t.Errorf("upsert on conflict: got %s", got)
	}
	post(t, hs.URL, fmt.Sprintf(`mutation { deleteUserByEmail(email: %q) { user { id } } }`, email("upsert")), nil)

	// Bulk mutations: multi-row insert, filtered update, filtered delete.
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		createUsers(input: [
			{email: %q, fullName: "Bulk One"}
			{email: %q, fullName: "Bulk Two"}
		]) { users { fullName } affectedCount }
	}`, email("bulk1"), email("bulk2")), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["createUsers"]); got != `{"affectedCount":2,"users":[{"fullName":"Bulk One"},{"fullName":"Bulk Two"}]}` {
		t.Errorf("bulk create: got %s", got)
	}
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		updateUsers(filter: {email: {in: [%q, %q]}}, patch: {fullName: "Bulk Updated"}) { affectedCount }
	}`, email("bulk1"), email("bulk2")), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["updateUsers"]); got != `{"affectedCount":2}` {
		t.Errorf("bulk update: got %s", got)
	}
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		deleteUsers(filter: {email: {in: [%q, %q]}}) { users { fullName } affectedCount }
	}`, email("bulk1"), email("bulk2")), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["deleteUsers"]); got != `{"affectedCount":2,"users":[{"fullName":"Bulk Updated"},{"fullName":"Bulk Updated"}]}` {
		t.Errorf("bulk delete: got %s", got)
	}

	// Composite input round-trip: create with an object value, read it back
	// structured (including the enum attribute and the composite array).
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		updateUserById(id: %d, patch: {
			address: {street: "221B Baker St", city: "London", mood: HAPPY}
			prevAddresses: [{city: "Cambridge"}, {city: "Princeton", mood: SAD}]
		}) { user { address { street city mood } prevAddresses { city mood } } }
	}`, id), nil)
	requireNoErrors(t, res)
	wantAddr := normalizeStr(t, `{"user": {
		"address": {"street": "221B Baker St", "city": "London", "mood": "HAPPY"},
		"prevAddresses": [{"city": "Cambridge", "mood": null}, {"city": "Princeton", "mood": "SAD"}]
	}}`)
	if got := normalize(t, res.Data["updateUserById"]); got != wantAddr {
		t.Errorf("composite round-trip: got %s want %s", got, wantAddr)
	}

	// Computed column on a mutation payload: exercises the ROW(...)::table
	// cast against a real RETURNING * CTE row source.
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		updateUserById(id: %d, patch: {fullName: "E2E Computed"}) { user { postCount } }
	}`, id), nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["updateUserById"]); got != `{"user":{"postCount":"0"}}` {
		t.Errorf("computed on mutation payload: got %s", got)
	}

	// Nested mutation: creates a user and two posts atomically.
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		createUser(input: {
			email: %q
			postsByAuthorId: {create: [{title: "n1"}, {title: "n2"}]}
		}) { user { id postsByAuthorId { nodes { title } } } }
	}`, email("nested")), nil)
	requireNoErrors(t, res)
	if c := strings.Count(string(res.Data["createUser"]), `"title"`); c != 2 {
		t.Errorf("nested create should return 2 posts: %s", res.Data["createUser"])
	}

	// Induced failure: duplicate email inside a nested mutation must roll
	// back the parent insert too (single statement + tx).
	res = post(t, hs.URL, fmt.Sprintf(`mutation {
		createUser(input: {
			email: %q
			postsByAuthorId: {create: [{title: "x", authorId: 999999}]}
		}) { user { id } }
	}`, email("rollback")), nil)
	if len(res.Errors) == 0 {
		t.Fatal("expected nested mutation failure")
	}
	res = post(t, hs.URL, fmt.Sprintf(`{userByEmail(email: %q) { id }}`, email("rollback")), nil)
	requireNoErrors(t, res)
	if string(res.Data["userByEmail"]) != "null" {
		t.Errorf("rollback leaked the parent row: %s", res.Data["userByEmail"])
	}

	// Cleanup.
	post(t, hs.URL, fmt.Sprintf(`mutation { deleteUserById(id: %d) { user { id } } }`, id), nil)
}

func TestPerRequestTransaction(t *testing.T) {
	hs := testServer(t, func(c *config.Config) { c.TX.PerRequest = true })
	// Multiple root fields (queries included) run inside one transaction and
	// read one snapshot; behaviorally this must stay indistinguishable for a
	// read-only request.
	res := post(t, hs.URL, `{
		a: userByEmail(email: "ada@example.com") { id }
		b: allUsers(first: 1) { totalCount }
	}`, nil)
	requireNoErrors(t, res)
	if string(res.Data["a"]) == "null" || len(res.Data["b"]) == 0 {
		t.Fatalf("per-request tx broke reads: %v", res.Data)
	}
	// Mutations still roll back on error under the per-request policy.
	res = post(t, hs.URL, `mutation {
		one: createUser(input: {email: "tx1@pr.example"}) { user { id } }
		two: createUser(input: {email: "tx1@pr.example"}) { user { id } }
	}`, nil)
	if len(res.Errors) == 0 {
		t.Fatal("expected duplicate email failure")
	}
	res = post(t, hs.URL, `{userByEmail(email: "tx1@pr.example") { id }}`, nil)
	requireNoErrors(t, res)
	if string(res.Data["userByEmail"]) != "null" {
		t.Errorf("per-request tx failed to roll back first mutation: %s", res.Data["userByEmail"])
	}
}

func TestSerializationRetry(t *testing.T) {
	// retry_probe() raises SQLSTATE 40001 on odd sequence values; the
	// sequence advance survives the rollback, so a single retry always
	// lands on an even value and succeeds.
	hs := testServer(t, func(c *config.Config) { c.TX.MaxRetries = 1 })
	res := post(t, hs.URL, `mutation { retryProbe(input: {}) { result } }`, nil)
	requireNoErrors(t, res)

	// Without retries the next call (odd again) surfaces the failure.
	hs2 := testServer(t, nil)
	res = post(t, hs2.URL, `mutation { retryProbe(input: {}) { result } }`, nil)
	if len(res.Errors) == 0 {
		t.Fatal("expected serialization failure without retries")
	}
}

// TestJWTMint covers rls.auth.jwt_type: a function returning the configured
// composite yields a signed HS256 token, and the token round-trips through
// the verifier into RLS claims.
func TestJWTMint(t *testing.T) {
	const secret = "e2e-mint-secret"
	hs := testServer(t, func(c *config.Config) {
		c.RLS.Enabled = true
		c.RLS.Auth.Mode = "jwt"
		c.RLS.Auth.JWTSecret = secret
		c.RLS.Auth.JWTType = "public.jwt"
		c.RLS.Auth.JWTIssuer = "pdbq-e2e"
	})

	// Anonymous request mints a token via the authenticate() function.
	res := post(t, hs.URL, `mutation { authenticate(input: {userEmail: "ada@example.com"}) { result } }`, nil)
	requireNoErrors(t, res)
	var payload struct {
		Result string `json:"result"`
	}
	mustUnmarshal(t, res.Data["authenticate"], &payload)
	if strings.Count(payload.Result, ".") != 2 {
		t.Fatalf("expected a JWT, got %q", payload.Result)
	}

	// The token verifies with the same secret and carries the composite's
	// fields plus the configured issuer.
	tok, err := jwt.Parse(payload.Result, func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("pdbq-e2e"), jwt.WithExpirationRequired())
	if err != nil {
		t.Fatalf("token does not verify: %v", err)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["role"] != "app_user" || claims["user_id"] != float64(1) {
		t.Errorf("unexpected claims: %v", claims)
	}

	// Bearer round trip: the minted token switches the role and exposes
	// user_id to RLS, revealing ada's unpublished draft.
	countPosts := func(headers map[string]string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": `{allPosts(first: 100) { nodes { id } }}`})
		req, err := http.NewRequest("POST", hs.URL+"/graphql", bytes.NewReader(body))
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
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out gqlResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("bad response: %s", raw)
		}
		if len(out.Errors) > 0 {
			t.Fatalf("errors: %+v", out.Errors)
		}
		var conn struct {
			Nodes []any `json:"nodes"`
		}
		mustUnmarshal(t, out.Data["allPosts"], &conn)
		return len(conn.Nodes)
	}
	if got := countPosts(nil); got != 3 {
		t.Errorf("anonymous should see 3 published posts, got %d", got)
	}
	if got := countPosts(map[string]string{"Authorization": "Bearer " + payload.Result}); got != 4 {
		t.Errorf("ada's token should reveal 4 posts, got %d", got)
	}

	// Unknown email: strictly-null composite stays null, no token minted.
	res = post(t, hs.URL, `mutation { authenticate(input: {userEmail: "nobody@example.com"}) { result } }`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["authenticate"]); got != `{"result":null}` {
		t.Errorf("unknown email should yield null result, got %s", got)
	}
}

// TestFunctionMutations covers the Relay-classic shape volatile functions
// get: fn(input: FnInput!): FnPayload! { result clientMutationId }.
func TestFunctionMutations(t *testing.T) {
	hs := testServer(t, nil)

	// Scalar result + clientMutationId passthrough + __typename.
	res := post(t, hs.URL, `mutation {
	  addNumbers(input: {a: 2, b: 40, clientMutationId: "cm-1"}) {
	    __typename result clientMutationId
	  }
	}`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["addNumbers"]); got != `{"__typename":"AddNumbersPayload","clientMutationId":"cm-1","result":42}` {
		t.Errorf("addNumbers: got %s", got)
	}

	// Omitted args arrive as SQL NULL; unsent clientMutationId is null.
	res = post(t, hs.URL, `mutation { addNumbers(input: {a: 2}) { result clientMutationId } }`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["addNumbers"]); got != `{"clientMutationId":null,"result":null}` {
		t.Errorf("addNumbers null arg: got %s", got)
	}

	// Table-returning function: result supports a full selection with relations.
	res = post(t, hs.URL, `mutation {
	  publishPost(input: {postId: 2}) {
	    result { title published author { email } }
	  }
	}`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["publishPost"]); !strings.Contains(got, `"published":true`) ||
		!strings.Contains(got, `"email":"ada@example.com"`) {
		t.Errorf("publishPost: got %s", got)
	}

	// Array-typed arguments arrive as GraphQL lists.
	res = post(t, hs.URL, `mutation { wordLengths(input: {words: ["a", "bb", "ccc"]}) { result } }`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["wordLengths"]); got != `{"result":3}` {
		t.Errorf("wordLengths: got %s", got)
	}

	// The call runs even when result is not selected.
	res = post(t, hs.URL, `mutation { unpublishPost(input: {postId: 2}) { clientMutationId } }`, nil)
	requireNoErrors(t, res)
	res = post(t, hs.URL, `{postById(id: 2) { published }}`, nil)
	requireNoErrors(t, res)
	if got := normalize(t, res.Data["postById"]); got != `{"published":false}` {
		t.Errorf("unpublish side effect missing: got %s", got)
	}
}

func TestSDLAndHealth(t *testing.T) {
	hs := testServer(t, func(c *config.Config) { c.Server.ExposeSchema = true })
	body, err := httpGet(hs.URL + "/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("type User")) {
		t.Error("SDL endpoint missing type User")
	}
	body, err = httpGet(hs.URL + "/healthz")
	if err != nil || !bytes.Contains(body, []byte("ok")) {
		t.Errorf("healthz: %s err=%v", body, err)
	}
}

// TestRLSMatrix exercises role x row visibility: anonymous sees only
// published posts; app_user with user_id=1 also sees their own draft.
func TestRLSMatrix(t *testing.T) {
	hs := testServer(t, func(cfg *config.Config) {
		cfg.RLS.Enabled = true
		cfg.RLS.Auth.Mode = "headers"
		cfg.RLS.RoleClaim = "role"
	})

	countPosts := func(headers map[string]string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": `{allPosts(first: 100) { nodes { id } }}`})
		req, err := http.NewRequest("POST", hs.URL+"/graphql", bytes.NewReader(body))
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
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var out gqlResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("bad response: %s", raw)
		}
		if len(out.Errors) > 0 {
			t.Fatalf("errors: %+v", out.Errors)
		}
		var conn struct {
			Nodes []any `json:"nodes"`
		}
		mustUnmarshal(t, out.Data["allPosts"], &conn)
		return len(conn.Nodes)
	}

	anon := countPosts(nil) // no claim headers -> anonymous role
	own := countPosts(map[string]string{
		"X-Pdbq-Claim-Role":    "app_user",
		"X-Pdbq-Claim-User-Id": "1",
	})
	other := countPosts(map[string]string{
		"X-Pdbq-Claim-Role":    "app_user",
		"X-Pdbq-Claim-User-Id": "2",
	})
	if anon != 3 {
		t.Errorf("anonymous should see 3 published posts, got %d", anon)
	}
	if own != 4 {
		t.Errorf("author 1 should see 4 posts (3 published + own draft), got %d", own)
	}
	if other != 3 {
		t.Errorf("author 2 should see 3 posts, got %d", other)
	}
}

// fetchIDs runs a connection query and returns nodes[].id in order.
func fetchIDs(t *testing.T, url, query string) []int {
	t.Helper()
	res := post(t, url, query, nil)
	requireNoErrors(t, res)
	for _, raw := range res.Data {
		var conn struct {
			Nodes []struct {
				ID int `json:"id"`
			} `json:"nodes"`
		}
		mustUnmarshal(t, raw, &conn)
		out := make([]int, len(conn.Nodes))
		for i, n := range conn.Nodes {
			out[i] = n.ID
		}
		return out
	}
	t.Fatal("no data")
	return nil
}

func requireNoErrors(t *testing.T, res gqlResp) {
	t.Helper()
	if len(res.Errors) > 0 {
		t.Fatalf("GraphQL errors: %+v", res.Errors)
	}
}

func mustUnmarshal(t *testing.T, raw json.RawMessage, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
}

func normalize(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func normalizeStr(t *testing.T, s string) string {
	return normalize(t, json.RawMessage(s))
}

// TestSmartComments exercises the smart-comments path end to end: COMMENT ON
// statements applied to the live database change the generated schema
// (rename, relation field name, mutation gating, description stripping).
func TestSmartComments(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)

	comments := map[string]string{
		`COLUMN users.full_name`:                   `E'@name display_name\nThe name shown in the UI.'`,
		`CONSTRAINT posts_author_id_fkey ON posts`: `'@fieldName writer'`,
		`TABLE posts`:                              `'@omit delete'`,
	}
	for target, comment := range comments {
		if _, err := conn.Exec(ctx, fmt.Sprintf("COMMENT ON %s IS %s", target, comment)); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for target := range comments {
			_, _ = conn.Exec(ctx, fmt.Sprintf("COMMENT ON %s IS NULL", target))
		}
	})

	hs := testServer(t, nil)

	// @name and @fieldName reshape query fields; the tag lines never reach
	// the SDL, the trailing description does.
	res := post(t, hs.URL, `{ allPosts(first: 1, orderBy: [ID_ASC]) { nodes { writer { displayName } } } }`, nil)
	requireNoErrors(t, res)
	if !strings.Contains(string(res.Data["allPosts"]), "displayName") {
		t.Fatalf("renamed field missing: %s", res.Data["allPosts"])
	}
	res = post(t, hs.URL, `{ __type(name: "User") { fields { name description } } }`, nil)
	requireNoErrors(t, res)
	typ := string(res.Data["__type"])
	if strings.Contains(typ, "@name") || !strings.Contains(typ, "The name shown in the UI.") {
		t.Fatalf("smart tags must be stripped from descriptions: %s", typ)
	}

	// @omit delete removes the mutation while update survives.
	res = post(t, hs.URL, `{ __type(name: "Mutation") { fields { name } } }`, nil)
	requireNoErrors(t, res)
	mut := string(res.Data["__type"])
	if strings.Contains(mut, "deletePostById") || !strings.Contains(mut, "updatePostById") {
		t.Fatalf("@omit delete not applied: %s", mut)
	}
}
