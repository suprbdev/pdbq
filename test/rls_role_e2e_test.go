package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

// TestRLSRejectsReservedRoles checks that a role claim naming none, public
// or a pg_ role is refused instead of being assumed: "none" in particular is
// RESET ROLE, which would run the request as the pool's login role and
// bypass RLS. It runs with and without roles.allowed, since an empty
// list lets any other role through to the database.
func TestRLSRejectsReservedRoles(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	var loginTotal, published int
	err = conn.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE published) FROM public.posts`).Scan(&loginTotal, &published)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loginTotal == published {
		t.Fatalf("fixture has no unpublished posts (%d posts): the test cannot tell the login role from the anonymous role", loginTotal)
	}

	type outcome struct {
		status int
		errors int
		posts  int
		data   bool
	}
	run := func(t *testing.T, srvURL, role string) outcome {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"query": `{allPosts(first: 100) { nodes { id } }}`})
		req, err := http.NewRequest("POST", srvURL+"/graphql", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pdbq-Claim-Role", role)
		req.Header.Set("X-Pdbq-Claim-User-Id", "1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		out := outcome{status: resp.StatusCode}
		var res gqlResp
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatalf("bad response (%d): %s", resp.StatusCode, raw)
		}
		out.errors = len(res.Errors)
		if v, ok := res.Data["allPosts"]; ok && string(v) != "null" {
			var c struct {
				Nodes []any `json:"nodes"`
			}
			mustUnmarshal(t, v, &c)
			out.data, out.posts = true, len(c.Nodes)
		}
		return out
	}

	for name, allowed := range map[string][]string{
		"any role allowed":  nil,
		"allowed_roles set": {"app_user"},
	} {
		t.Run(name, func(t *testing.T) {
			hs := testServer(t, func(cfg *config.Config) {
				headersIdentity(cfg)
				cfg.Roles.Allowed = allowed
			})
			if got := run(t, hs.URL, "app_user"); got.status != http.StatusOK || got.errors != 0 || got.posts != published+1 {
				t.Fatalf("app_user control request = %+v, want 200 with %d posts", got, published+1)
			}
			for _, role := range []string{"none", "NONE", "None", "public", "pg_read_all_data", "pg_database_owner"} {
				got := run(t, hs.URL, role)
				if got.data {
					t.Errorf("role %q returned %d posts (login role sees %d): reserved role was not refused", role, got.posts, loginTotal)
				}
				if got.status != http.StatusForbidden || got.errors == 0 {
					t.Errorf("role %q = %+v, want 403 with an error", role, got)
				}
			}
		})
	}
}
