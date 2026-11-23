package e2e

import (
	"context"
	"encoding/json"
	"testing"
)

// TestMutationPayloads covers the PostGraphile payload fields: the result
// field named after its type (plural for SETOF and arrays, the output
// parameter's name, none for void), query, the forward relations of a
// returned row and its edge, on custom and table mutations alike, and a
// custom mutation's payload seeing what its function wrote.
func TestMutationPayloads(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE pay_authors (id integer PRIMARY KEY, name text NOT NULL)`,
		`CREATE TABLE pay_posts (id integer PRIMARY KEY, author_id integer REFERENCES pay_authors (id), title text NOT NULL, hits integer NOT NULL DEFAULT 0)`,
		`CREATE INDEX pay_posts_author_id_idx ON pay_posts (author_id)`,
		`CREATE INDEX pay_posts_title_idx ON pay_posts (title)`,
		`INSERT INTO pay_authors VALUES (1, 'ada')`,
		`INSERT INTO pay_posts (id, author_id, title) VALUES (1, 1, 'one'), (2, 1, 'two')`,
		`CREATE FUNCTION pay_hit(post_id integer) RETURNS pay_posts VOLATILE LANGUAGE sql AS $$
			UPDATE pay_posts SET hits = hits + 1 WHERE id = post_id RETURNING * $$`,
		`CREATE FUNCTION pay_hit_all(post_ids integer[]) RETURNS SETOF pay_posts VOLATILE LANGUAGE sql AS $$
			UPDATE pay_posts SET hits = hits + 1 WHERE id = ANY (post_ids) RETURNING * $$`,
		`CREATE FUNCTION pay_touch(post_id integer) RETURNS void VOLATILE LANGUAGE sql AS $$
			UPDATE pay_posts SET hits = hits + 1 WHERE id = post_id $$`,
		`CREATE FUNCTION pay_count(OUT total integer) VOLATILE LANGUAGE sql AS $$
			SELECT count(*)::integer FROM pay_posts $$`,
		`CREATE FUNCTION pay_titles() RETURNS text[] VOLATILE LANGUAGE sql AS $$
			SELECT array_agg(title ORDER BY id) FROM pay_posts $$`,
		`CREATE FUNCTION pay_write(author_name text, post_title text) RETURNS pay_posts VOLATILE LANGUAGE plpgsql AS $$
			DECLARE r pay_posts;
			BEGIN
				INSERT INTO pay_authors VALUES (10, author_name);
				INSERT INTO pay_posts (id, author_id, title) VALUES (10, 10, post_title) RETURNING * INTO r;
				RETURN r;
			END $$`,
		`CREATE FUNCTION pay_add_author(author_id integer, author_name text) RETURNS integer VOLATILE LANGUAGE sql AS $$
			INSERT INTO pay_authors VALUES (author_id, author_name);
			SELECT count(*)::integer FROM pay_authors $$`,
		`CREATE FUNCTION pay_newest() RETURNS SETOF pay_posts VOLATILE LANGUAGE sql AS $$
			SELECT * FROM pay_posts WHERE id IN (1, 2) ORDER BY id DESC $$`,
		`CREATE FUNCTION pay_boom(post_id integer, post_title text) RETURNS pay_posts VOLATILE LANGUAGE sql AS $$
			INSERT INTO pay_posts (id, author_id, title) VALUES (post_id, 1, post_title) RETURNING * $$`,
		`CREATE FUNCTION pay_posts_checked(p pay_posts) RETURNS text STABLE LANGUAGE plpgsql AS $$
			BEGIN
				IF p.title = 'boom' THEN RAISE EXCEPTION 'boom'; END IF;
				RETURN p.title;
			END $$`,
		`COMMENT ON FUNCTION pay_titles() IS '@resultFieldName titles'`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION pay_hit, pay_hit_all, pay_touch, pay_count, pay_titles, pay_write,
				pay_add_author, pay_newest, pay_boom, pay_posts_checked;
			DROP TABLE pay_posts; DROP TABLE pay_authors`)
	})
	hs := testServer(t, nil)

	t.Run("row result with relations, edge and query", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
		  payHit(input: {postId: 1, clientMutationId: "cm"}) {
		    clientMutationId
		    payPost { title hits }
		    payAuthorByAuthorId { name }
		    payPostEdge(orderBy: [TITLE_ASC]) { cursor node { id hits } }
		    query { payPostById(id: 2) { title } }
		  }
		}`, nil)
		requireNoErrors(t, res)
		var out struct {
			Edge struct {
				Cursor string `json:"cursor"`
			} `json:"payPostEdge"`
		}
		mustUnmarshal(t, res.Data["payHit"], &out)
		expectField(t, res, "payHit", `{"clientMutationId": "cm", "payPost": {"title": "one", "hits": 1},
			"payAuthorByAuthorId": {"name": "ada"},
			"payPostEdge": {"cursor": "`+out.Edge.Cursor+`", "node": {"id": 1, "hits": 1}},
			"query": {"payPostById": {"title": "two"}}}`)

		res = post(t, hs.URL, `{
		  byId: allPayPosts(after: "`+out.Edge.Cursor+`") { nodes { id } }
		  byTitle: allPayPosts(orderBy: [TITLE_DESC], after: "`+out.Edge.Cursor+`") { nodes { id } }
		}`, nil)
		expectField(t, res, "byId", `{"nodes": [{"id": 2}]}`)
		expectField(t, res, "byTitle", `{"nodes": []}`)
	})

	t.Run("setof, void, output parameter and array results", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
		  payHitAll(input: {postIds: [1, 2]}) { payPosts { id hits } }
		  payTouch(input: {postId: 2, clientMutationId: "t"}) { clientMutationId query { __typename } }
		  payCount(input: {}) { total }
		  payTitles(input: {}) { titles }
		}`, nil)
		expectField(t, res, "payHitAll", `{"payPosts": [{"id": 1, "hits": 2}, {"id": 2, "hits": 1}]}`)
		expectField(t, res, "payTouch", `{"clientMutationId": "t", "query": {"__typename": "Query"}}`)
		expectField(t, res, "payCount", `{"total": 2}`)
		expectField(t, res, "payTitles", `{"titles": ["one", "two"]}`)
	})

	t.Run("custom mutation payload sees the function's writes", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
		  payWrite(input: {authorName: "grace", postTitle: "ten"}) {
		    payPost { id title payAuthorByAuthorId { name } }
		    payAuthorByAuthorId { name }
		    payPostEdge { node { payAuthorByAuthorId { name } } }
		    query { payAuthorById(id: 10) { name } allPayPosts { totalCount } }
		  }
		}`, nil)
		requireNoErrors(t, res)
		expectField(t, res, "payWrite", `{"payPost": {"id": 10, "title": "ten", "payAuthorByAuthorId": {"name": "grace"}},
			"payAuthorByAuthorId": {"name": "grace"}, "payPostEdge": {"node": {"payAuthorByAuthorId": {"name": "grace"}}},
			"query": {"payAuthorById": {"name": "grace"}, "allPayPosts": {"totalCount": 3}}}`)
	})

	t.Run("scalar result with query", func(t *testing.T) {
		res := post(t, hs.URL, `mutation ($id: Int!) {
		  payAddAuthor(input: {authorId: $id, authorName: "ken", clientMutationId: "a"}) {
		    integer
		    clientMutationId
		    query { payAuthorById(id: $id) { name } }
		  }
		}`, map[string]any{"id": 40})
		requireNoErrors(t, res)
		expectField(t, res, "payAddAuthor", `{"integer": 3, "clientMutationId": "a",
			"query": {"payAuthorById": {"name": "ken"}}}`)
	})

	t.Run("setof result keeps the function's order", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { payNewest(input: {}) { payPosts { id payAuthorByAuthorId { name } } } }`, nil)
		expectField(t, res, "payNewest", `{"payPosts": [{"id": 2, "payAuthorByAuthorId": {"name": "ada"}}, {"id": 1, "payAuthorByAuthorId": {"name": "ada"}}]}`)
	})

	t.Run("payload error rolls back the function", func(t *testing.T) {
		res := post(t, hs.URL, `mutation ($id: Int!) {
		  payBoom(input: {postId: $id, postTitle: "boom"}) { payPost { id checked } }
		}`, map[string]any{"id": 30})
		if len(res.Errors) != 1 || string(res.Data["payBoom"]) != "null" {
			raw, _ := json.Marshal(res)
			t.Fatalf("want the payload's error, got %s", raw)
		}
		res = post(t, hs.URL, `query ($id: Int!) { payPostById(id: $id) { id } }`, map[string]any{"id": 30})
		expectField(t, res, "payPostById", `null`)
	})

	t.Run("table mutation payload", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
		  createPayPost(input: {id: 3, authorId: 1, title: "three"}) {
		    payAuthorByAuthorId { name }
		    payPostEdge { node { title } }
		    query { allPayPosts(orderBy: [ID_ASC]) { totalCount nodes { id } } }
		  }
		  deletePayPostById(id: 3) {
		    payPost { id }
		    query { payPostById(id: 3) { id } allPayPosts { totalCount } }
		  }
		  updatePayPosts(filter: {authorId: {equalTo: 1}}, patch: {title: "same"}) {
		    affectedCount
		    query { allPayPosts(filter: {title: {equalTo: "same"}}) { totalCount } }
		  }
		}`, nil)
		expectField(t, res, "createPayPost", `{"payAuthorByAuthorId": {"name": "ada"}, "payPostEdge": {"node": {"title": "three"}},
			"query": {"allPayPosts": {"totalCount": 4, "nodes": [{"id": 1}, {"id": 2}, {"id": 3}, {"id": 10}]}}}`)
		expectField(t, res, "deletePayPostById", `{"payPost": {"id": 3}, "query": {"payPostById": null, "allPayPosts": {"totalCount": 3}}}`)
		expectField(t, res, "updatePayPosts", `{"affectedCount": 2, "query": {"allPayPosts": {"totalCount": 2}}}`)
	})

	t.Run("nested mutation payload", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
		  createPayPost(input: {id: 20, title: "nested", payAuthorByAuthorId: {create: {id: 20, name: "linus"}}}) {
		    payAuthorByAuthorId { name }
		    query { payAuthorById(id: 20) { name } }
		  }
		}`, nil)
		expectField(t, res, "createPayPost", `{"payAuthorByAuthorId": {"name": "linus"}, "query": {"payAuthorById": {"name": "linus"}}}`)
	})

	t.Run("not-found update still errors", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updatePayPostById(id: 99, patch: {title: "x"}) { query { __typename } } }`, nil)
		if len(res.Errors) != 1 || string(res.Data["updatePayPostById"]) != "null" {
			raw, _ := json.Marshal(res)
			t.Errorf("want a no-row error, got %s", raw)
		}
	})
}
