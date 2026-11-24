package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/suprbdev/pdbq/internal/config"
)

const apqSecret = "e2e-apq-secret"

func apqToken(t *testing.T) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"role": "app_user", "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(apqSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// apqPost posts query (empty for a hash-only request) with its
// persistedQuery hash and returns the status and body.
func apqPost(t *testing.T, url, query, hash, token string) (int, string) {
	t.Helper()
	body := map[string]any{"extensions": map[string]any{"persistedQuery": map[string]any{"version": 1, "sha256Hash": hash}}}
	if query != "" {
		body["query"] = query
	}
	b, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", url+"/graphql", strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// TestAPQRegistrationRequiresAuthentication: with jwt and no anonymous
// role, a registration without credentials is answered 401 and not stored:
// a later hash-only request misses (PersistedQueryNotFound) rather than
// finding it. An authenticated registration of a valid document is stored;
// one above persisted_queries.max_entry_bytes executes but is not.
func TestAPQRegistrationRequiresAuthentication(t *testing.T) {
	url := testServer(t, func(c *config.Config) {
		jwtIdentity(c, apqSecret)
		c.Roles.Anonymous = ""
		c.PersistedQueries.APQ = true
		c.PersistedQueries.MaxEntryBytes = 4 << 10
	}).URL
	tok := apqToken(t)

	for i := range 3 {
		q := fmt.Sprintf("{ __typename } # %d %s", i, strings.Repeat("x", 16<<10))
		if code, body := apqPost(t, url, q, sha256Hex(q), ""); code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated registration: %d %s, want 401", code, body)
		}
		if code, body := apqPost(t, url, "", sha256Hex(q), ""); code != http.StatusOK || !strings.Contains(body, "PersistedQueryNotFound") {
			t.Fatalf("hash-only after an unauthenticated registration: %d %s, want PersistedQueryNotFound", code, body)
		}
	}

	small := `{ allUsers(first: 1) { totalCount } }`
	if code, body := apqPost(t, url, small, sha256Hex(small), tok); code != http.StatusOK || !strings.Contains(body, "totalCount") {
		t.Fatalf("authenticated registration: %d %s", code, body)
	}
	if code, body := apqPost(t, url, "", sha256Hex(small), tok); code != http.StatusOK || !strings.Contains(body, "totalCount") {
		t.Fatalf("hash-only after an authenticated registration: %d %s", code, body)
	}

	big := fmt.Sprintf("{ __typename } # %s", strings.Repeat("y", 8<<10))
	if code, body := apqPost(t, url, big, sha256Hex(big), tok); code != http.StatusOK || !strings.Contains(body, `"__typename":"Query"`) {
		t.Fatalf("oversized registration: %d %s, want it executed", code, body)
	}
	if code, body := apqPost(t, url, "", sha256Hex(big), tok); !strings.Contains(body, "PersistedQueryNotFound") {
		t.Fatalf("hash-only after an oversized registration: %d %s, want PersistedQueryNotFound", code, body)
	}
}

// TestSchemaSDLRequiresCredentials: /schema.graphql has the authentication
// requirement of an introspection query on /graphql: without credentials
// and without an anonymous role both are 401, with a token both answer.
func TestSchemaSDLRequiresCredentials(t *testing.T) {
	url := testServer(t, func(c *config.Config) {
		jwtIdentity(c, apqSecret)
		c.Roles.Anonymous = ""
	}).URL
	get := func(token string) (int, string) {
		req, err := http.NewRequest("GET", url+"/schema.graphql", nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	if resp := postStatus(t, url, `{ __schema { queryType { name } } }`, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous __schema: %d, want 401", resp.StatusCode)
	}
	if code, body := get(""); code != http.StatusUnauthorized || !strings.Contains(body, `"code":"auth_required"`) {
		t.Errorf("anonymous /schema.graphql: %d %s, want 401 auth_required", code, body)
	}
	if code, body := get("nope"); code != http.StatusUnauthorized || !strings.Contains(body, `"code":"invalid_token"`) {
		t.Errorf("/schema.graphql with an invalid token: %d %s, want 401 invalid_token", code, body)
	}
	if code, body := get(apqToken(t)); code != http.StatusOK || !strings.Contains(body, "type Query") {
		t.Errorf("authenticated /schema.graphql: %d %.200s, want the SDL", code, body)
	}

	open := testServer(t, func(c *config.Config) { jwtIdentity(c, apqSecret) }).URL
	resp, err := http.Get(open + "/schema.graphql")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("/schema.graphql with an anonymous role: %d, want 200", resp.StatusCode)
	}
}
