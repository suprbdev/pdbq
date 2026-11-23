package e2e

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

// TestAppExecuteIdentity covers the identity of pdbq query (App.Execute):
// the connection role without claims by default, and --role / --claims
// applied like an HTTP request's role and claims, on a config whose auth
// and role switch are off (the CLI's ConnectionIdentity).
func TestAppExecuteIdentity(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	conn := testConn(t)
	execAll(t, conn,
		`CREATE FUNCTION cli_whoami() RETURNS text LANGUAGE sql STABLE AS $$
			SELECT current_user || ':' || coalesce(current_setting('jwt.claims.user_id', true), '') || ':' ||
				coalesce(current_setting('request.jwt.claims', true), '')
		$$`,
	)
	t.Cleanup(func() { _, _ = conn.Exec(context.Background(), `DROP FUNCTION cli_whoami()`) })
	var login string
	if err := conn.QueryRow(context.Background(), `SELECT current_user`).Scan(&login); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.Claims.JSONSetting = "request.jwt.claims"
	cfg.ConnectionIdentity()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	for _, tc := range []struct {
		name   string
		role   string
		claims map[string]any
		want   string
	}{
		{"default", "", nil, login + "::{}"},
		{"role", "app_user", nil, "app_user::{}"},
		{"claims", "", map[string]any{"user_id": 7}, login + `:7:{"user_id":7}`},
		{"role and claims", "anonymous", map[string]any{"user_id": "u1"}, `anonymous:u1:{"user_id":"u1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := app.Execute(context.Background(), pdbq.ExecRequest{Query: `{ cliWhoami }`, Role: tc.role, Claims: tc.claims})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Errors) > 0 {
				t.Fatalf("errors: %v", res.Errors)
			}
			var got string
			if err := json.Unmarshal(res.Data["cliWhoami"], &got); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
