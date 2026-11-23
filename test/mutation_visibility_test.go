package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// expectField fails t unless the root field of res normalizes to want.
func expectField(t *testing.T, res gqlResp, field, want string) {
	t.Helper()
	requireNoErrors(t, res)
	if got, w := normalize(t, res.Data[field]), normalizeStr(t, want); got != w {
		t.Errorf("%s: got %s\nwant %s", field, got, w)
	}
}

// TestMutationVisibility checks that a plain (non-nested) mutation's payload
// sees the mutated table as it stands after the statement when a relation
// leads back to it: created rows included, updated rows in their new
// version only (also when the update changed the primary key), deleted rows
// gone, across totalCount, nodes and filters.
func TestMutationVisibility(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE mv_authors (id integer PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE mv_posts (id integer PRIMARY KEY, author_id integer NOT NULL REFERENCES mv_authors (id), title text NOT NULL, code text UNIQUE)`,
		`CREATE INDEX mv_posts_author_id_idx ON mv_posts (author_id)`,
		`CREATE INDEX mv_posts_title_idx ON mv_posts (title)`,
		`CREATE FUNCTION mv_posts_shout(p mv_posts) RETURNS text LANGUAGE sql STABLE AS $$ SELECT upper(p.title) $$`,
		`INSERT INTO mv_authors VALUES (1, 'ada')`,
		`INSERT INTO mv_posts (id, author_id, title) VALUES (1, 1, 'a-old')`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION mv_posts_shout(mv_posts); DROP TABLE mv_posts; DROP TABLE mv_authors`)
	})
	hs := testServer(t, nil)
	const sel = `mvAuthorByAuthorId { mvPostsByAuthorId(orderBy: ID_ASC) { totalCount nodes { id title } } bs: mvPostsByAuthorId(filter: {title: {startsWith: "b"}}) { totalCount } }`

	t.Run("create", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { createMvPost(input: {id: 2, authorId: 1, title: "b-new"}) { mvPost { `+sel+` } } }`, nil)
		expectField(t, res, "createMvPost", `{"mvPost": {"mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 2, "nodes": [{"id": 1, "title": "a-old"}, {"id": 2, "title": "b-new"}]},
			"bs": {"totalCount": 1}}}}`)
	})

	t.Run("create many", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { createMvPosts(input: [{id: 3, authorId: 1, title: "b-3"}, {id: 4, authorId: 1, title: "c-4"}]) { mvPosts { id `+sel+` } } }`, nil)
		requireNoErrors(t, res)
		var out struct {
			Rows []json.RawMessage `json:"mvPosts"`
		}
		mustUnmarshal(t, res.Data["createMvPosts"], &out)
		want := normalizeStr(t, `{"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "a-old"}, {"id": 2, "title": "b-new"}, {"id": 3, "title": "b-3"}, {"id": 4, "title": "c-4"}]}, "bs": {"totalCount": 2}}`)
		if len(out.Rows) != 2 {
			t.Fatalf("got %d rows", len(out.Rows))
		}
		for _, r := range out.Rows {
			var row struct {
				Author json.RawMessage `json:"mvAuthorByAuthorId"`
			}
			mustUnmarshal(t, r, &row)
			if got := normalize(t, row.Author); got != want {
				t.Errorf("got %s\nwant %s", got, want)
			}
		}
	})

	t.Run("update", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updateMvPostById(id: 1, patch: {title: "b-renamed"}) { mvPost { title `+sel+` } } }`, nil)
		expectField(t, res, "updateMvPostById", `{"mvPost": {"title": "b-renamed", "mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "b-renamed"}, {"id": 2, "title": "b-new"}, {"id": 3, "title": "b-3"}, {"id": 4, "title": "c-4"}]},
			"bs": {"totalCount": 3}}}}`)
	})

	t.Run("update changing the primary key", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updateMvPostById(id: 4, patch: {id: 40}) { mvPost { id `+sel+` } } }`, nil)
		expectField(t, res, "updateMvPostById", `{"mvPost": {"id": 40, "mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "b-renamed"}, {"id": 2, "title": "b-new"}, {"id": 3, "title": "b-3"}, {"id": 40, "title": "c-4"}]},
			"bs": {"totalCount": 3}}}}`)
	})

	t.Run("update by unique key", func(t *testing.T) {
		execAll(t, conn, `UPDATE mv_posts SET code = 'k3' WHERE id = 3`)
		res := post(t, hs.URL, `mutation { updateMvPostByCode(code: "k3", patch: {code: "k33", title: "c-3"}) { mvPost { code `+sel+` } } }`, nil)
		expectField(t, res, "updateMvPostByCode", `{"mvPost": {"code": "k33", "mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "b-renamed"}, {"id": 2, "title": "b-new"}, {"id": 3, "title": "c-3"}, {"id": 40, "title": "c-4"}]},
			"bs": {"totalCount": 2}}}}`)
	})

	t.Run("update many", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updateMvPosts(filter: {title: {startsWith: "c"}}, patch: {title: "b-bulk"}) { affectedCount } }`, nil)
		expectField(t, res, "updateMvPosts", `{"affectedCount": 2}`)
		res = post(t, hs.URL, `mutation { updateMvPosts(filter: {title: {equalTo: "b-bulk"}}, patch: {title: "c-bulk"}) { mvPosts { `+sel+` } } }`, nil)
		requireNoErrors(t, res)
		var out struct {
			Rows []struct {
				Author json.RawMessage `json:"mvAuthorByAuthorId"`
			} `json:"mvPosts"`
		}
		mustUnmarshal(t, res.Data["updateMvPosts"], &out)
		want := normalizeStr(t, `{"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "b-renamed"}, {"id": 2, "title": "b-new"}, {"id": 3, "title": "c-bulk"}, {"id": 40, "title": "c-bulk"}]}, "bs": {"totalCount": 2}}`)
		if len(out.Rows) != 2 {
			t.Fatalf("got %d rows", len(out.Rows))
		}
		for _, r := range out.Rows {
			if got := normalize(t, r.Author); got != want {
				t.Errorf("got %s\nwant %s", got, want)
			}
		}
	})

	t.Run("update many patching every key", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updateMvPosts(filter: {title: {equalTo: "b-new"}}, patch: {id: 20, code: "k20"}) { mvPosts { id `+sel+` } } }`, nil)
		expectField(t, res, "updateMvPosts", `{"mvPosts": [{"id": 20, "mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "b-renamed"}, {"id": 3, "title": "c-bulk"}, {"id": 20, "title": "b-new"}, {"id": 40, "title": "c-bulk"}]},
			"bs": {"totalCount": 2}}}]}`)
	})

	t.Run("upsert", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { upsertMvPostById(input: {id: 1, authorId: 1, title: "c-upserted"}) { mvPost { `+sel+` } } }`, nil)
		expectField(t, res, "upsertMvPostById", `{"mvPost": {"mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "c-upserted"}, {"id": 3, "title": "c-bulk"}, {"id": 20, "title": "b-new"}, {"id": 40, "title": "c-bulk"}]},
			"bs": {"totalCount": 1}}}}`)
		res = post(t, hs.URL, `mutation { upsertMvPostById(input: {id: 5, authorId: 1, title: "b-5"}) { mvPost { mvAuthorByAuthorId { mvPostsByAuthorId { totalCount } } } } }`, nil)
		expectField(t, res, "upsertMvPostById", `{"mvPost": {"mvAuthorByAuthorId": {"mvPostsByAuthorId": {"totalCount": 5}}}}`)
	})

	t.Run("delete", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { deleteMvPostById(id: 20) { mvPost { id `+sel+` } } }`, nil)
		expectField(t, res, "deleteMvPostById", `{"mvPost": {"id": 20, "mvAuthorByAuthorId": {
			"mvPostsByAuthorId": {"totalCount": 4, "nodes": [{"id": 1, "title": "c-upserted"}, {"id": 3, "title": "c-bulk"}, {"id": 5, "title": "b-5"}, {"id": 40, "title": "c-bulk"}]},
			"bs": {"totalCount": 1}}}}`)
	})

	t.Run("delete many", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { deleteMvPosts(filter: {title: {equalTo: "c-bulk"}}) { affectedCount mvPosts { mvAuthorByAuthorId { mvPostsByAuthorId { totalCount nodes { id } } } } } }`, nil)
		expectField(t, res, "deleteMvPosts", `{"affectedCount": 2, "mvPosts": [
			{"mvAuthorByAuthorId": {"mvPostsByAuthorId": {"totalCount": 2, "nodes": [{"id": 1}, {"id": 5}]}}},
			{"mvAuthorByAuthorId": {"mvPostsByAuthorId": {"totalCount": 2, "nodes": [{"id": 1}, {"id": 5}]}}}]}`)
	})

	t.Run("computed column over the merged table", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { createMvPost(input: {id: 6, authorId: 1, title: "loud"}) { mvPost { mvAuthorByAuthorId { mvPostsByAuthorId(filter: {id: {equalTo: 6}}) { nodes { shout } } } } } }`, nil)
		expectField(t, res, "createMvPost", `{"mvPost": {"mvAuthorByAuthorId": {"mvPostsByAuthorId": {"nodes": [{"shout": "LOUD"}]}}}}`)
	})
}

// TestMutationVisibilityRLS checks the payload of plain mutations under RLS
// whose UPDATE and DELETE policies are narrower than the SELECT one: the
// payload lists the rows the role may read, created and updated rows in
// their new version, and rows a bulk update or delete matched but its
// policies skipped keep showing; rows the SELECT policy hides never do. The
// role only holds column-level SELECT grants on the mutated table.
func TestMutationVisibilityRLS(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE mvr_authors (id integer PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE mvr_posts (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, author_id integer NOT NULL REFERENCES mvr_authors (id), owner_id integer NOT NULL, visible boolean NOT NULL DEFAULT true, title text NOT NULL, secret text)`,
		`CREATE INDEX mvr_posts_author_id_idx ON mvr_posts (author_id)`,
		`CREATE INDEX mvr_posts_title_idx ON mvr_posts (title)`,
		`GRANT SELECT ON mvr_authors TO app_user`,
		`GRANT SELECT (id, author_id, owner_id, visible, title), INSERT, UPDATE, DELETE ON mvr_posts TO app_user`,
		`ALTER TABLE mvr_posts ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY mvr_read ON mvr_posts FOR SELECT USING (visible OR owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`CREATE POLICY mvr_insert ON mvr_posts FOR INSERT WITH CHECK (owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`CREATE POLICY mvr_update ON mvr_posts FOR UPDATE USING (owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`CREATE POLICY mvr_delete ON mvr_posts FOR DELETE USING (owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`INSERT INTO mvr_authors VALUES (1, 'shared')`,
		`INSERT INTO mvr_posts (author_id, owner_id, visible, title, secret) VALUES (1, 2, true, 'grace-1', 's'), (1, 1, true, 'ada-public', 's'), (1, 1, false, 'ada-hidden', 's')`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE mvr_posts; DROP TABLE mvr_authors`)
	})
	hs := testServer(t, func(cfg *config.Config) {
		headersIdentity(cfg)
	})
	asGrace := func(t *testing.T, query string) gqlResp {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": query})
		req, err := http.NewRequest("POST", hs.URL+"/graphql", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pdbq-Claim-Role", "app_user")
		req.Header.Set("X-Pdbq-Claim-User-Id", "2")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		var out gqlResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("bad response: %s", raw)
		}
		return out
	}
	const sel = `mvrAuthorByAuthorId { mvrPostsByAuthorId(orderBy: TITLE_ASC) { totalCount nodes { title } } }`

	t.Run("create", func(t *testing.T) {
		res := asGrace(t, `mutation { createMvrPost(input: {authorId: 1, ownerId: 2, title: "grace-2"}) { mvrPost { title `+sel+` } } }`)
		expectField(t, res, "createMvrPost", `{"mvrPost": {"title": "grace-2", "mvrAuthorByAuthorId": {"mvrPostsByAuthorId": {"totalCount": 3,
			"nodes": [{"title": "ada-public"}, {"title": "grace-1"}, {"title": "grace-2"}]}}}}`)
	})

	t.Run("update many skips rows of others", func(t *testing.T) {
		res := asGrace(t, `mutation { updateMvrPosts(filter: {authorId: {equalTo: 1}}, patch: {title: "grace-x"}) { affectedCount mvrPosts { `+sel+` } } }`)
		expectField(t, res, "updateMvrPosts", `{"affectedCount": 2, "mvrPosts": [
			{"mvrAuthorByAuthorId": {"mvrPostsByAuthorId": {"totalCount": 3, "nodes": [{"title": "ada-public"}, {"title": "grace-x"}, {"title": "grace-x"}]}}},
			{"mvrAuthorByAuthorId": {"mvrPostsByAuthorId": {"totalCount": 3, "nodes": [{"title": "ada-public"}, {"title": "grace-x"}, {"title": "grace-x"}]}}}]}`)
	})

	t.Run("delete many skips rows of others", func(t *testing.T) {
		res := asGrace(t, `mutation { deleteMvrPosts(filter: {authorId: {equalTo: 1}}) { affectedCount mvrPosts { `+sel+` } } }`)
		expectField(t, res, "deleteMvrPosts", `{"affectedCount": 2, "mvrPosts": [
			{"mvrAuthorByAuthorId": {"mvrPostsByAuthorId": {"totalCount": 1, "nodes": [{"title": "ada-public"}]}}},
			{"mvrAuthorByAuthorId": {"mvrPostsByAuthorId": {"totalCount": 1, "nodes": [{"title": "ada-public"}]}}}]}`)
	})
}
