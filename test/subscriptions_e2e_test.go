package e2e

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"

	"github.com/suprbdev/pdbq"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/server"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

const subsSecret = "subscriptions-e2e-secret"

type subsFixture struct {
	url string
	hub *subscribe.Hub
	db  *pgx.Conn
}

func subsServer(t *testing.T) *subsFixture {
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
	srv, err := server.New(cfg.Server, server.NewAuthenticator(cfg.RLS, nil), app.Executor(built), built.SDL, nil)
	if err != nil {
		t.Fatal(err)
	}
	hub := subscribe.NewHub(subscribe.PoolDialer(app.Pool()), nil)
	srv.SetSubscriptions(hub)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.CloseWebSockets()
		hs.Close()
		hub.Close()
	})
	db, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(context.Background()) })
	return &subsFixture{url: "ws" + strings.TrimPrefix(hs.URL, "http") + "/graphql", hub: hub, db: db}
}

type subsClient struct {
	t *testing.T
	c *websocket.Conn
}

type subsMsg struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func subsDial(t *testing.T, url string) *subsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{"graphql-transport-ws"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return &subsClient{t: t, c: c}
}

func (s *subsClient) send(v any) {
	s.t.Helper()
	b, _ := json.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.c.Write(ctx, websocket.MessageText, b); err != nil {
		s.t.Fatal(err)
	}
}

func (s *subsClient) read() (subsMsg, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, data, err := s.c.Read(ctx)
	if err != nil {
		return subsMsg{}, err
	}
	var m subsMsg
	if err := json.Unmarshal(data, &m); err != nil {
		s.t.Fatalf("bad message %s", data)
	}
	return m, nil
}

func (s *subsClient) expect(typ string) subsMsg {
	s.t.Helper()
	m, err := s.read()
	if err != nil {
		s.t.Fatalf("waiting for %s: %v", typ, err)
	}
	if m.Type != typ {
		s.t.Fatalf("got %s %s, want %s", m.Type, m.Payload, typ)
	}
	return m
}

func subsToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(subsSecret))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type listenEvent struct {
	Data struct {
		Listen struct {
			RelatedNodeID *string `json:"relatedNodeId"`
			RelatedNode   *struct {
				NodeID string `json:"nodeId"`
				ID     int    `json:"id"`
				Title  string `json:"title"`
			} `json:"relatedNode"`
			Query struct {
				PostByID *struct {
					Title string `json:"title"`
				} `json:"postById"`
			} `json:"query"`
		} `json:"listen"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func decodeEvent(t *testing.T, m subsMsg) listenEvent {
	t.Helper()
	var ev listenEvent
	if err := json.Unmarshal(m.Payload, &ev); err != nil {
		t.Fatalf("bad event %s: %v", m.Payload, err)
	}
	if len(ev.Errors) > 0 {
		t.Fatalf("event errors: %s", m.Payload)
	}
	return ev
}

const listenDoc = `subscription {
	listen(topic: "posts", initialEvent: true) {
		relatedNodeId
		relatedNode { nodeId ... on Post { id title } }
		query { postById(id: 2) { title } }
	}
}`

func TestSubscriptionsListen(t *testing.T) {
	f := subsServer(t)
	ctx := context.Background()

	author := subsDial(t, f.url)
	author.send(map[string]any{"type": "connection_init", "payload": map[string]any{
		"authorization": "Bearer " + subsToken(t, jwt.MapClaims{"role": "app_user", "user_id": 1}),
	}})
	author.expect("connection_ack")
	anon := subsDial(t, f.url)
	anon.send(map[string]any{"type": "connection_init"})
	anon.expect("connection_ack")

	for _, c := range []*subsClient{author, anon} {
		c.send(map[string]any{"id": "1", "type": "subscribe", "payload": map[string]any{"query": listenDoc}})
		ev := decodeEvent(t, c.expect("next"))
		if ev.Data.Listen.RelatedNodeID != nil || ev.Data.Listen.RelatedNode != nil {
			t.Errorf("initial event names a node: %+v", ev.Data.Listen)
		}
	}
	queryOverSocket(t, author)

	if _, err := f.db.Exec(ctx, `SELECT pg_notify('postgraphile:posts', '{"__node__": ["Post", 2]}')`); err != nil {
		t.Fatal(err)
	}
	authorEv := decodeEvent(t, author.expect("next"))
	anonEv := decodeEvent(t, anon.expect("next"))
	if n := authorEv.Data.Listen.RelatedNode; n == nil || n.ID != 2 || n.Title != "Unpublished draft" {
		t.Errorf("author relatedNode = %+v, want the own draft", n)
	} else if id := authorEv.Data.Listen.RelatedNodeID; id == nil || *id != n.NodeID {
		t.Errorf("relatedNodeId %v differs from relatedNode.nodeId %q", id, n.NodeID)
	}
	if q := authorEv.Data.Listen.Query.PostByID; q == nil || q.Title != "Unpublished draft" {
		t.Errorf("author query.postById = %+v", q)
	}
	if anonEv.Data.Listen.RelatedNode != nil {
		t.Errorf("anonymous subscriber sees the draft through relatedNode: %+v", anonEv.Data.Listen.RelatedNode)
	}
	if anonEv.Data.Listen.Query.PostByID != nil {
		t.Errorf("anonymous subscriber sees the draft through query: %+v", anonEv.Data.Listen.Query.PostByID)
	}
	if anonEv.Data.Listen.RelatedNodeID == nil {
		t.Error("anonymous relatedNodeId missing")
	}

	if _, err := f.db.Exec(ctx, `SELECT pg_notify('postgraphile:posts', json_build_object('__node__', json_build_array('Post', 1))::text)`); err != nil {
		t.Fatal(err)
	}
	if n := decodeEvent(t, anon.expect("next")).Data.Listen.RelatedNode; n == nil || n.ID != 1 {
		t.Errorf("anonymous relatedNode for a published post = %+v", n)
	}
	decodeEvent(t, author.expect("next"))

	if !contains(f.hub.Listening(), "postgraphile:posts") {
		t.Fatalf("hub not listening: %q", f.hub.Listening())
	}
	author.send(map[string]any{"id": "1", "type": "complete"})
	if !contains(f.hub.Listening(), "postgraphile:posts") {
		t.Fatal("UNLISTEN while another subscriber remains")
	}
	anon.send(map[string]any{"id": "1", "type": "complete"})
	deadline := time.Now().Add(10 * time.Second)
	for len(f.hub.Listening()) > 0 {
		if time.Now().After(deadline) {
			t.Fatalf("still listening after the last subscriber left: %q", f.hub.Listening())
		}
		time.Sleep(10 * time.Millisecond)
	}
	anon.send(map[string]any{"type": "ping"})
	anon.expect("pong")
}

// queryOverSocket runs a query as a second operation on c, next to its
// running subscription, and checks it sees the author's draft.
func queryOverSocket(t *testing.T, c *subsClient) {
	t.Helper()
	c.send(map[string]any{"id": "2", "type": "subscribe", "payload": map[string]any{"query": `{ postById(id: 2) { title } }`}})
	m := c.expect("next")
	c.expect("complete")
	var res struct {
		Data struct {
			PostByID *struct{ Title string } `json:"postById"`
		} `json:"data"`
	}
	if err := json.Unmarshal(m.Payload, &res); err != nil || res.Data.PostByID == nil {
		t.Fatalf("query over the socket as the author: %s", m.Payload)
	}
}

func TestSubscriptionsAuthRejected(t *testing.T) {
	f := subsServer(t)
	bad, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"role": "app_user", "exp": time.Now().Add(time.Hour).Unix()}).
		SignedString([]byte("wrong-secret"))
	if err != nil {
		t.Fatal(err)
	}
	for name, payload := range map[string]map[string]any{
		"bad signature": {"authorization": "Bearer " + bad},
		"reserved role": {"Authorization": "Bearer " + subsToken(t, jwt.MapClaims{"role": "postgres"})},
		"not bearer":    {"headers": map[string]any{"Authorization": "Basic Zm9vOmJhcg=="}},
	} {
		t.Run(name, func(t *testing.T) {
			c := subsDial(t, f.url)
			c.send(map[string]any{"type": "connection_init", "payload": payload})
			_, err := c.read()
			if got := websocket.CloseStatus(err); got != 4403 {
				t.Errorf("close code %d (%v), want 4403", got, err)
			}
		})
	}
	if got := f.hub.Listening(); len(got) != 0 {
		t.Errorf("rejected connections left LISTENs: %q", got)
	}
}

// TestSubscriptionsReconnect serves through App.Handler (the boot pipeline's
// hub wiring) and kills the LISTEN connection: the hub reconnects and
// LISTENs again, and the subscription keeps receiving events.
func TestSubscriptionsReconnect(t *testing.T) {
	url := os.Getenv("PDBQ_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PDBQ_TEST_DATABASE_URL not set; run `make test-e2e`")
	}
	cfg := config.DefaultConfig()
	cfg.Database.URL = url
	cfg.RLS.Enabled = false
	cfg.Plugins.Disabled = []string{"simple-names"}
	cfg.Server.Subscriptions = true
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

	c := subsDial(t, "ws"+strings.TrimPrefix(hs.URL, "http")+"/graphql")
	c.send(map[string]any{"type": "connection_init"})
	c.expect("connection_ack")
	c.send(map[string]any{"id": "r", "type": "subscribe", "payload": map[string]any{
		"query": `subscription { listen(topic: "reconnect", initialEvent: true) { relatedNode { ... on User { email } } } }`,
	}})
	c.expect("next")

	listener := func() int {
		var pid int
		err := db.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE query = 'LISTEN "postgraphile:reconnect"' ORDER BY backend_start DESC LIMIT 1`).Scan(&pid)
		if err != nil {
			return 0
		}
		return pid
	}
	old := listener()
	if old == 0 {
		t.Fatal("no LISTEN backend found")
	}
	if _, err := db.Exec(ctx, `SELECT pg_terminate_backend($1)`, old); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for pid := listener(); pid == 0 || pid == old; pid = listener() {
		if time.Now().After(deadline) {
			t.Fatal("hub did not LISTEN again after its connection was terminated")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := db.Exec(ctx, `SELECT pg_notify('postgraphile:reconnect', '{"__node__":["User",1]}')`); err != nil {
		t.Fatal(err)
	}
	m := c.expect("next")
	if !strings.Contains(string(m.Payload), `"ada@example.com"`) {
		t.Errorf("event after reconnect = %s", m.Payload)
	}
}
