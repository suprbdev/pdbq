package e2e

import (
	"context"
	"encoding/base64"
	"fmt"
	"slices"
	"testing"
)

type ocPage struct {
	Nodes []struct {
		N int `json:"n"`
	} `json:"nodes"`
	Edges []struct {
		Cursor string `json:"cursor"`
		Node   struct {
			N int `json:"n"`
		} `json:"node"`
	} `json:"edges"`
	PageInfo struct {
		HasNextPage     bool   `json:"hasNextPage"`
		HasPreviousPage bool   `json:"hasPreviousPage"`
		StartCursor     string `json:"startCursor"`
		EndCursor       string `json:"endCursor"`
	} `json:"pageInfo"`
}

func ocFetch(t *testing.T, url, args string) ocPage {
	t.Helper()
	res := post(t, url, `{ allOcItems(`+args+`) {
		nodes { n } edges { cursor node { n } }
		pageInfo { hasNextPage hasPreviousPage startCursor endCursor } } }`, nil)
	requireNoErrors(t, res)
	var p ocPage
	mustUnmarshal(t, res.Data["allOcItems"], &p)
	return p
}

func ocNs(p ocPage) []int {
	out := make([]int, len(p.Nodes))
	for i, n := range p.Nodes {
		out[i] = n.N
	}
	return out
}

func ocCursorN(t *testing.T, cur string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(cur)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestOffsetCursors checks that offset-mode cursors (PK-less tables) name the
// absolute position of their row regardless of the offset argument.
func TestOffsetCursors(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE TABLE oc_items (n integer NOT NULL, g integer NOT NULL)`,
		`CREATE TABLE oc_keyed (id integer PRIMARY KEY, g integer NOT NULL)`,
		`CREATE INDEX ON oc_keyed (g)`,
		`INSERT INTO oc_keyed SELECT i, i FROM generate_series(1, 10) AS i`,
		`INSERT INTO oc_items SELECT i, i % 5 FROM generate_series(1, 10) AS i`,
	)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP TABLE oc_items, oc_keyed`) })
	hs := testServer(t, nil)

	p := ocFetch(t, hs.URL, `offset: 2, first: 2`)
	if got := ocNs(p); !slices.Equal(got, []int{3, 4}) {
		t.Fatalf("offset 2 first 2: got %v", got)
	}
	for i, want := range []string{"o:3", "o:4"} {
		if got := ocCursorN(t, p.Edges[i].Cursor); got != want {
			t.Errorf("edge %d cursor = %q, want %q", i, got, want)
		}
	}
	if got := ocCursorN(t, p.PageInfo.StartCursor); got != "o:3" {
		t.Errorf("startCursor = %q, want o:3", got)
	}
	if got := ocCursorN(t, p.PageInfo.EndCursor); got != "o:4" {
		t.Errorf("endCursor = %q, want o:4", got)
	}
	if !p.PageInfo.HasNextPage || !p.PageInfo.HasPreviousPage {
		t.Errorf("pageInfo = %+v, want both true", p.PageInfo)
	}

	next := ocFetch(t, hs.URL, fmt.Sprintf(`first: 3, after: %q`, p.PageInfo.EndCursor))
	if got := ocNs(next); !slices.Equal(got, []int{5, 6, 7}) {
		t.Errorf("after endCursor: got %v, want [5 6 7]", got)
	}

	both := ocFetch(t, hs.URL, fmt.Sprintf(`offset: 1, first: 2, after: %q`, p.PageInfo.EndCursor))
	if got := ocNs(both); !slices.Equal(got, []int{6, 7}) {
		t.Errorf("offset 1 after endCursor: got %v, want [6 7]", got)
	}
	if got := ocCursorN(t, both.PageInfo.EndCursor); got != "o:7" {
		t.Errorf("endCursor after offset+after = %q, want o:7", got)
	}

	res := post(t, hs.URL, `{ allOcKeyeds(distinctOn: [G], orderBy: [G_ASC], offset: 1, first: 2) { edges { cursor } } }`, nil)
	requireNoErrors(t, res)
	var dist ocPage
	mustUnmarshal(t, res.Data["allOcKeyeds"], &dist)
	if len(dist.Edges) != 2 {
		t.Fatalf("distinctOn offset 1 first 2: got %d edges", len(dist.Edges))
	}
	for i, want := range []string{"o:2", "o:3"} {
		if got := ocCursorN(t, dist.Edges[i].Cursor); got != want {
			t.Errorf("distinctOn edge %d cursor = %q, want %q", i, got, want)
		}
	}
}
