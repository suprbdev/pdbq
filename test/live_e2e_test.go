package e2e

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
)

// liveServer serves through App.Handler with live queries on, so the
// change triggers are installed by the boot pipeline.
func liveServer(t *testing.T, mutate func(*config.Server)) (string, *pgx.Conn) {
	t.Helper()
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.RLS.Enabled = true
	cfg.RLS.Auth.Mode = "jwt"
	cfg.RLS.Auth.JWTSecret = subsSecret
	cfg.RLS.AllowedRoles = []string{"anonymous", "app_user"}
	cfg.Errors.Detail = "dev"
	cfg.Plugins.Disabled = []string{"simple-names"}
	cfg.Server.Subscriptions = true
	cfg.Server.SubscriptionsKeepalive = 0
	cfg.Server.Live = true
	cfg.Server.LiveThrottle = 20 * time.Millisecond
	if mutate != nil {
		mutate(&cfg.Server)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	app := pdbq.New(cfg)
	t.Cleanup(app.Close)
	ctx := context.Background()
	h, err := app.Handler(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(h)
	t.Cleanup(hs.Close)
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return "ws" + strings.TrimPrefix(hs.URL, "http") + "/graphql", db
}

// liveClient is a graphql-transport-ws client whose messages are read in
// the background: cancelling a coder/websocket read closes the connection,
// so waiting for "no message" needs a reader that keeps running.
type liveClient struct {
	*subsClient
	msgs chan subsMsg
}

func liveDial(t *testing.T, url string, claims jwt.MapClaims) *liveClient {
	t.Helper()
	return liveWrap(t, subsDial(t, url), claims)
}

func liveWrap(t *testing.T, c *subsClient, claims jwt.MapClaims) *liveClient {
	t.Helper()
	init := map[string]any{"type": "connection_init"}
	if claims != nil {
		init["payload"] = map[string]any{"authorization": "Bearer " + subsToken(t, claims)}
	}
	c.send(init)
	c.expect("connection_ack")
	lc := &liveClient{subsClient: c, msgs: make(chan subsMsg, 64)}
	go func() {
		defer close(lc.msgs)
		for {
			_, data, err := c.c.Read(context.Background())
			if err != nil {
				return
			}
			var m subsMsg
			if json.Unmarshal(data, &m) != nil {
				return
			}
			lc.msgs <- m
		}
	}()
	return lc
}

// next returns the next message, failing after 10 seconds.
func (c *liveClient) next() subsMsg {
	c.t.Helper()
	m, ok := c.within(10 * time.Second)
	if !ok {
		c.t.Fatal("no message within 10s")
	}
	return m
}

// expect returns the next message, which must have type typ.
func (c *liveClient) expect(typ string) subsMsg {
	c.t.Helper()
	m := c.next()
	if m.Type != typ {
		c.t.Fatalf("got %s %s, want %s", m.Type, m.Payload, typ)
	}
	return m
}

// within returns the next message, or false when none arrives within d.
func (c *liveClient) within(d time.Duration) (subsMsg, bool) {
	c.t.Helper()
	select {
	case m, ok := <-c.msgs:
		if !ok {
			c.t.Fatal("connection closed")
		}
		return m, true
	case <-time.After(d):
		return subsMsg{}, false
	}
}

// titles decodes a live allPosts result into its titles.
func titles(t *testing.T, m subsMsg) []string {
	t.Helper()
	var res struct {
		Data struct {
			AllPosts struct {
				Nodes []struct {
					Title string `json:"title"`
				} `json:"nodes"`
			} `json:"allPosts"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(m.Payload, &res); err != nil {
		t.Fatalf("bad result %s: %v", m.Payload, err)
	}
	if len(res.Errors) > 0 {
		t.Fatalf("result errors: %s", m.Payload)
	}
	out := []string{}
	for _, n := range res.Data.AllPosts.Nodes {
		out = append(out, n.Title)
	}
	return out
}

func livePostsDoc(prefix string) string {
	return `subscription { allPosts(filter: {title: {startsWith: "` + prefix + `"}}, orderBy: TITLE_ASC) { nodes { title } } }`
}

func cleanPosts(t *testing.T, db *pgx.Conn, prefix string) {
	t.Helper()
	clean := func() {
		if _, err := db.Exec(context.Background(), `DELETE FROM posts WHERE title LIKE $1 || '%'`, prefix); err != nil {
			t.Fatal(err)
		}
	}
	clean()
	t.Cleanup(clean)
}

func mustExec(t *testing.T, db *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func expectTitles(t *testing.T, c *liveClient, want ...string) {
	t.Helper()
	m := c.expect("next")
	if got := titles(t, m); !slices.Equal(got, want) {
		t.Fatalf("titles = %q, want %q", got, want)
	}
}

func TestLiveInsertUpdateDelete(t *testing.T) {
	url, db := liveServer(t, nil)
	cleanPosts(t, db, "live1-")
	c := liveDial(t, url, nil)
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": livePostsDoc("live1-")}})
	expectTitles(t, c)

	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, 'live1-a', true)`)
	expectTitles(t, c, "live1-a")
	mustExec(t, db, `UPDATE posts SET title = 'live1-b' WHERE title = 'live1-a'`)
	expectTitles(t, c, "live1-b")

	mustExec(t, db, `UPDATE posts SET body = 'not selected' WHERE title = 'live1-b'`)
	if m, ok := c.within(300 * time.Millisecond); ok {
		t.Fatalf("unchanged result was sent again: %s", m.Payload)
	}

	mustExec(t, db, `DELETE FROM posts WHERE title = 'live1-b'`)
	expectTitles(t, c)

	c.send(map[string]any{"id": "1", "type": "complete"})
	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, 'live1-c', true)`)
	if m, ok := c.within(300 * time.Millisecond); ok {
		t.Fatalf("message after complete: %s", m.Payload)
	}
}

func TestLiveRLS(t *testing.T) {
	url, db := liveServer(t, nil)
	cleanPosts(t, db, "liverls-")
	ada := liveDial(t, url, jwt.MapClaims{"role": "app_user", "user_id": 1})
	grace := liveDial(t, url, jwt.MapClaims{"role": "app_user", "user_id": 2})
	anon := liveDial(t, url, nil)
	for _, c := range []*liveClient{ada, grace, anon} {
		c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": livePostsDoc("liverls-")}})
		expectTitles(t, c)
	}

	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, 'liverls-ada-draft', false)`)
	expectTitles(t, ada, "liverls-ada-draft")
	for _, c := range []*liveClient{grace, anon} {
		if m, ok := c.within(300 * time.Millisecond); ok {
			t.Fatalf("a subscriber who cannot see the draft got a result: %s", m.Payload)
		}
	}

	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (2, 'liverls-grace-draft', false)`)
	expectTitles(t, grace, "liverls-grace-draft")
	mustExec(t, db, `UPDATE posts SET published = true WHERE title = 'liverls-ada-draft'`)
	expectTitles(t, anon, "liverls-ada-draft")
	expectTitles(t, grace, "liverls-ada-draft", "liverls-grace-draft")
	if m, ok := ada.within(300 * time.Millisecond); ok {
		t.Fatalf("ada's result did not change but was sent: %s", m.Payload)
	}
}

func TestLiveThrottle(t *testing.T) {
	url, db := liveServer(t, func(s *config.Server) { s.LiveThrottle = time.Second })
	cleanPosts(t, db, "livethr-")
	c := liveDial(t, url, nil)
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": livePostsDoc("livethr-")}})
	start := time.Now()
	expectTitles(t, c)
	for i := range 5 {
		mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, $1, true)`, "livethr-"+string(rune('a'+i)))
	}
	m := c.expect("next")
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Errorf("second result after %v, want >= the 1s throttle", d)
	}
	if got := titles(t, m); len(got) != 5 {
		t.Errorf("throttled result = %q, want the 5 inserts coalesced", got)
	}
	if m, ok := c.within(1500 * time.Millisecond); ok {
		t.Errorf("extra result after the coalesced one: %s", m.Payload)
	}
}

func TestLiveDirectiveLegacyProtocol(t *testing.T) {
	url, db := liveServer(t, nil)
	cleanPosts(t, db, "livelegacy-")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"graphql-ws"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	c := liveWrap(t, &subsClient{t: t, c: conn}, nil)
	c.send(map[string]any{"id": "q", "type": "start", "payload": map[string]any{
		"query": `query @live { allPosts(filter: {title: {startsWith: "livelegacy-"}}) { nodes { title } } }`,
	}})
	expectData := func(want ...string) {
		t.Helper()
		m := c.expect("data")
		if got := titles(t, m); !slices.Equal(got, want) {
			t.Fatalf("titles = %q, want %q", got, want)
		}
	}
	expectData()
	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, 'livelegacy-a', true)`)
	expectData("livelegacy-a")
	c.send(map[string]any{"id": "q", "type": "stop"})
}

func TestLiveView(t *testing.T) {
	url, db := liveServer(t, nil)
	cleanPosts(t, db, "liveview-")
	c := liveDial(t, url, nil)
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": `subscription { allMetrics { nodes { name value } } }`}})
	first := c.expect("next")
	mustExec(t, db, `INSERT INTO posts (author_id, title, published) VALUES (1, 'liveview-a', true)`)
	second := c.expect("next")
	if string(first.Payload) == string(second.Payload) {
		t.Fatalf("view result did not change: %s", second.Payload)
	}
}

func TestLivePartitioned(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	conn := testConn(t)
	execAll(t, conn,
		`DROP TABLE IF EXISTS live_events`,
		`CREATE TABLE live_events (id int NOT NULL, kind text NOT NULL, PRIMARY KEY (id, kind)) PARTITION BY LIST (kind)`,
		`CREATE TABLE live_events_a PARTITION OF live_events FOR VALUES IN ('a')`,
		`GRANT SELECT ON live_events TO anonymous`,
	)
	t.Cleanup(func() { execAll(t, conn, `DROP TABLE IF EXISTS live_events`) })
	wsURL, db := liveServer(t, nil)
	mustExec(t, db, `CREATE TABLE live_events_b PARTITION OF live_events FOR VALUES IN ('b')`)

	c := liveDial(t, wsURL, nil)
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": `subscription { allLiveEvents { totalCount } }`}})
	count := func(m subsMsg) int {
		var res struct {
			Data struct {
				AllLiveEvents struct {
					TotalCount int `json:"totalCount"`
				} `json:"allLiveEvents"`
			} `json:"data"`
		}
		if err := json.Unmarshal(m.Payload, &res); err != nil {
			t.Fatalf("bad result %s", m.Payload)
		}
		return res.Data.AllLiveEvents.TotalCount
	}
	if n := count(c.expect("next")); n != 0 {
		t.Fatalf("initial count %d", n)
	}
	mustExec(t, db, `INSERT INTO live_events_b VALUES (1, 'b')`)
	if n := count(c.expect("next")); n != 1 {
		t.Fatalf("count after inserting into a partition created after startup = %d, want 1", n)
	}
	mustExec(t, db, `TRUNCATE live_events`)
	if n := count(c.expect("next")); n != 0 {
		t.Fatalf("count after TRUNCATE = %d, want 0", n)
	}
}

func TestLiveCaps(t *testing.T) {
	url, db := liveServer(t, func(s *config.Server) {
		s.LiveMaxPerConnection = 1
		s.LiveMaxOperations = 2
	})
	cleanPosts(t, db, "livecap-")
	doc := map[string]any{"query": livePostsDoc("livecap-")}
	a := liveDial(t, url, nil)
	a.send(map[string]any{"id": "1", "type": "subscribe", "payload": doc})
	expectTitles(t, a)
	a.send(map[string]any{"id": "2", "type": "subscribe", "payload": doc})
	if m := a.expect("error"); !strings.Contains(string(m.Payload), "too many live queries on this connection") {
		t.Fatalf("per-connection cap: %s", m.Payload)
	}
	b := liveDial(t, url, nil)
	b.send(map[string]any{"id": "1", "type": "subscribe", "payload": doc})
	expectTitles(t, b)
	c := liveDial(t, url, nil)
	c.send(map[string]any{"id": "1", "type": "subscribe", "payload": doc})
	if m := c.expect("error"); !strings.Contains(string(m.Payload), "too many live queries on this server") {
		t.Fatalf("server cap: %s", m.Payload)
	}

	a.send(map[string]any{"id": "1", "type": "complete"})
	deadline := time.Now().Add(5 * time.Second)
	for i := 0; ; i++ {
		c.send(map[string]any{"id": "retry" + strconv.Itoa(i), "type": "subscribe", "payload": doc})
		m := c.next()
		if m.Type == "next" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("live query slot not released after complete: %s %s", m.Type, m.Payload)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
