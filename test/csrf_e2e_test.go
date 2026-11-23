package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCSRFRequestsDoNotMutate(t *testing.T) {
	hs := testServer(t, nil)
	const title = "csrf probe"
	mutation := fmt.Sprintf(`mutation { createPost(input: {authorId: 1, title: %q}) { post { id } } }`, title)
	body, _ := json.Marshal(map[string]any{"query": mutation})

	send := func(t *testing.T, method, rawURL, contentType string, payload string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, rawURL, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	t.Run("mutation over GET", func(t *testing.T) {
		code, raw := send(t, http.MethodGet, hs.URL+"/graphql?"+url.Values{"query": {mutation}}.Encode(), "", "")
		if code != http.StatusMethodNotAllowed {
			t.Errorf("GET mutation: %d %s, want 405", code, raw)
		}
	})
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		t.Run("POST "+ct, func(t *testing.T) {
			code, raw := send(t, http.MethodPost, hs.URL+"/graphql", ct, string(body))
			if code != http.StatusUnsupportedMediaType {
				t.Errorf("POST %q: %d %s, want 415", ct, code, raw)
			}
		})
	}

	res := post(t, hs.URL, fmt.Sprintf(`{ allPosts(condition: {title: %q}) { totalCount } }`, title), nil)
	requireNoErrors(t, res)
	if got := string(res.Data["allPosts"]); !strings.Contains(strings.ReplaceAll(got, " ", ""), `"totalCount":0`) {
		t.Fatalf("rejected requests created posts: %s", got)
	}

	t.Run("query over GET", func(t *testing.T) {
		q := url.Values{"query": {`{ allPosts(condition: {authorId: 1}) { totalCount } }`}}.Encode()
		code, raw := send(t, http.MethodGet, hs.URL+"/graphql?"+q, "", "")
		if code != http.StatusOK || !strings.Contains(raw, "totalCount") {
			t.Errorf("GET query: %d %s", code, raw)
		}
	})
}
