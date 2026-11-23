package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/server"
)

const incSecret = "incremental-e2e-secret"

type incFixture struct {
	http string
	ws   string
	ex   *exec.Executor
	db   *pgx.Conn
}

// incServer serves the fixture database with incremental delivery on,
// JWT-authenticated RLS, subscriptions (for graphql-ws) and a stream batch
// of two items.
func incServer(t *testing.T, mutate func(*config.Config)) *incFixture {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	jwtIdentity(&cfg, incSecret)
	cfg.Roles.Allowed = []string{"anonymous", "app_user"}
	cfg.Errors.Detail = "dev"
	cfg.Plugins.Disabled = []string{"simple_names"}
	cfg.Subscriptions.Enabled = true
	cfg.Subscriptions.Keepalive = 0
	cfg.Incremental.Enabled = true
	cfg.Incremental.StreamBatchSize = 2
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	ctx := context.Background()
	cat, err := app.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	built, err := app.BuildSchema(ctx, cat)
	if err != nil {
		t.Fatal(err)
	}
	ex := app.Executor(built)
	srv, err := server.New(cfg, mustAuthenticator(t, cfg), ex, built.SDL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.CloseWebSockets()
		hs.Close()
	})
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return &incFixture{http: hs.URL + "/graphql", ws: "ws" + strings.TrimPrefix(hs.URL, "http") + "/graphql", ex: ex, db: db}
}

func incToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(incSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// incPost posts query with the Accept header accept (and a bearer token
// when token is set) and returns the response.
func incPost(t *testing.T, url, query, accept, token string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"query": query})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// readParts reads the parts of a multipart/mixed incremental response,
// checking its framing.
func readParts(t *testing.T, resp *http.Response, spec string) []json.RawMessage {
	t.Helper()
	mt, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/mixed" || params["boundary"] != "-" {
		t.Fatalf("Content-Type %q", resp.Header.Get("Content-Type"))
	}
	if k, v, _ := strings.Cut(spec, "="); params[strings.ToLower(k)] != v {
		t.Fatalf("Content-Type %q does not echo %s", resp.Header.Get("Content-Type"), spec)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(raw, []byte("\r\n---\r\nContent-Type: application/json; charset=utf-8\r\n")) || !bytes.HasSuffix(raw, []byte("\r\n-----\r\n")) {
		t.Fatalf("framing: %q", raw)
	}
	mr := multipart.NewReader(bytes.NewReader(raw), "-")
	var parts []json.RawMessage
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("part %d: %v in %q", len(parts), err, raw)
		}
		if p.Header.Get("Content-Type") != "application/json; charset=utf-8" {
			t.Fatalf("part Content-Type %q", p.Header.Get("Content-Type"))
		}
		b, _ := io.ReadAll(p)
		if !json.Valid(b) {
			t.Fatalf("part %d is not JSON: %s", len(parts), b)
		}
		parts = append(parts, b)
	}
	return parts
}

// assemble merges the payloads of an incrementalSpec=v0.2 response into
// one result, checking that every pending id completes and that only the
// last payload has hasNext false.
func assemble(t *testing.T, payloads []json.RawMessage) map[string]any {
	t.Helper()
	type pending struct {
		ID   string `json:"id"`
		Path []any  `json:"path"`
	}
	var data map[string]any
	paths := map[string][]any{}
	for i, raw := range payloads {
		var p struct {
			Data        map[string]any `json:"data"`
			Errors      []any          `json:"errors"`
			HasNext     *bool          `json:"hasNext"`
			Pending     []pending      `json:"pending"`
			Incremental []struct {
				ID      string         `json:"id"`
				Data    map[string]any `json:"data"`
				Items   []any          `json:"items"`
				SubPath []any          `json:"subPath"`
				Errors  []any          `json:"errors"`
			} `json:"incremental"`
			Completed []struct {
				ID     string `json:"id"`
				Errors []any  `json:"errors"`
			} `json:"completed"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if len(p.Errors) > 0 {
			t.Fatalf("payload %d errors: %s", i, raw)
		}
		if p.HasNext == nil || *p.HasNext != (i < len(payloads)-1) {
			t.Fatalf("payload %d hasNext wrong: %s", i, raw)
		}
		if i == 0 {
			data = p.Data
		}
		for _, pe := range p.Pending {
			paths[pe.ID] = pe.Path
		}
		for _, inc := range p.Incremental {
			if len(inc.Errors) > 0 {
				t.Fatalf("incremental errors: %s", raw)
			}
			path, ok := paths[inc.ID]
			if !ok {
				t.Fatalf("incremental id %s not pending: %s", inc.ID, raw)
			}
			target := at(t, data, append(path, inc.SubPath...))
			if inc.Items != nil {
				list := target.([]any)
				setAt(t, data, path, append(list, inc.Items...))
				continue
			}
			merge(target.(map[string]any), inc.Data)
		}
		for _, c := range p.Completed {
			if len(c.Errors) > 0 {
				t.Fatalf("completed with errors: %s", raw)
			}
			if _, ok := paths[c.ID]; !ok {
				t.Fatalf("completed id %s not pending", c.ID)
			}
			delete(paths, c.ID)
		}
	}
	if len(paths) > 0 {
		t.Fatalf("never completed: %v", paths)
	}
	return data
}

func at(t *testing.T, v any, path []any) any {
	t.Helper()
	for _, el := range path {
		switch k := el.(type) {
		case string:
			v = v.(map[string]any)[k]
		case float64:
			v = v.([]any)[int(k)]
		}
	}
	return v
}

func setAt(t *testing.T, root map[string]any, path []any, val any) {
	t.Helper()
	parent := at(t, root, path[:len(path)-1])
	switch k := path[len(path)-1].(type) {
	case string:
		parent.(map[string]any)[k] = val
	case float64:
		parent.([]any)[int(k)] = val
	}
}

func merge(dst, src map[string]any) {
	for k, v := range src {
		if sub, ok := v.(map[string]any); ok {
			if d, ok := dst[k].(map[string]any); ok {
				merge(d, sub)
				continue
			}
		}
		dst[k] = v
	}
}

func canonical(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// inlineData runs query without incremental delivery (a plain JSON
// request, where the directives are ignored) and returns its data.
func inlineData(t *testing.T, url, query, token string) map[string]any {
	t.Helper()
	resp := incPost(t, url, query, "application/json", token)
	var res struct {
		Data   map[string]any `json:"data"`
		Errors []any          `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil || len(res.Errors) > 0 {
		t.Fatalf("inline query: %v %v", err, res.Errors)
	}
	return res.Data
}

const incQuery = `{
	allUsers(orderBy: [ID_ASC]) {
		totalCount
		nodes @stream(initialCount: 1, label: "users") {
			id email
			... @defer(label: "name") { fullName }
		}
	}
	userById(id: 1) {
		id
		... @defer(label: "posts") {
			postsByAuthorId(orderBy: [ID_ASC]) { totalCount nodes { title } }
		}
	}
	... @defer(label: "root") {
		allPosts(first: 3, orderBy: [ID_ASC]) {
			edges @stream { cursor node { id title } }
			pageInfo { hasNextPage endCursor }
		}
	}
	byAuthor: allPosts(orderBy: [AUTHOR_ID_ASC, ID_ASC]) {
		nodes { id ... @defer { title author { email } } }
	}
}`

func TestIncrementalHTTP(t *testing.T) {
	f := incServer(t, nil)
	want := canonical(t, inlineData(t, f.http, incQuery, ""))

	resp := incPost(t, f.http, incQuery, "multipart/mixed;incrementalSpec=v0.2, application/json", "")
	parts := readParts(t, resp, "incrementalSpec=v0.2")
	if len(parts) < 4 {
		t.Fatalf("%d payloads, want the stream and deferred fragments apart", len(parts))
	}
	var initial struct {
		Pending []struct {
			Path  []any  `json:"path"`
			Label string `json:"label"`
		} `json:"pending"`
	}
	if err := json.Unmarshal(parts[0], &initial); err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, p := range initial.Pending {
		labels[p.Label] = true
	}
	for _, l := range []string{"users", "posts", "root"} {
		if !labels[l] {
			t.Errorf("initial payload does not announce %q: %s", l, parts[0])
		}
	}
	if got := canonical(t, assemble(t, parts)); got != want {
		t.Errorf("assembled:\n%s\nwant (inline):\n%s", got, want)
	}
}

func TestIncrementalLegacyHTTP(t *testing.T) {
	f := incServer(t, nil)
	resp := incPost(t, f.http, `{ allPosts(first: 4, orderBy: [ID_ASC]) { nodes @stream(initialCount: 1, label: "s") { id } } }`, "multipart/mixed;deferSpec=20220824", "")
	parts := readParts(t, resp, "deferSpec=20220824")
	want := []string{
		`{"data":{"allPosts":{"nodes":[{"id":1}]}},"hasNext":true}`,
		`{"hasNext":true,"incremental":[{"items":[{"id":3},{"id":4}],"path":["allPosts","nodes",1],"label":"s"}]}`,
		`{"hasNext":false}`,
	}
	if len(parts) != len(want) {
		t.Fatalf("payloads %s", parts)
	}
	for i := range want {
		if got := normalize(t, parts[i]); got != normalizeStr(t, want[i]) {
			t.Errorf("payload %d = %s, want %s", i, parts[i], want[i])
		}
	}
}

func TestIncrementalWebSocket(t *testing.T) {
	f := incServer(t, nil)
	want := canonical(t, inlineData(t, f.http, incQuery, ""))
	c := subsDial(t, f.ws)
	c.send(map[string]any{"type": "connection_init"})
	c.expect("connection_ack")
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": incQuery}})
	var payloads []json.RawMessage
	for {
		m, err := c.read()
		if err != nil {
			t.Fatal(err)
		}
		if m.Type == "complete" {
			break
		}
		if m.Type != "next" || m.ID != "1" {
			t.Fatalf("got %s %s", m.Type, m.Payload)
		}
		payloads = append(payloads, m.Payload)
	}
	if len(payloads) < 4 {
		t.Fatalf("%d next messages, want the stream and deferred fragments apart", len(payloads))
	}
	if got := canonical(t, assemble(t, payloads)); got != want {
		t.Errorf("assembled:\n%s\nwant (inline):\n%s", got, want)
	}
}

// TestIncrementalRLS checks that deferred statements run under the
// request's role and claims: the author sees their draft in a deferred
// fragment, an anonymous client does not.
func TestIncrementalRLS(t *testing.T) {
	f := incServer(t, nil)
	q := `{ userById(id: 1) { id ... @defer { postsByAuthorId { totalCount } } } }`
	count := func(token string) float64 {
		parts := readParts(t, incPost(t, f.http, q, "multipart/mixed", token), "incrementalSpec=v0.2")
		data := assemble(t, parts)
		return at(t, data, []any{"userById", "postsByAuthorId", "totalCount"}).(float64)
	}
	if got := count(""); got != 1 {
		t.Errorf("anonymous sees %v posts, want 1", got)
	}
	if got := count(incToken(t, jwt.MapClaims{"role": "app_user", "user_id": "1"})); got != 2 {
		t.Errorf("author sees %v posts, want 2", got)
	}
}

// TestIncrementalSnapshot checks that writes committed while a response is
// being delivered do not show in its later payloads: every payload reads
// the snapshot of the first.
func TestIncrementalSnapshot(t *testing.T) {
	f := incServer(t, func(c *config.Config) {
		c.Incremental.StreamBatchSize = 1
		noIdentity(c)
	})
	ctx := context.Background()
	q := `{
		allPosts(orderBy: [ID_ASC]) { nodes @stream(initialCount: 1) { id title } }
		userById(id: 3) { id ... @defer { fullName postsByAuthorId { totalCount } } }
	}`
	want := canonical(t, inlineData(t, f.http, q, ""))
	restore := func() {
		if _, err := f.db.Exec(ctx, `UPDATE posts SET title = 'On Computable Numbers' WHERE id = 4`); err != nil {
			t.Error(err)
		}
		if _, err := f.db.Exec(ctx, `UPDATE users SET full_name = 'Alan Turing' WHERE id = 3`); err != nil {
			t.Error(err)
		}
		if _, err := f.db.Exec(ctx, `UPDATE posts SET author_id = 3 WHERE id = 4`); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(restore)
	var payloads []json.RawMessage
	err := f.ex.ExecuteIncrementalRequest(ctx, exec.Request{Query: q}, exec.IncrementalV02, func(body []byte, incremental bool) error {
		if !incremental {
			t.Fatalf("not delivered incrementally: %s", body)
		}
		if len(payloads) == 0 {
			if _, err := f.db.Exec(ctx, `UPDATE posts SET title = 'changed mid-stream', author_id = 1 WHERE id = 4`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Exec(ctx, `UPDATE users SET full_name = 'changed mid-stream' WHERE id = 3`); err != nil {
				t.Fatal(err)
			}
		}
		payloads = append(payloads, append(json.RawMessage(nil), body...))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := canonical(t, assemble(t, payloads)); got != want {
		t.Errorf("assembled:\n%s\nwant the snapshot before the writes:\n%s", got, want)
	}
	restore()
}

func TestIncrementalSchemaToggle(t *testing.T) {
	on := incServer(t, nil)
	off := incServer(t, func(c *config.Config) { c.Incremental.Enabled = false })
	sdl := func(url string) string {
		b, err := httpGet(strings.TrimSuffix(url, "/graphql") + "/schema.graphql")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	onSDL, offSDL := sdl(on.http), sdl(off.http)
	if !strings.Contains(onSDL, "directive @stream(") || !strings.Contains(onSDL, "directive @defer(") {
		t.Error("schema with incremental delivery does not declare the directives")
	}
	if strings.Contains(offSDL, "directive @") || !strings.HasSuffix(onSDL, offSDL) {
		t.Error("schema without incremental delivery differs beyond the directives")
	}
	resp := incPost(t, off.http, `{ allPosts { nodes @stream { id } } }`, "multipart/mixed", "")
	if b, _ := io.ReadAll(resp.Body); !strings.Contains(string(b), `Unknown directive \"@stream\"`) {
		t.Errorf("stream accepted while off: %s", b)
	}
	resp = incPost(t, off.http, `{ userById(id: 1) { id ... @defer { email } } }`, "multipart/mixed", "")
	if b, _ := io.ReadAll(resp.Body); resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(b), "ada@example.com") {
		t.Errorf("defer while off: %s %s", resp.Header.Get("Content-Type"), b)
	}
}

func TestIncrementalLimits(t *testing.T) {
	f := incServer(t, func(c *config.Config) {
		c.Incremental.MaxDeferred = 1
		c.Limits.MaxCost = 500
	})
	cases := map[string]string{
		`{ userById(id: 1) { ... @defer { id } ... @defer { email } } }`:                                   "more than 1 selections",
		`{ allUsers(first: 50) { nodes { ... @defer { postsByAuthorId(first: 50) { nodes { id } } } } } }`: "operation cost",
	}
	for q, want := range cases {
		b, _ := io.ReadAll(incPost(t, f.http, q, "multipart/mixed", "").Body)
		if !strings.Contains(string(b), want) {
			t.Errorf("%s: %s, want %q", q, b, want)
		}
	}
}

// TestIncrementalMutationInline checks that a mutation's deferred fragments
// are delivered in its single result.
func TestIncrementalMutationInline(t *testing.T) {
	f := incServer(t, func(c *config.Config) { noIdentity(c) })
	resp := incPost(t, f.http, `mutation { publishPost(input: {postId: 1}) { clientMutationId ... @defer { query { postById(id: 1) { published } } } } }`, "multipart/mixed, application/json", "")
	b, _ := io.ReadAll(resp.Body)
	if resp.Header.Get("Content-Type") != "application/json" || !strings.Contains(string(b), `"published"`) || strings.Contains(string(b), "hasNext") {
		t.Errorf("mutation: %s %s", resp.Header.Get("Content-Type"), b)
	}
}
