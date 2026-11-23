package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

// TestKeysetPaging walks connections forward (first/after) and backward
// (last/before) one page at a time over orderings with ties, NULLs in both
// sort directions, uniform and mixed directions, and checks every walk
// reproduces the order PostgreSQL itself produces.
func TestKeysetPaging(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE ks_items (id integer PRIMARY KEY, name text NOT NULL, score integer, grp integer NOT NULL)`,
		`CREATE INDEX ON ks_items (name)`,
		`CREATE INDEX ON ks_items (score)`,
		`CREATE INDEX ON ks_items (grp)`,
		`INSERT INTO ks_items SELECT i, 'n' || (i % 7), CASE WHEN i % 5 = 0 THEN NULL ELSE i % 4 END, i % 3 FROM generate_series(1, 47) AS i`,
	)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP TABLE ks_items`) })
	hs := testServer(t, nil)

	for _, tc := range []struct{ order, sql string }{
		{"NAME_ASC", "name, id"},
		{"NAME_DESC", "name DESC, id"},
		{"NAME_DESC, ID_DESC", "name DESC, id DESC"},
		{"NAME_ASC, ID_DESC", "name, id DESC"},
		{"SCORE_ASC", "score, id"},
		{"SCORE_DESC", "score DESC NULLS LAST, id"},
		{"SCORE_DESC, ID_DESC", "score DESC NULLS LAST, id DESC"},
		{"GRP_ASC, NAME_ASC", "grp, name, id"},
		{"GRP_ASC, SCORE_DESC", "grp, score DESC NULLS LAST, id"},
		{"GRP_DESC, SCORE_ASC, NAME_DESC", "grp DESC, score, name DESC, id"},
		{"SCORE_ASC, GRP_DESC", "score, grp DESC, id"},
	} {
		t.Run(tc.order, func(t *testing.T) {
			want := queryIDs(t, conn, `SELECT id FROM ks_items ORDER BY `+tc.sql)
			forward := ksWalk(t, hs.URL, "allKsItems", tc.order, false)
			if !slices.Equal(forward, want) {
				t.Errorf("forward walk diverged:\nwant %v\ngot  %v", want, forward)
			}
			backward := ksWalk(t, hs.URL, "allKsItems", tc.order, true)
			if !slices.Equal(backward, want) {
				t.Errorf("backward walk diverged:\nwant %v\ngot  %v", want, backward)
			}
		})
	}
}

// queryIDs returns the integer first column of every row of query.
func queryIDs(t *testing.T, conn *pgx.Conn, query string) []int {
	t.Helper()
	rows, err := conn.Query(context.Background(), query)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// ksWalk pages through the connection field under order five rows at a
// time and returns the ids in connection order: forward follows endCursor with
// first/after; backward starts from the end with last and follows
// startCursor with before, prepending each page.
func ksWalk(t *testing.T, url, field, order string, backward bool) []int {
	t.Helper()
	var ids []int
	cursor := ""
	for range 30 {
		args := "first: 5"
		if backward {
			args = "last: 5"
		}
		if cursor != "" {
			if backward {
				args += fmt.Sprintf(", before: %q", cursor)
			} else {
				args += fmt.Sprintf(", after: %q", cursor)
			}
		}
		res := post(t, url, `{`+field+`(`+args+`, orderBy: [`+order+`]) {
			nodes { id } pageInfo { hasNextPage hasPreviousPage startCursor endCursor } }}`, nil)
		requireNoErrors(t, res)
		var page struct {
			Nodes []struct {
				ID int `json:"id"`
			} `json:"nodes"`
			PageInfo struct {
				HasNextPage     bool   `json:"hasNextPage"`
				HasPreviousPage bool   `json:"hasPreviousPage"`
				StartCursor     string `json:"startCursor"`
				EndCursor       string `json:"endCursor"`
			} `json:"pageInfo"`
		}
		mustUnmarshal(t, res.Data[field], &page)
		got := make([]int, len(page.Nodes))
		for i, n := range page.Nodes {
			got[i] = n.ID
		}
		if backward {
			ids = append(got, ids...)
			if !page.PageInfo.HasPreviousPage {
				return ids
			}
			cursor = page.PageInfo.StartCursor
		} else {
			ids = append(ids, got...)
			if !page.PageInfo.HasNextPage {
				return ids
			}
			cursor = page.PageInfo.EndCursor
		}
	}
	t.Fatalf("walk over %s did not terminate: %v", order, ids)
	return nil
}

// TestKeysetIndexBound checks that a keyset page deep into a non-primary-key
// order is answered by an index range scan on the order column (Index Cond)
// rather than a scan of the index from its start that filters out every row
// before the cursor.
func TestKeysetIndexBound(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE ks_big (id integer PRIMARY KEY, name text NOT NULL, grp integer NOT NULL)`,
		`CREATE INDEX ks_big_name_idx ON ks_big (name)`,
		`CREATE INDEX ON ks_big (grp)`,
		`INSERT INTO ks_big SELECT i, 'n' || lpad(((i * 7919) % 20000)::text, 5, '0'), i % 3 FROM generate_series(1, 20000) AS i`,
		`ANALYZE ks_big`,
	)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP TABLE ks_big`) })

	var anchor int
	if err := conn.QueryRow(context.Background(), `SELECT id FROM ks_big ORDER BY name, id OFFSET 15000 LIMIT 1`).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	cursor := base64.StdEncoding.EncodeToString(fmt.Appendf(nil, `["KsBig", %d]`, anchor))

	cfg := config.DefaultConfig()
	cfg.Database.URL = os.Getenv("PDBQ_TEST_DATABASE_URL")
	cfg.Plugins.Disabled = []string{"simple-names"}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)

	for _, args := range []string{
		`first: 10, after: %q, orderBy: [NAME_ASC]`,
		`first: 10, after: %q, orderBy: [NAME_DESC]`,
		`first: 10, after: %q, orderBy: [NAME_ASC, GRP_DESC]`,
		`last: 10, before: %q, orderBy: [NAME_ASC]`,
		`last: 10, before: %q, orderBy: [NAME_DESC]`,
	} {
		args = fmt.Sprintf(args, cursor)
		t.Run(args, func(t *testing.T) {
			stmts, err := app.CompileQuery(context.Background(), `{allKsBigs(`+args+`) { nodes { id } pageInfo { hasNextPage } }}`, nil, "")
			if err != nil || len(stmts) != 1 {
				t.Fatalf("compile: %v %v", stmts, err)
			}
			rows, err := conn.Query(context.Background(), "EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF) "+stmts[0].SQL, stmts[0].Args...)
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for rows.Next() {
				var line string
				if err := rows.Scan(&line); err != nil {
					t.Fatal(err)
				}
				lines = append(lines, line)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			plan := strings.Join(lines, "\n")
			if !strings.Contains(plan, "Index Cond: (name ") {
				t.Errorf("no index bound on name:\n%s", plan)
			}
			for _, l := range lines {
				var removed int
				if _, err := fmt.Sscanf(strings.TrimSpace(l), "Rows Removed by Filter: %d", &removed); err == nil && removed > 100 {
					t.Errorf("scan discarded %d rows before the cursor:\n%s", removed, plan)
				}
			}
		})
	}
}
