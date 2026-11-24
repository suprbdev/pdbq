package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestNestedMutationVisibility checks that a nested create's payload sees
// the rows its own CTEs inserted: created parents (forward create, any
// depth), created children and grandchildren (backward create), and the
// main row itself when a relation leads back to its table, including
// totalCount and relation filters over those tables.
func TestNestedMutationVisibility(t *testing.T) {
	hs := testServer(t, nil)
	run := time.Now().UnixNano()
	email := func(prefix string) string { return fmt.Sprintf("%s-%d@example.com", prefix, run) }
	sweepUsers(t, fmt.Sprintf("%%-%d@example.com", run))

	t.Run("forward create", func(t *testing.T) {
		res := post(t, hs.URL, fmt.Sprintf(`mutation {
			createComment(input: {comment: {body: "First!", postByPostId: {create: {title: "Fwd", userByAuthorId: {create: {email: %q}}}}}}) {
				comment {
					body
					postByPostId {
						title
						userByAuthorId { email postsByAuthorId { totalCount nodes { title } } }
						commentsByPostId(filter: {parentId: {isNull: true}}) { totalCount nodes { body } }
					}
				}
			}
		}`, email("fwd")), nil)
		requireNoErrors(t, res)
		want := normalizeStr(t, fmt.Sprintf(`{"comment": {"body": "First!", "postByPostId": {"title": "Fwd",
			"userByAuthorId": {"email": %q, "postsByAuthorId": {"totalCount": 1, "nodes": [{"title": "Fwd"}]}},
			"commentsByPostId": {"totalCount": 1, "nodes": [{"body": "First!"}]}}}}`, email("fwd")))
		if got := normalize(t, res.Data["createComment"]); got != want {
			t.Errorf("got %s\nwant %s", got, want)
		}
	})

	var userID int
	t.Run("backward create", func(t *testing.T) {
		res := post(t, hs.URL, fmt.Sprintf(`mutation {
			createUser(input: {user: {email: %q, postsByAuthorId: {create: [
				{title: "Kid A", commentsByPostId: {create: [{body: "ga"}]}}
				{title: "Kid B"}
			]}}}) {
				user {
					id
					all: postsByAuthorId(orderBy: TITLE_ASC) {
						totalCount
						nodes { title userByAuthorId { email } commentsByPostId { totalCount nodes { body postByPostId { title } } } }
					}
					commented: postsByAuthorId(filter: {commentsByPostId: {some: {parentId: {isNull: true}}}}) {
						totalCount nodes { title }
					}
				}
			}
		}`, email("bwd")), nil)
		requireNoErrors(t, res)
		var out struct {
			User struct {
				ID  int             `json:"id"`
				All json.RawMessage `json:"all"`
				Com json.RawMessage `json:"commented"`
			} `json:"user"`
		}
		mustUnmarshal(t, res.Data["createUser"], &out)
		userID = out.User.ID
		wantAll := normalizeStr(t, fmt.Sprintf(`{"totalCount": 2, "nodes": [
			{"title": "Kid A", "userByAuthorId": {"email": %[1]q}, "commentsByPostId": {"totalCount": 1, "nodes": [{"body": "ga", "postByPostId": {"title": "Kid A"}}]}},
			{"title": "Kid B", "userByAuthorId": {"email": %[1]q}, "commentsByPostId": {"totalCount": 0, "nodes": []}}]}`, email("bwd")))
		if got := normalize(t, out.User.All); got != wantAll {
			t.Errorf("children: got %s\nwant %s", got, wantAll)
		}
		wantCom := normalizeStr(t, `{"totalCount": 1, "nodes": [{"title": "Kid A"}]}`)
		if got := normalize(t, out.User.Com); got != wantCom {
			t.Errorf("relation filter: got %s\nwant %s", got, wantCom)
		}
	})

	t.Run("connect sees existing and new rows", func(t *testing.T) {
		if userID == 0 {
			t.Skip("backward create failed")
		}
		res := post(t, hs.URL, fmt.Sprintf(`mutation {
			createPost(input: {post: {title: "Kid C", userByAuthorId: {connectById: {id: %d}}, commentsByPostId: {create: [{body: "gc"}]}}}) {
				post {
					title
					userByAuthorId { postsByAuthorId(orderBy: TITLE_ASC) { totalCount nodes { title } } }
					commentsByPostId { nodes { body } }
				}
			}
		}`, userID), nil)
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"post": {"title": "Kid C",
			"userByAuthorId": {"postsByAuthorId": {"totalCount": 3, "nodes": [{"title": "Kid A"}, {"title": "Kid B"}, {"title": "Kid C"}]}},
			"commentsByPostId": {"nodes": [{"body": "gc"}]}}}`)
		if got := normalize(t, res.Data["createPost"]); got != want {
			t.Errorf("got %s\nwant %s", got, want)
		}
	})
}

// TestNestedMutationVisibilityRLS checks that the payload of a nested create
// under RLS shows the rows the role may read, existing and just inserted,
// and nothing its SELECT policies hide; a nested row the role could not
// read back fails the whole statement instead of leaking.
func TestNestedMutationVisibilityRLS(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE nv_lists (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, owner_id integer NOT NULL, name text NOT NULL)`,
		`CREATE TABLE nv_items (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, list_id integer NOT NULL REFERENCES nv_lists (id), owner_id integer NOT NULL, body text NOT NULL)`,
		`CREATE INDEX nv_items_list_id_idx ON nv_items (list_id)`,
		`GRANT SELECT, INSERT ON nv_lists, nv_items TO app_user`,
		`ALTER TABLE nv_lists ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE nv_items ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY nv_lists_read ON nv_lists FOR SELECT USING (owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`CREATE POLICY nv_lists_write ON nv_lists FOR INSERT WITH CHECK (true)`,
		`CREATE POLICY nv_items_read ON nv_items FOR SELECT USING (owner_id = NULLIF(current_setting('jwt.claims.user_id', true), '')::integer)`,
		`CREATE POLICY nv_items_write ON nv_items FOR INSERT WITH CHECK (true)`,
		`INSERT INTO nv_lists (owner_id, name) VALUES (2, 'shared')`,
		`INSERT INTO nv_items (list_id, owner_id, body) SELECT id, 1, 'ada-secret' FROM nv_lists WHERE name = 'shared'`,
		`INSERT INTO nv_items (list_id, owner_id, body) SELECT id, 2, 'grace-visible' FROM nv_lists WHERE name = 'shared'`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE nv_items; DROP TABLE nv_lists`)
	})
	var listID int
	if err := conn.QueryRow(context.Background(), `SELECT id FROM nv_lists WHERE name = 'shared'`).Scan(&listID); err != nil {
		t.Fatal(err)
	}
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

	t.Run("connected parent lists readable rows only", func(t *testing.T) {
		res := asGrace(t, fmt.Sprintf(`mutation {
			createNvItem(input: {nvItem: {body: "grace-new", ownerId: 2, nvListByListId: {connectById: {id: %d}}}}) {
				nvItem { nvListByListId { name nvItemsByListId { totalCount nodes { body } } } }
			}
		}`, listID))
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"nvItem": {"nvListByListId": {"name": "shared", "nvItemsByListId": {"totalCount": 2, "nodes": [{"body": "grace-visible"}, {"body": "grace-new"}]}}}}`)
		if got := normalize(t, res.Data["createNvItem"]); got != want {
			t.Errorf("got %s\nwant %s", got, want)
		}
	})

	t.Run("created parent and children", func(t *testing.T) {
		res := asGrace(t, `mutation {
			createNvItem(input: {nvItem: {body: "in-fresh", ownerId: 2, nvListByListId: {create: {ownerId: 2, name: "fresh"}}}}) {
				nvItem { nvListByListId { name nvItemsByListId { totalCount nodes { body } } } }
			}
		}`)
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"nvItem": {"nvListByListId": {"name": "fresh", "nvItemsByListId": {"totalCount": 1, "nodes": [{"body": "in-fresh"}]}}}}`)
		if got := normalize(t, res.Data["createNvItem"]); got != want {
			t.Errorf("forward: got %s\nwant %s", got, want)
		}

		res = asGrace(t, `mutation {
			createNvList(input: {nvList: {ownerId: 2, name: "kids", nvItemsByListId: {create: [{body: "kid", ownerId: 2}]}}}) {
				nvList { name nvItemsByListId { totalCount nodes { body nvListByListId { name } } } }
			}
		}`)
		requireNoErrors(t, res)
		want = normalizeStr(t, `{"nvList": {"name": "kids", "nvItemsByListId": {"totalCount": 1, "nodes": [{"body": "kid", "nvListByListId": {"name": "kids"}}]}}}`)
		if got := normalize(t, res.Data["createNvList"]); got != want {
			t.Errorf("backward: got %s\nwant %s", got, want)
		}
	})

	t.Run("unreadable nested row fails the statement", func(t *testing.T) {
		res := asGrace(t, `mutation {
			createNvList(input: {nvList: {ownerId: 2, name: "leak", nvItemsByListId: {create: [{body: "for-ada", ownerId: 1}]}}}) {
				nvList { nvItemsByListId { nodes { body } } }
			}
		}`)
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "row-level security") {
			t.Fatalf("want an RLS error, got %+v / %s", res.Errors, res.Data["createNvList"])
		}
		var n int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM nv_lists WHERE name = 'leak'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("rolled-back list persisted (%d rows)", n)
		}
	})
}
