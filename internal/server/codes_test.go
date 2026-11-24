package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suprbdev/pdbcore/auth"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/errcode"
)

// errorBody decodes a GraphQL-shaped error response and returns the code
// of its first error.
func errorBody(t *testing.T, rec *httptest.ResponseRecorder) (errcode.Code, string) {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, body %s", ct, rec.Body)
	}
	var out struct {
		Errors []struct {
			Message    string         `json:"message"`
			Extensions map[string]any `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out.Errors) != 1 {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	code, _ := out.Errors[0].Extensions["code"].(string)
	return errcode.Code(code), out.Errors[0].Message
}

func TestHTTPErrorCodes(t *testing.T) {
	limits := config.Limits{MaxBodyBytes: 256}
	plain := apqServer(t, config.Config{Limits: limits, Server: config.Server{GraphiQL: true}}).Handler()
	apq := apqServer(t, config.Config{Limits: limits, PersistedQueries: config.PersistedQueries{APQ: true}}).Handler()
	only := apqServer(t, config.Config{Limits: limits, PersistedQueries: config.PersistedQueries{APQ: true, Only: true}}).Handler()
	pqFile := filepath.Join(t.TempDir(), "pq.json")
	if err := os.WriteFile(pqFile, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	static := apqServer(t, config.Config{Limits: limits, PersistedQueries: config.PersistedQueries{Path: pqFile}}).Handler()
	hashOnly := `{"extensions":` + apqExt(apqQuery) + `}`
	cases := []struct {
		name   string
		h      http.Handler
		req    *http.Request
		status int
		code   errcode.Code
		msg    string
	}{
		{"malformed body", plain, jsonRequest(`{`), http.StatusBadRequest, errcode.InvalidRequest, ""},
		{"too large", plain, jsonRequest(`{"query":"` + strings.Repeat(" ", 300) + `"}`), http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, ""},
		{"too large not JSON", plain, jsonRequest(strings.Repeat("x", 300)), http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, ""},
		{"too large malformed JSON", plain, jsonRequest(`{` + strings.Repeat(" ", 300)), http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, ""},
		{"too large query string", plain, httptest.NewRequest("GET", "/graphql?query="+strings.Repeat("a", 300), nil), http.StatusRequestEntityTooLarge, errcode.BodyTooLarge, ""},
		{"missing query", plain, jsonRequest(`{}`), http.StatusBadRequest, errcode.InvalidRequest, "missing query"},
		{"media type", plain, httptest.NewRequest("POST", "/graphql", strings.NewReader(`{}`)), http.StatusUnsupportedMediaType, errcode.UnsupportedMediaType, ""},
		{"malformed variables", plain, httptest.NewRequest("GET", "/graphql?query=%7B__typename%7D&variables=%7B", nil), http.StatusBadRequest, errcode.InvalidRequest, ""},
		{"mutation over GET", plain, httptest.NewRequest("GET", "/graphql?query=mutation%7B__typename%7D", nil), http.StatusMethodNotAllowed, errcode.MethodNotAllowed, ""},
		{"unrouted method", plain, httptest.NewRequest("PUT", "/graphql", nil), http.StatusMethodNotAllowed, errcode.MethodNotAllowed, ""},
		{"unknown path", plain, httptest.NewRequest("GET", "/nope", nil), http.StatusNotFound, errcode.NotFound, ""},
		{"unknown path without graphiql", apq, httptest.NewRequest("GET", "/nope", nil), http.StatusNotFound, errcode.NotFound, ""},
		{"apq off hash only", plain, jsonRequest(hashOnly), http.StatusOK, errcode.PersistedQueryNotSupported, "PersistedQueryNotSupported"},
		{"apq miss", apq, jsonRequest(hashOnly), http.StatusOK, errcode.PersistedQueryNotFound, "PersistedQueryNotFound"},
		{"apq bad version", apq, jsonRequest(`{"extensions":{"persistedQuery":{"version":7,"sha256Hash":"x"}}}`), http.StatusBadRequest, errcode.InvalidRequest, ""},
		{"apq mismatch", apq, jsonRequest(`{"query":"{__typename}","extensions":{"persistedQuery":{"version":1,"sha256Hash":"ab"}}}`), http.StatusBadRequest, errcode.InvalidRequest, ""},
		{"apq required", only, jsonRequest(`{"query":"{__typename}"}`), http.StatusBadRequest, errcode.PersistedQueryRequired, ""},
		{"registration off", static, jsonRequest(`{"query":"{__typename}","extensions":` + apqExt(apqQuery) + `}`), http.StatusBadRequest, errcode.PersistedQueryNotSupported, ""},
		{"graphql error", plain, jsonRequest(`{"query":"{nope}"}`), http.StatusOK, errcode.GraphQLValidationFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.h.ServeHTTP(rec, tc.req)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
			}
			code, msg := errorBody(t, rec)
			if code != tc.code || !strings.Contains(msg, tc.msg) {
				t.Errorf("code %q message %q, want %q %q", code, msg, tc.code, tc.msg)
			}
			if tc.status == http.StatusMethodNotAllowed && rec.Header().Get("Allow") == "" {
				t.Error("405 without Allow")
			}
		})
	}
}

func jsonRequest(body string) *http.Request {
	req := httptest.NewRequest("POST", "/graphql", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestTimeoutAnswers504JSON(t *testing.T) {
	release := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		w.Header().Set("X-Late", "1")
		_, _ = w.Write([]byte("late"))
		close(release)
	})
	rec := httptest.NewRecorder()
	withTimeout(slow, 10*time.Millisecond).ServeHTTP(rec, httptest.NewRequest("POST", "/graphql", nil))
	<-release
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status %d", rec.Code)
	}
	if got := rec.Body.String(); got != `{"errors":[{"message":"request timeout","extensions":{"code":"timeout"}}]}`+"\n" {
		t.Errorf("body %q", got)
	}
	if rec.Header().Get("X-Late") != "" {
		t.Error("header written after the timeout reached the client")
	}
}

func TestTimeoutPassesCompletedResponse(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("ok"))
	})
	rec := httptest.NewRecorder()
	withTimeout(h, time.Second).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusTeapot || rec.Body.String() != "ok" || rec.Header().Get("Content-Type") != "text/plain" {
		t.Errorf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
}

func TestTimeoutRepanics(t *testing.T) {
	defer func() {
		if recover() != "boom" {
			t.Error("panic not propagated")
		}
	}()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })
	withTimeout(h, time.Second).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
}

func TestAuthErrorCodes(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   errcode.Code
	}{
		{fmt.Errorf("x: %w", auth.ErrAuthRequired), http.StatusUnauthorized, errcode.AuthRequired},
		{fmt.Errorf("x: %w", auth.ErrInvalidToken), http.StatusUnauthorized, errcode.InvalidToken},
		{fmt.Errorf("x: %w", auth.ErrRoleNotAllowed), http.StatusForbidden, errcode.RoleNotAllowed},
	} {
		status, code := authErrorCode(tc.err)
		if status != tc.status || code != tc.code {
			t.Errorf("%v: %d %q, want %d %q", tc.err, status, code, tc.status, tc.code)
		}
	}
}

func TestWSErrorCodes(t *testing.T) {
	f := wsServer(t, func(c *config.Config) { c.Subscriptions.MaxPerConnection = 1 })
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	for _, tc := range []struct {
		payload string
		code    errcode.Code
	}{
		{`{}`, errcode.InvalidRequest},
		{`{"extensions":` + apqExt(apqQuery) + `}`, errcode.PersistedQueryNotSupported},
		{`{"query":"subscription { listen(topic: \"bad topic\") { relatedNodeId } }"}`, errcode.InvalidArgument},
		{`{"query":"{ nope }"}`, errcode.GraphQLValidationFailed},
	} {
		c.send(`{"id":"x","type":"subscribe","payload":` + tc.payload + `}`)
		e := c.expect("error")
		var errs []struct {
			Extensions map[string]any `json:"extensions"`
		}
		if err := json.Unmarshal(e.Payload, &errs); err != nil || len(errs) != 1 || errs[0].Extensions["code"] != string(tc.code) {
			t.Errorf("%s: %s, want %s", tc.payload, e.Payload, tc.code)
		}
	}
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("next")
	c.send(`{"id":"b","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\") { relatedNodeId } }"}}`)
	if e := c.expect("error"); !strings.Contains(string(e.Payload), `"code":"subscription_limit_exceeded"`) {
		t.Errorf("second op: %s", e.Payload)
	}

	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, jsonRequest(`{"query":"subscription { listen(topic: \"a\") { relatedNodeId } }"}`))
	if code, _ := errorBody(t, rec); code != errcode.OperationNotSupported {
		t.Errorf("subscription over HTTP: %q", code)
	}
}
