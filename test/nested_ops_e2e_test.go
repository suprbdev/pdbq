package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestNestedMutationOperations covers every nested operation of the
// nested-mutations plugin against a live database, in both directions, by
// key and by nodeId, on create and update mutations: the payload must show
// the rows as they stand after the statement, and a failing operation must
// roll the whole mutation back.
func TestNestedMutationOperations(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE no_teams (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, slug text NOT NULL UNIQUE, name text NOT NULL)`,
		`CREATE TABLE no_members (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, team_id integer REFERENCES no_teams (id), name text NOT NULL)`,
		`CREATE INDEX no_members_team_id_idx ON no_members (team_id)`,
		`CREATE INDEX no_members_name_idx ON no_members (name)`,
		`CREATE TABLE no_tasks (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, member_id integer NOT NULL REFERENCES no_members (id), title text NOT NULL)`,
		`CREATE INDEX no_tasks_member_id_idx ON no_tasks (member_id)`,
		`INSERT INTO no_teams (slug, name) VALUES ('red', 'Red'), ('blue', 'Blue')`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE no_tasks; DROP TABLE no_members; DROP TABLE no_teams`)
	})
	hs := testServer(t, nil)
	ctx := context.Background()
	scalar := func(t *testing.T, sql string, args ...any) (v any) {
		t.Helper()
		if err := conn.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	teamID := func(t *testing.T, slug string) int {
		t.Helper()
		return int(scalar(t, `SELECT id FROM no_teams WHERE slug = $1`, slug).(int32))
	}
	member := func(t *testing.T, name string, team any) int {
		t.Helper()
		return int(scalar(t, `INSERT INTO no_members (name, team_id) VALUES ($1, $2) RETURNING id`, name, team).(int32))
	}
	nodeID := func(typ string, id int) string {
		b, _ := json.Marshal([]any{typ, id})
		return base64.StdEncoding.EncodeToString(b)
	}
	expect := func(t *testing.T, res gqlResp, field, want string) {
		t.Helper()
		requireNoErrors(t, res)
		if got := normalize(t, res.Data[field]); got != normalizeStr(t, want) {
			t.Errorf("got %s\nwant %s", got, want)
		}
	}
	expectError := func(t *testing.T, res gqlResp, substr string) {
		t.Helper()
		if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, substr) {
			t.Fatalf("want error containing %q, got %+v", substr, res.Errors)
		}
	}
	red, blue := teamID(t, "red"), teamID(t, "blue")

	t.Run("forward connect by unique key on create", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { createNoMember(input: {name: "fwd-key", team: {connectBySlug: {slug: "red"}}}) {
			noMember { name team { slug noMembersByTeamId(filter: {name: {equalTo: "fwd-key"}}) { totalCount } } } } }`, nil)
		expect(t, res, "createNoMember", `{"noMember": {"name": "fwd-key", "team": {"slug": "red", "noMembersByTeamId": {"totalCount": 1}}}}`)
	})

	t.Run("forward connect by nodeId on create", func(t *testing.T) {
		res := post(t, hs.URL, `mutation($id: ID!) { createNoMember(input: {name: "fwd-node", team: {connectByNodeId: {nodeId: $id}}}) {
			noMember { team { slug } } } }`, map[string]any{"id": nodeID("NoTeam", blue)})
		expect(t, res, "createNoMember", `{"noMember": {"team": {"slug": "blue"}}}`)

		res = post(t, hs.URL, `mutation($id: ID!) { createNoMember(input: {name: "fwd-bad-node", team: {connectByNodeId: {nodeId: $id}}}) {
			noMember { id } } }`, map[string]any{"id": nodeID("NoMember", blue)})
		expectError(t, res, "nodeId does not identify a NoTeam")
	})

	t.Run("forward connect of a missing row rolls back", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { createNoMember(input: {name: "fwd-missing", team: {connectBySlug: {slug: "nope"}}}) { noMember { id } } }`, nil)
		expectError(t, res, "team.connectBySlug matched 0 rows")
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE name = 'fwd-missing'`); n != int64(0) {
			t.Errorf("rolled-back member persisted: %v", n)
		}
	})

	t.Run("forward create and update by key on update", func(t *testing.T) {
		m := member(t, "fwd-upd", nil)
		res := post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: %d, patch: {team: {create: {slug: "green", name: "Green"}}}) {
			noMember { team { slug noMembersByTeamId { nodes { name } } } } } }`, m), nil)
		expect(t, res, "updateNoMemberById", `{"noMember": {"team": {"slug": "green", "noMembersByTeamId": {"nodes": [{"name": "fwd-upd"}]}}}}`)

		res = post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: %d, patch: {name: "fwd-upd2", team: {updateById: {id: %d, noTeamPatch: {name: "Blue!"}}}}) {
			noMember { name team { name } } } }`, m, blue), nil)
		expect(t, res, "updateNoMemberById", `{"noMember": {"name": "fwd-upd2", "team": {"name": "Blue!"}}}`)

		res = post(t, hs.URL, fmt.Sprintf(`mutation($id: ID!) { updateNoMemberById(id: %d, patch: {team: {updateByNodeId: {nodeId: $id, noTeamPatch: {name: "Red!"}}}}) {
			noMember { team { slug name } } } }`, m), map[string]any{"id": nodeID("NoTeam", red)})
		expect(t, res, "updateNoMemberById", `{"noMember": {"team": {"slug": "red", "name": "Red!"}}}`)
	})

	t.Run("forward disconnect by key and nodeId", func(t *testing.T) {
		m := member(t, "fwd-disc", red)
		res := post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: %d, patch: {team: {disconnectBySlug: {slug: "blue"}}}) {
			noMember { teamId } } }`, m), nil)
		expect(t, res, "updateNoMemberById", fmt.Sprintf(`{"noMember": {"teamId": %d}}`, red))

		res = post(t, hs.URL, fmt.Sprintf(`mutation($id: ID!) { updateNoMemberById(id: %d, patch: {team: {disconnectByNodeId: {nodeId: $id}}}) {
			noMember { teamId team { slug } } } }`, m), map[string]any{"id": nodeID("NoTeam", red)})
		expect(t, res, "updateNoMemberById", `{"noMember": {"teamId": null, "team": null}}`)

		res = post(t, hs.URL, `mutation { createNoMember(input: {name: "fwd-disc-create", team: {disconnectById: {id: 1}}}) { noMember { id } } }`, nil)
		expectError(t, res, "disconnect applies to update mutations only")
	})

	t.Run("forward delete with disconnect", func(t *testing.T) {
		tmp := int(scalar(t, `INSERT INTO no_teams (slug, name) VALUES ('tmp', 'Tmp') RETURNING id`).(int32))
		m := member(t, "fwd-del", tmp)
		res := post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: %d, patch: {team: {deleteById: {id: %d}, disconnectById: {id: %d}}}) {
			noMember { teamId } } }`, m, tmp, tmp), nil)
		expect(t, res, "updateNoMemberById", `{"noMember": {"teamId": null}}`)
		if n := scalar(t, `SELECT count(*) FROM no_teams WHERE id = $1`, tmp); n != int64(0) {
			t.Errorf("deleted team persisted")
		}

		res = post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: %d, patch: {team: {connectById: {id: %d}, create: {slug: "x", name: "X"}}}) {
			noMember { teamId } } }`, m, red), nil)
		expectError(t, res, "specify at most one of create, connect, update or disconnect")
	})

	t.Run("backward operations on update", func(t *testing.T) {
		team := int(scalar(t, `INSERT INTO no_teams (slug, name) VALUES ('bwd', 'Bwd') RETURNING id`).(int32))
		join := member(t, "b-join", nil)
		joinNode := member(t, "b-join-node", red)
		rename := member(t, "b-rename", team)
		renameNode := member(t, "b-rename-node", team)
		leave := member(t, "b-leave", team)
		leaveNode := member(t, "b-leave-node", team)
		gone := member(t, "b-gone", team)
		goneNode := member(t, "b-gone-node", team)
		res := post(t, hs.URL, fmt.Sprintf(`mutation($jn: ID!, $rn: ID!, $ln: ID!, $gn: ID!) { updateNoTeamById(id: %d, patch: {name: "Bwd!", noMembersByTeamId: {
				connectById: [{id: %d}], connectByNodeId: [{nodeId: $jn}],
				create: [{name: "b-new"}],
				updateById: [{id: %d, noMemberPatch: {name: "b-renamed"}}], updateByNodeId: [{nodeId: $rn, noMemberPatch: {name: "b-renamed-node"}}],
				disconnectById: [{id: %d}], disconnectByNodeId: [{nodeId: $ln}],
				deleteById: [{id: %d}], deleteByNodeId: [{nodeId: $gn}]
			}}) {
			noTeam { name noMembersByTeamId(orderBy: NAME_ASC) { totalCount nodes { name team { name } } } } } }`,
			team, join, rename, leave, gone),
			map[string]any{"jn": nodeID("NoMember", joinNode), "rn": nodeID("NoMember", renameNode), "ln": nodeID("NoMember", leaveNode), "gn": nodeID("NoMember", goneNode)})
		expect(t, res, "updateNoTeamById", `{"noTeam": {"name": "Bwd!", "noMembersByTeamId": {"totalCount": 5, "nodes": [
			{"name": "b-join", "team": {"name": "Bwd!"}}, {"name": "b-join-node", "team": {"name": "Bwd!"}},
			{"name": "b-new", "team": {"name": "Bwd!"}},
			{"name": "b-renamed", "team": {"name": "Bwd!"}}, {"name": "b-renamed-node", "team": {"name": "Bwd!"}}]}}}`)
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE id IN ($1, $2) AND team_id IS NULL`, leave, leaveNode); n != int64(2) {
			t.Errorf("disconnected members: %v of 2 have no team", n)
		}
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE id IN ($1, $2)`, gone, goneNode); n != int64(0) {
			t.Errorf("deleted members persisted: %v", n)
		}
	})

	t.Run("backward update and delete only reach the row's children", func(t *testing.T) {
		other := member(t, "b-other", red)
		res := post(t, hs.URL, fmt.Sprintf(`mutation { updateNoTeamById(id: %d, patch: {name: "Blue", noMembersByTeamId: {deleteById: [{id: %d}]}}) { noTeam { id } } }`, blue, other), nil)
		expectError(t, res, "noMembersByTeamId.deleteById[0] matched 0 rows")
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE id = $1`, other); n != int64(1) {
			t.Errorf("member of another team deleted")
		}
		if name := scalar(t, `SELECT name FROM no_teams WHERE id = $1`, blue); name != "Blue!" {
			t.Errorf("rolled-back team rename persisted: %v", name)
		}
		res = post(t, hs.URL, fmt.Sprintf(`mutation { updateNoTeamById(id: %d, patch: {noMembersByTeamId: {updateById: [{id: %d, noMemberPatch: {name: "stolen"}}]}}) { noTeam { id } } }`, blue, other), nil)
		expectError(t, res, "noMembersByTeamId.updateById[0] matched 0 rows")
	})

	t.Run("backward connect and create on create", func(t *testing.T) {
		loose := member(t, "c-loose", nil)
		res := post(t, hs.URL, fmt.Sprintf(`mutation { createNoTeam(input: {slug: "fresh", name: "Fresh", noMembersByTeamId: {connectById: {id: %d}, create: [{name: "c-kid", noTasksByMemberId: {create: [{title: "t1"}]}}]}}) {
			noTeam { noMembersByTeamId(orderBy: NAME_ASC) { nodes { name noTasksByMemberId { nodes { title } } } } } } }`, loose), nil)
		expect(t, res, "createNoTeam", `{"noTeam": {"noMembersByTeamId": {"nodes": [
			{"name": "c-kid", "noTasksByMemberId": {"nodes": [{"title": "t1"}]}},
			{"name": "c-loose", "noTasksByMemberId": {"nodes": []}}]}}}`)
	})

	t.Run("disconnect is absent for a NOT NULL foreign key", func(t *testing.T) {
		res := post(t, hs.URL, `mutation { updateNoMemberById(id: 1, patch: {noTasksByMemberId: {disconnectById: [{id: 1}]}}) { noMember { id } } }`, nil)
		expectError(t, res, `Field "disconnectById" is not defined`)
	})

	t.Run("a missing main row rolls back the forward operations", func(t *testing.T) {
		res := post(t, hs.URL, fmt.Sprintf(`mutation { updateNoMemberById(id: 999999, patch: {team: {updateById: {id: %d, noTeamPatch: {name: "ghost"}}}}) { noMember { id } } }`, red), nil)
		if len(res.Errors) == 0 {
			t.Fatal("want an error")
		}
		if name := scalar(t, `SELECT name FROM no_teams WHERE id = $1`, red); name == "ghost" {
			t.Error("forward update persisted without its main row")
		}
	})

	t.Run("a failing field rolls back the whole operation", func(t *testing.T) {
		res := post(t, hs.URL, `mutation {
			a: createNoMember(input: {name: "multi-a", team: {connectBySlug: {slug: "red"}}}) { noMember { id } }
			b: createNoMember(input: {name: "multi-b", team: {connectBySlug: {slug: "nope"}}}) { noMember { id } }
		}`, nil)
		expectError(t, res, "team.connectBySlug matched 0 rows")
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE name LIKE 'multi-%'`); n != int64(0) {
			t.Errorf("rolled-back members persisted: %v", n)
		}
	})

	t.Run("a failed guard aborts the statement without a transaction", func(t *testing.T) {
		plain := testServer(t, func(cfg *config.Config) {
			cfg.TX.Mutations = false
			cfg.Plugins.Settings["nested-mutations"] = map[string]any{"force_transactions": false}
		})
		res := post(t, plain.URL, fmt.Sprintf(`mutation { updateNoTeamById(id: %d, patch: {name: "no-tx", noMembersByTeamId: {create: [{name: "no-tx-kid"}], deleteById: [{id: 999999}]}}) { noTeam { id } } }`, red), nil)
		expectError(t, res, "noMembersByTeamId.deleteById[0] matched 0 rows")
		if n := scalar(t, `SELECT count(*) FROM no_members WHERE name = 'no-tx-kid'`); n != int64(0) {
			t.Errorf("statement was not atomic: %v", n)
		}
		if name := scalar(t, `SELECT name FROM no_teams WHERE id = $1`, red); name == "no-tx" {
			t.Error("statement was not atomic: team renamed")
		}
	})
}

// TestNestedMutationOperationsRLS checks that nested operations run under
// the caller's RLS policies: rows the role may not update or delete make
// the operation match nothing, which fails and rolls back the mutation,
// and the payload shows only readable rows.
func TestNestedMutationOperationsRLS(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE nr_lists (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, owner_id integer NOT NULL, name text NOT NULL)`,
		`CREATE TABLE nr_items (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, list_id integer REFERENCES nr_lists (id), owner_id integer NOT NULL, body text NOT NULL)`,
		`CREATE INDEX nr_items_list_id_idx ON nr_items (list_id)`,
		`CREATE INDEX nr_items_body_idx ON nr_items (body)`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON nr_lists, nr_items TO app_user`,
		`ALTER TABLE nr_lists ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE nr_items ENABLE ROW LEVEL SECURITY`,
		`CREATE POLICY nr_lists_all ON nr_lists USING (true) WITH CHECK (true)`,
		`CREATE POLICY nr_items_read ON nr_items FOR SELECT USING (true)`,
		`CREATE POLICY nr_items_ins ON nr_items FOR INSERT WITH CHECK (owner_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer)`,
		`CREATE POLICY nr_items_upd ON nr_items FOR UPDATE USING (owner_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer)`,
		`CREATE POLICY nr_items_del ON nr_items FOR DELETE USING (owner_id = NULLIF(current_setting('pdbq.claims.user_id', true), '')::integer)`,
		`INSERT INTO nr_lists (owner_id, name) VALUES (2, 'shared')`,
		`INSERT INTO nr_items (list_id, owner_id, body) SELECT id, 1, 'ada' FROM nr_lists`,
		`INSERT INTO nr_items (list_id, owner_id, body) SELECT id, 2, 'grace' FROM nr_lists`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP TABLE nr_items; DROP TABLE nr_lists`)
	})
	ctx := context.Background()
	var listID, adaItem, graceItem int
	if err := conn.QueryRow(ctx, `SELECT l.id, a.id, g.id FROM nr_lists l, nr_items a, nr_items g WHERE a.body = 'ada' AND g.body = 'grace'`).Scan(&listID, &adaItem, &graceItem); err != nil {
		t.Fatal(err)
	}
	hs := testServer(t, func(cfg *config.Config) {
		cfg.RLS.Enabled = true
		cfg.RLS.Auth.Mode = "headers"
		cfg.RLS.RoleClaim = "role"
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

	t.Run("own rows", func(t *testing.T) {
		res := asGrace(t, fmt.Sprintf(`mutation { updateNrListById(id: %d, patch: {nrItemsByListId: {updateById: [{id: %d, nrItemPatch: {body: "grace2"}}]}}) {
			nrList { nrItemsByListId(orderBy: BODY_ASC) { nodes { body } } } } }`, listID, graceItem))
		requireNoErrors(t, res)
		want := normalizeStr(t, `{"nrList": {"nrItemsByListId": {"nodes": [{"body": "ada"}, {"body": "grace2"}]}}}`)
		if got := normalize(t, res.Data["updateNrListById"]); got != want {
			t.Errorf("got %s\nwant %s", got, want)
		}
	})

	for _, tc := range []struct{ name, op, path string }{
		{"update", fmt.Sprintf(`updateById: [{id: %d, nrItemPatch: {body: "hacked"}}]`, adaItem), "updateById[0]"},
		{"delete", fmt.Sprintf(`deleteById: [{id: %d}]`, adaItem), "deleteById[0]"},
		{"disconnect", fmt.Sprintf(`disconnectById: [{id: %d}]`, adaItem), ""},
	} {
		t.Run("another owner's row: "+tc.name, func(t *testing.T) {
			res := asGrace(t, fmt.Sprintf(`mutation { updateNrListById(id: %d, patch: {name: "renamed", nrItemsByListId: {%s}}) {
				nrList { name nrItemsByListId { totalCount } } } }`, listID, tc.op))
			var body string
			var listed any
			if err := conn.QueryRow(ctx, `SELECT body, list_id FROM nr_items WHERE id = $1`, adaItem).Scan(&body, &listed); err != nil {
				t.Fatal(err)
			}
			if body != "ada" || listed == nil {
				t.Errorf("RLS-protected row changed: body=%q list_id=%v", body, listed)
			}
			if tc.path == "" {
				requireNoErrors(t, res)
				want := normalizeStr(t, `{"nrList": {"name": "renamed", "nrItemsByListId": {"totalCount": 2}}}`)
				if got := normalize(t, res.Data["updateNrListById"]); got != want {
					t.Errorf("got %s\nwant %s", got, want)
				}
				return
			}
			if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Message, "nrItemsByListId."+tc.path+" matched 0 rows") {
				t.Fatalf("want a no-match error, got %+v", res.Errors)
			}
		})
	}
}
