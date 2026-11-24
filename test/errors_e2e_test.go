package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

// codedResponse is an HTTP response with the code of its first GraphQL
// error.
type codedResponse struct {
	status      int
	contentType string
	code        string
	message     string
	body        string
}

func sendCoded(t *testing.T, req *http.Request) codedResponse {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := codedResponse{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: string(raw)}
	var body struct {
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &body) == nil && len(body.Errors) > 0 {
		out.code, _ = body.Errors[0].Extensions["code"].(string)
		out.message = body.Errors[0].Message
	}
	return out
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func gqlQuery(query string) string {
	b, _ := json.Marshal(map[string]any{"query": query})
	return string(b)
}

type codeCase struct {
	name   string
	url    string
	req    func(url string) *http.Request
	status int
	code   string
	msg    string
}

func runCodeCases(t *testing.T, cases []codeCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sendCoded(t, tc.req(tc.url))
			if got.status != tc.status || got.code != tc.code || !strings.Contains(got.message, tc.msg) {
				t.Fatalf("got %d %q %q (%s), want %d %q %q", got.status, got.code, got.message, got.body, tc.status, tc.code, tc.msg)
			}
			if !strings.HasPrefix(got.contentType, "application/json") {
				t.Errorf("Content-Type %q", got.contentType)
			}
		})
	}
}

func post200(query string) func(string) *http.Request {
	return func(url string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, url+"/graphql", strings.NewReader(gqlQuery(query)))
		req.Header.Set("Content-Type", "application/json")
		return req
	}
}

func rawPost(body string, header ...string) func(string) *http.Request {
	return func(url string) *http.Request {
		req, _ := http.NewRequest(http.MethodPost, url+"/graphql", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		return req
	}
}

func rawRequest(method, path, contentType, body string) func(string) *http.Request {
	return func(url string) *http.Request {
		req, _ := http.NewRequest(method, url+path, strings.NewReader(body))
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return req
	}
}

// TestErrorCodesE2E asserts one response per reachable row of the code
// table in docs/errors.md.
func TestErrorCodesE2E(t *testing.T) {
	conn := testConn(t)
	execAll(t, conn,
		`CREATE FUNCTION err_probe(state text) RETURNS integer LANGUAGE plpgsql STABLE AS $$ BEGIN RAISE EXCEPTION 'probe %', state USING ERRCODE = state; END $$`,
		`CREATE FUNCTION err_sleep(seconds float8) RETURNS integer LANGUAGE plpgsql STABLE AS $$ BEGIN PERFORM pg_sleep(seconds); RETURN 1; END $$`,
		`CREATE TABLE err_parents (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, slug text NOT NULL UNIQUE)`,
		`CREATE TABLE err_children (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, parent_id integer REFERENCES err_parents (id), name text NOT NULL)`,
		`CREATE INDEX err_children_parent_id_idx ON err_children (parent_id)`,
	)
	t.Cleanup(func() {
		_, _ = conn.Exec(context.Background(), `DROP FUNCTION err_probe(text); DROP FUNCTION err_sleep(float8); DROP TABLE err_children; DROP TABLE err_parents`)
	})

	plain := testServer(t, func(c *config.Config) {
		c.Limits.MaxBodyBytes = 4096
		c.Limits.MaxDepth = 6
		c.Limits.MaxCost = 5000
		c.Limits.MaxPageSize = 50
		c.Limits.MaxQueryTokens = 400
		c.Limits.MaxIntrospectionBytes = 2048
		c.Subscriptions.Enabled = true
		c.Incremental.Enabled = true
		c.Database.StatementTimeout = 300 * time.Millisecond
	}).URL
	shallow := testServer(t, func(c *config.Config) { c.Limits.MaxDepth = 2 }).URL
	timeout := testServer(t, func(c *config.Config) {
		c.Server.RequestTimeout = 300 * time.Millisecond
		c.Database.StatementTimeout = 5 * time.Second
	}).URL
	locked := testServer(t, func(c *config.Config) {
		c.Server.ReadOnly = true
		c.Server.Introspection = false
		c.PersistedQueries.APQ = true
		c.PersistedQueries.Only = true
	}).URL
	apq := testServer(t, func(c *config.Config) { c.PersistedQueries.APQ = true }).URL
	const secret = "errors-e2e-secret-errors-e2e-secret"
	jwtURL := testServer(t, func(c *config.Config) {
		jwtIdentity(c, secret)
		c.Roles.Anonymous = ""
		c.Roles.Allowed = []string{"app_user"}
	}).URL
	adminToken, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"role": "admin", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	hashOnly := `{"extensions":{"persistedQuery":{"version":1,"sha256Hash":"` + sha256Hex(`{ neverRegistered }`) + `"}}}`

	runCodeCases(t, []codeCase{
		{"graphql_parse_failed", plain, post200(`{ allUsers {`), 200, "graphql_parse_failed", ""},
		{"graphql_validation_failed", plain, post200(`{ nope }`), 200, "graphql_validation_failed", ""},
		{"invalid_request body", plain, rawPost(`{`), 400, "invalid_request", "malformed request body"},
		{"invalid_request no query", plain, rawPost(`{}`), 400, "invalid_request", "missing query"},
		{"invalid_request operation", plain, rawPost(`{"query":"query A { __typename }","operationName":"B"}`), 200, "invalid_request", `operation "B" not found`},
		{"document_limit_exceeded", plain, post200(`{ a: __typename ` + strings.Repeat("b: __typename ", 200) + `}`), 200, "document_limit_exceeded", "token limit"},
		{"depth_exceeded", shallow, post200(`{ allUsers(first: 1) { nodes { id } } }`), 200, "depth_exceeded", ""},
		{"cost_exceeded", plain, post200(`{ allUsers(first: 50) { nodes { postsByAuthorId(first: 50) { nodes { id title } } } } }`), 200, "cost_exceeded", ""},
		{"page_size_exceeded", plain, post200(`{ allUsers(first: 51) { nodes { id } } }`), 200, "page_size_exceeded", ""},
		{"invalid_cursor", plain, post200(`{ allUsers(after: "bm9wZQ==") { nodes { id } } }`), 200, "invalid_cursor", ""},
		{"invalid_argument", plain, post200(`{ allUsers(first: -1) { nodes { id } } }`), 200, "invalid_argument", ""},
		{"introspection_limit_exceeded", plain, post200(`{ __schema { types { name fields { name } } } }`), 200, "introspection_limit_exceeded", ""},
		{"introspection_disabled", locked, rawPost(`{"query":"{ __schema { queryType { name } } }","extensions":{"persistedQuery":{"version":1,"sha256Hash":"` + sha256Hex(`{ __schema { queryType { name } } }`) + `"}}}`), 200, "introspection_disabled", ""},
		{"read_only", locked, rawPost(`{"query":"mutation { __typename }","extensions":{"persistedQuery":{"version":1,"sha256Hash":"` + sha256Hex(`mutation { __typename }`) + `"}}}`), 200, "read_only", ""},
		{"partial_mutations_disabled", plain, rawPost(`{"query":"mutation { __typename }","extensions":{"partialMutations":true}}`), 200, "partial_mutations_disabled", ""},
		{"operation_not_supported", plain, post200(`subscription { listen(topic: "a") { relatedNodeId } }`), 200, "operation_not_supported", "WebSocket"},
		{"not_found root", plain, post200(`mutation { deleteUserById(input: {id: 999999}) { deletedUserNodeId } }`), 200, "not_found", `no row matched "deleteUserById"`},
		{"not_found nested", plain, post200(`mutation { createErrChild(input: {errChild: {name: "x", errParentByParentId: {connectBySlug: {slug: "nope"}}}}) { errChild { id } } }`), 200, "not_found", "errParentByParentId.connectBySlug matched 0 rows"},
		{"not_found path", plain, rawRequest(http.MethodGet, "/nope", "", ""), 404, "not_found", ""},
		{"pg:<SQLSTATE>", plain, post200(`{ errProbe(state: "23505") }`), 200, "pg:23505", "probe 23505"},
		{"pg:57014", plain, post200(`{ errSleep(seconds: 5) }`), 200, "pg:57014", ""},
		{"auth_required", jwtURL, post200(`{ __typename }`), 401, "auth_required", ""},
		{"invalid_token", jwtURL, rawPost(gqlQuery(`{ __typename }`), "Authorization", "Bearer nope"), 401, "invalid_token", ""},
		{"role_not_allowed", jwtURL, rawPost(gqlQuery(`{ __typename }`), "Authorization", "Bearer "+adminToken), 403, "role_not_allowed", ""},
		{"method_not_allowed GET mutation", plain, rawRequest(http.MethodGet, "/graphql?query=mutation%7B__typename%7D", "", ""), 405, "method_not_allowed", ""},
		{"method_not_allowed PUT", plain, rawRequest(http.MethodPut, "/graphql", "application/json", "{}"), 405, "method_not_allowed", ""},
		{"unsupported_media_type", plain, rawRequest(http.MethodPost, "/graphql", "text/plain", gqlQuery(`{ __typename }`)), 415, "unsupported_media_type", ""},
		{"not_acceptable", plain, rawPost(gqlQuery(`{ __typename }`), "Accept", "multipart/mixed;deferSpec=1999"), 406, "not_acceptable", ""},
		{"body_too_large", plain, rawPost(gqlQuery(`{ __typename }` + strings.Repeat(" ", 5000))), 413, "body_too_large", ""},
		{"persisted_query_not_supported", plain, rawPost(hashOnly), 200, "persisted_query_not_supported", "PersistedQueryNotSupported"},
		{"persisted_query_not_found", apq, rawPost(hashOnly), 200, "persisted_query_not_found", "PersistedQueryNotFound"},
		{"persisted_query_required", locked, post200(`{ __typename }`), 400, "persisted_query_required", ""},
		{"timeout", timeout, post200(`{ errSleep(seconds: 2) }`), 504, "timeout", "request timeout"},
	})
}

// TestUnavailableCodeE2E boots from a schema cache against a database that
// refuses connections: data requests and /readyz answer unavailable.
func TestUnavailableCodeE2E(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	noIdentity(&cfg)
	live := pdbq.New(cfg)
	t.Cleanup(live.Close)
	cat, err := live.LoadCatalog(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "schema.json")
	if err := cache.Save(path, cat, cache.Writer{Product: "pdbq"}); err != nil {
		t.Fatal(err)
	}
	cfg.Database.URL = "postgres://pdbq:pdbq@127.0.0.1:1/pdbq_test?connect_timeout=2"
	cfg.Schema.CachePath = path
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	h, err := app.Handler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	runCodeCases(t, []codeCase{
		{"query", hs.URL, post200(`{ allUsers(first: 1) { nodes { id } } }`), 200, "unavailable", "database unavailable"},
		{"readyz", hs.URL, rawRequest(http.MethodGet, "/readyz", "", ""), 503, "unavailable", "database unavailable"},
	})
}
