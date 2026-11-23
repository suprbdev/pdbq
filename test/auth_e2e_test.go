package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/suprbdev/pdbq/internal/config"
)

const idtSchemaSQL = `
DROP SCHEMA IF EXISTS idt CASCADE;
CREATE SCHEMA idt;
CREATE FUNCTION idt.idt_json_claims() RETURNS text STABLE LANGUAGE sql AS $$
    SELECT current_setting('request.jwt.claims', true)
$$;
CREATE FUNCTION idt.idt_json_user() RETURNS text STABLE LANGUAGE sql AS $$
    SELECT current_user || ':' || coalesce(current_setting('request.jwt.claims', true)::json->>'user_id', '')
        || ':' || coalesce(current_setting('jwt.claims.user_id', true), '')
$$;
CREATE FUNCTION idt.idt_many() RETURNS integer STABLE LANGUAGE sql AS $$
    SELECT (SELECT n FROM generate_series(1, 2) AS n)
$$;
GRANT USAGE ON SCHEMA idt TO anonymous, app_user;
GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA idt TO anonymous, app_user;
`

// idtServer creates the idt schema (identity probe functions) and serves
// it next to public; the schema is dropped when the test ends.
func idtServer(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, idtSchemaSQL); err != nil {
		_ = conn.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `DROP SCHEMA IF EXISTS idt CASCADE`)
		_ = conn.Close(ctx)
	})
	hs := testServer(t, func(c *config.Config) {
		c.Schema.Schemas = []string{"public", "idt"}
		if mutate != nil {
			mutate(c)
		}
	})
	return hs.URL
}

func postStatus(t *testing.T, url, query string, headers map[string]string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query})
	req, err := http.NewRequest("POST", url+"/graphql", bytes.NewReader(body))
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
	_ = resp.Body.Close()
	return resp
}

// TestAnonymousRefused: with the default roles (no anonymous role) a
// request without credentials is answered 401 with a Bearer challenge.
func TestAnonymousRefused(t *testing.T) {
	hs := testServer(t, func(c *config.Config) {
		jwtIdentity(c, "e2e-anon-secret")
		c.Roles.Anonymous = ""
	})
	resp := postStatus(t, hs.URL, `{ __typename }`, nil)
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != "Bearer" {
		t.Fatalf("status %d, WWW-Authenticate %q, want 401 Bearer", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
	resp = postStatus(t, hs.URL, `{ __typename }`, map[string]string{"Authorization": "Bearer nope"})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("status %d, WWW-Authenticate %q, want 401 invalid_token", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// TestHeadersModeFailClosed: claim headers from a peer outside
// auth.headers.trusted_proxies are refused, not ignored.
func TestHeadersModeFailClosed(t *testing.T) {
	hs := testServer(t, func(c *config.Config) {
		headersIdentity(c)
		c.Auth.Headers.TrustedProxies = []string{"10.0.0.1"}
	})
	if resp := postStatus(t, hs.URL, `{ __typename }`, nil); resp.StatusCode != http.StatusOK {
		t.Errorf("anonymous request: status %d, want 200", resp.StatusCode)
	}
	resp := postStatus(t, hs.URL, `{ __typename }`, map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": "1"})
	if resp.StatusCode != http.StatusUnauthorized || resp.Header.Get("WWW-Authenticate") != `Bearer error="invalid_token"` {
		t.Fatalf("claim headers from an untrusted peer: status %d, WWW-Authenticate %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
	}
}

// TestClaimsJSONSetting: claims.json_setting exposes every claim as one
// JSON object (PostgREST's request.jwt.claims), next to the per-claim
// jwt.claims.* settings, and '{}' for a request without claims.
func TestClaimsJSONSetting(t *testing.T) {
	url := idtServer(t, func(c *config.Config) {
		c.Database.MaxConns = 1
		headersIdentity(c)
		c.Claims.JSONSetting = "request.jwt.claims"
	})
	user := map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": "7"}
	steps := []struct {
		query   string
		headers map[string]string
		want    string
	}{
		{`{ idtJsonUser }`, user, `{"data":{"idtJsonUser":"app_user:7:7"}}`},
		{`{ idtJsonClaims }`, user, `{"data":{"idtJsonClaims":"{\"role\":\"app_user\",\"user_id\":\"7\"}"}}`},
		{`{ idtJsonClaims }`, nil, `{"data":{"idtJsonClaims":"{}"}}`},
		{`{ idtJsonUser }`, nil, `{"data":{"idtJsonUser":"anonymous::"}}`},
	}
	for i, s := range steps {
		if got := postRaw(t, url, s.query, s.headers); got != s.want {
			t.Errorf("step %d %s:\ngot  %s\nwant %s", i, s.query, got, s.want)
		}
	}
}

// TestRoleSwitchOff: with roles.switch off, requests run as the connection
// role while their claims are still set.
func TestRoleSwitchOff(t *testing.T) {
	url := idtServer(t, func(c *config.Config) {
		headersIdentity(c)
		c.Roles.Switch = false
	})
	got := postRaw(t, url, `{ idtJsonUser }`, map[string]string{"X-Pdbq-Claim-Role": "app_user", "X-Pdbq-Claim-User-Id": "7"})
	if got != `{"data":{"idtJsonUser":"pdbq::7"}}` {
		t.Errorf("got %s, want the connection role with the claim", got)
	}
}

// TestCardinalityViolationIsUserError: SQLSTATE 21000 (a scalar subquery
// returning several rows) passes its message through in prod and is
// hidden in strict.
func TestCardinalityViolationIsUserError(t *testing.T) {
	for detail, want := range map[string]string{
		"prod":   "more than one row returned by a subquery used as an expression",
		"strict": "invalid input value",
	} {
		t.Run(detail, func(t *testing.T) {
			url := idtServer(t, func(c *config.Config) { c.Errors.Detail = detail })
			got := postRaw(t, url, `{ idtMany }`, nil)
			if !strings.Contains(got, `"message":"`+want+`"`) || !strings.Contains(got, `"code":"21000"`) {
				t.Errorf("got %s, want message %q", got, want)
			}
		})
	}
}

// TestCORSHeadersE2E: an allowed origin gets Access-Control-Allow-Origin
// and the exposed headers, and Vary keeps both Origin and Accept-Encoding
// with compression on.
func TestCORSHeadersE2E(t *testing.T) {
	hs := testServer(t, func(c *config.Config) {
		c.Server.CORSOrigins = []string{"https://app.example"}
		c.Server.Compression = true
	})
	resp := postStatus(t, hs.URL, `{ __typename }`, map[string]string{"Origin": "https://app.example", "Accept-Encoding": "gzip"})
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}
	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "WWW-Authenticate" {
		t.Errorf("Access-Control-Expose-Headers = %q", got)
	}
	vary := strings.Join(resp.Header.Values("Vary"), ", ")
	if !strings.Contains(vary, "Origin") || !strings.Contains(vary, "Accept-Encoding") {
		t.Errorf("Vary = %q, want Origin and Accept-Encoding", vary)
	}
}
