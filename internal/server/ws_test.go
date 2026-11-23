package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbcore/testutil"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/config"
	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/inflect"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

// memConn is an in-memory LISTEN connection for the hub.
type memConn struct {
	mu       sync.Mutex
	execs    []string
	notified chan *pgconn.Notification
	// delay is how long each statement takes, as a round trip to the
	// database would.
	delay time.Duration
}

func (m *memConn) Exec(ctx context.Context, sql string) error {
	m.mu.Lock()
	d := m.delay
	m.execs = append(m.execs, sql)
	m.mu.Unlock()
	if d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (m *memConn) setDelay(d time.Duration) {
	m.mu.Lock()
	m.delay = d
	m.mu.Unlock()
}

func (m *memConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case n := <-m.notified:
		return n, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *memConn) Close(context.Context) error { return nil }

func (m *memConn) statements() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.execs)
}

type wsFixture struct {
	url  string
	conn *memConn
	hub  *subscribe.Hub
	srv  *Server
}

func wsServer(t *testing.T, mutate func(*config.Config), execOpts ...func(*exec.Options)) *wsFixture {
	t.Helper()
	b := schema.New(testutil.FixtureCatalog(), inflect.Default, schema.Options{FilterIndexedOnly: true, Subscriptions: true})
	built, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	opts := exec.Options{MaxDepth: 15, MaxCost: 10000}
	for _, o := range execOpts {
		o(&opts)
	}
	ex := exec.New(nil, built, compile.New(built).Compile, nil, opts)
	cfg := config.DefaultConfig()
	cfg.Subscriptions.Enabled = true
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg, testAuth(), ex, built.SDL, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	mc := &memConn{notified: make(chan *pgconn.Notification, 16)}
	hub := subscribe.NewHub(func(context.Context) (subscribe.Conn, error) { return mc, nil }, nil)
	srv.SetSubscriptions(hub)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.CloseWebSockets()
		hs.Close()
		hub.Close()
	})
	return &wsFixture{url: "ws" + strings.TrimPrefix(hs.URL, "http") + "/graphql", conn: mc, hub: hub, srv: srv}
}

type wsClient struct {
	t *testing.T
	c *websocket.Conn
}

func dialWS(t *testing.T, url, proto string) *wsClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{Subprotocols: []string{proto}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.CloseNow() })
	return &wsClient{t: t, c: c}
}

func (w *wsClient) send(msg string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
		w.t.Fatal(err)
	}
}

type wsReply struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func (w *wsClient) read() (wsReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := w.c.Read(ctx)
	if err != nil {
		return wsReply{}, err
	}
	var m wsReply
	if err := json.Unmarshal(data, &m); err != nil {
		w.t.Fatalf("bad message %s: %v", data, err)
	}
	return m, nil
}

// expect reads messages, skipping keepalives, until one arrives.
func (w *wsClient) expect(typ string) wsReply {
	w.t.Helper()
	for {
		m, err := w.read()
		if err != nil {
			w.t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m.Type == "ka" && typ != "ka" {
			continue
		}
		if m.Type != typ {
			w.t.Fatalf("got %s %s, want %s", m.Type, m.Payload, typ)
		}
		return m
	}
}

// expectClose reads until the server closes and returns the close code.
func (w *wsClient) expectClose() websocket.StatusCode {
	w.t.Helper()
	for {
		m, err := w.read()
		if err != nil {
			return websocket.CloseStatus(err)
		}
		if m.Type != "ka" {
			w.t.Fatalf("got %s %s, want close", m.Type, m.Payload)
		}
	}
}

func (w *wsClient) init(payload string) {
	w.t.Helper()
	if payload == "" {
		w.send(`{"type":"connection_init"}`)
	} else {
		w.send(`{"type":"connection_init","payload":` + payload + `}`)
	}
	w.expect("connection_ack")
}

func token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWSHandshakeAndPing(t *testing.T) {
	f := wsServer(t, nil)
	c := dialWS(t, f.url, subprotocolTransportWS)
	if got := c.c.Subprotocol(); got != subprotocolTransportWS {
		t.Fatalf("subprotocol %q", got)
	}
	c.init(`{}`)
	c.send(`{"type":"ping"}`)
	c.expect("pong")
	c.send(`{"type":"pong"}`)
	c.send(`{"type":"ping","payload":{"x":1}}`)
	c.expect("pong")
}

func TestWSQueryOverSocket(t *testing.T) {
	f := wsServer(t, nil)
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	c.send(`{"id":"1","type":"subscribe","payload":{"query":"{ __typename }"}}`)
	next := c.expect("next")
	if next.ID != "1" || string(next.Payload) != `{"data":{"__typename":"Query"}}` {
		t.Errorf("next = %s %s", next.ID, next.Payload)
	}
	if done := c.expect("complete"); done.ID != "1" {
		t.Errorf("complete id %q", done.ID)
	}
	c.send(`{"id":"2","type":"subscribe","payload":{"query":"{ nope }"}}`)
	e := c.expect("error")
	if e.ID != "2" || !strings.Contains(string(e.Payload), "nope") {
		t.Errorf("error = %s %s", e.ID, e.Payload)
	}
}

func TestWSProtocolErrors(t *testing.T) {
	f := wsServer(t, func(c *config.Config) { c.Subscriptions.Keepalive = 0 })
	cases := []struct {
		name string
		run  func(c *wsClient)
		code websocket.StatusCode
	}{
		{"subscribe before init", func(c *wsClient) {
			c.send(`{"id":"1","type":"subscribe","payload":{"query":"{ __typename }"}}`)
		}, closeUnauthorized},
		{"second init", func(c *wsClient) {
			c.init("")
			c.send(`{"type":"connection_init"}`)
		}, closeTooManyInits},
		{"unknown type", func(c *wsClient) {
			c.send(`{"type":"bogus"}`)
		}, closeBadRequest},
		{"not json", func(c *wsClient) {
			c.send(`hello`)
		}, closeBadRequest},
		{"missing id", func(c *wsClient) {
			c.init("")
			c.send(`{"type":"subscribe","payload":{"query":"{ __typename }"}}`)
		}, closeBadRequest},
		{"bad token", func(c *wsClient) {
			c.send(`{"type":"connection_init","payload":{"Authorization":"Bearer nope"}}`)
		}, closeForbidden},
		{"duplicate id", func(c *wsClient) {
			c.init("")
			c.send(`{"id":"1","type":"subscribe","payload":{"query":"subscription { listen(topic: \"a\") { relatedNodeId } }"}}`)
			c.send(`{"id":"1","type":"subscribe","payload":{"query":"subscription { listen(topic: \"a\") { relatedNodeId } }"}}`)
		}, closeSubscriberExists},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := dialWS(t, f.url, subprotocolTransportWS)
			tc.run(c)
			if got := c.expectClose(); got != tc.code {
				t.Errorf("close code %d, want %d", got, tc.code)
			}
		})
	}
}

func TestWSInitTimeout(t *testing.T) {
	f := wsServer(t, nil)
	f.srv.wsInitTimeout = 50 * time.Millisecond
	c := dialWS(t, f.url, subprotocolTransportWS)
	if got := c.expectClose(); got != closeInitTimeout {
		t.Errorf("close code %d, want %d", got, closeInitTimeout)
	}
}

func TestWSRejectsUnknownSubprotocol(t *testing.T) {
	f := wsServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, f.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.CloseNow() }()
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != closeSubprotocol {
		t.Errorf("close code %d (%v), want %d", got, err, closeSubprotocol)
	}
}

func TestWSOffByDefault(t *testing.T) {
	s := testServer(config.Config{Limits: config.Limits{MaxBodyBytes: 1 << 20}})
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http")+"/graphql",
		&websocket.DialOptions{Subprotocols: []string{subprotocolTransportWS}})
	if err == nil {
		t.Fatal("upgrade accepted with subscriptions off")
	}
	if resp != nil && resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestInitAuthorization(t *testing.T) {
	for payload, want := range map[string]string{
		``:                             "",
		`{"authorization":"Bearer a"}`: "Bearer a",
		`{"Authorization":"Bearer b"}`: "Bearer b",
		`{"headers":{"authorization":"Bearer c"}}`: "Bearer c",
		`{"headers":{"X-Other":"x"}}`:              "",
		`not json`:                                 "",
	} {
		if got := initAuthorization(json.RawMessage(payload)); got != want {
			t.Errorf("initAuthorization(%s) = %q, want %q", payload, got, want)
		}
	}
}

func TestWSListen(t *testing.T) {
	f := wsServer(t, func(c *config.Config) { c.Subscriptions.Topics = []string{"chat:*"} })
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")

	c.send(`{"id":"s1","type":"subscribe","payload":{"query":"subscription($t: String!) { ev: listen(topic: $t, initialEvent: true) { relatedNodeId ... on ListenPayload { __typename q: query { __typename } } } }","variables":{"t":"chat:1"}}}`)
	initial := c.expect("next")
	if want := `{"data":{"ev":{"relatedNodeId":null,"__typename":"ListenPayload","q":{"__typename":"Query"}}}}`; string(initial.Payload) != want {
		t.Errorf("initial event = %s, want %s", initial.Payload, want)
	}
	if got := f.conn.statements(); !slices.Equal(got, []string{`LISTEN "postgraphile:chat:1"`}) {
		t.Fatalf("statements %q", got)
	}

	f.conn.notified <- &pgconn.Notification{Channel: "postgraphile:chat:1", Payload: `{"__node__":["Post",2]}`}
	ev := c.expect("next")
	id := base64.StdEncoding.EncodeToString([]byte(`["Post", 2]`))
	if want := `{"data":{"ev":{"relatedNodeId":"` + id + `","__typename":"ListenPayload","q":{"__typename":"Query"}}}}`; string(ev.Payload) != want {
		t.Errorf("event = %s, want %s", ev.Payload, want)
	}
	f.conn.notified <- &pgconn.Notification{Channel: "postgraphile:chat:1", Payload: `not json`}
	if ev := c.expect("next"); !strings.Contains(string(ev.Payload), `"relatedNodeId":null`) {
		t.Errorf("non-JSON payload event = %s", ev.Payload)
	}

	c.send(`{"id":"s2","type":"subscribe","payload":{"query":"subscription { listen(topic: \"other\") { relatedNodeId } }"}}`)
	if e := c.expect("error"); e.ID != "s2" || !strings.Contains(string(e.Payload), "not allowed") {
		t.Errorf("disallowed topic: %s %s", e.ID, e.Payload)
	}

	c.send(`{"id":"s1","type":"complete"}`)
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(f.conn.statements(), `UNLISTEN "postgraphile:chat:1"`) {
		if time.Now().After(deadline) {
			t.Fatalf("no UNLISTEN after complete: %q", f.conn.statements())
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.send(`{"type":"ping"}`)
	c.expect("pong")
}

func TestWSSubscriptionLimits(t *testing.T) {
	f := wsServer(t, nil, func(o *exec.Options) {
		o.MaxDepth = 4
		o.DisableIntrospection = true
	})
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	for id, tc := range map[string]struct{ query, want string }{
		"depth":         {`subscription { listen(topic: \"a\") { query { allPosts { nodes { userByAuthorId { id } } } } } }`, "exceeds limit 4"},
		"introspection": {`subscription { listen(topic: \"a\") { query { __schema { queryType { name } } } } }`, "introspection is disabled"},
		"within limits": {`subscription { listen(topic: \"a\", initialEvent: true) { query { __typename } } }`, ""},
	} {
		c.send(`{"id":"` + id + `","type":"subscribe","payload":{"query":"` + tc.query + `"}}`)
		if tc.want == "" {
			if m := c.expect("next"); m.ID != id {
				t.Errorf("%s: next for %q", id, m.ID)
			}
			continue
		}
		if e := c.expect("error"); e.ID != id || !strings.Contains(string(e.Payload), tc.want) {
			t.Errorf("%s: %s %s, want %q", id, e.ID, e.Payload, tc.want)
		}
	}
}

func TestWSSubscriptionOverHTTPRejected(t *testing.T) {
	f := wsServer(t, nil)
	body := `{"query":"subscription { listen(topic: \"a\") { relatedNodeId } }"}`
	resp, err := http.Post(strings.Replace(f.url, "ws", "http", 1), "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Errors []struct{ Message string } `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0].Message, "WebSocket") {
		t.Errorf("errors = %+v", out.Errors)
	}
}

func TestWSMaxPerConnection(t *testing.T) {
	f := wsServer(t, func(c *config.Config) { c.Subscriptions.MaxPerConnection = 1 })
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("next")
	c.send(`{"id":"b","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\") { relatedNodeId } }"}}`)
	if e := c.expect("error"); e.ID != "b" || !strings.Contains(string(e.Payload), "too many operations") {
		t.Errorf("second op: %s %s", e.ID, e.Payload)
	}
}

func TestWSTokenExpiry(t *testing.T) {
	f := wsServer(t, nil)
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init(`{"headers":{"Authorization":"Bearer ` + token(t, jwt.MapClaims{"exp": time.Now().Add(1100 * time.Millisecond).Unix()}) + `"}}`)
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("next")
	time.Sleep(2100 * time.Millisecond)
	f.conn.notified <- &pgconn.Notification{Channel: "postgraphile:x", Payload: `{}`}
	if got := c.expectClose(); got != closeForbidden {
		t.Errorf("close code %d, want %d", got, closeForbidden)
	}
}

func TestWSLegacyProtocol(t *testing.T) {
	f := wsServer(t, func(c *config.Config) { c.Subscriptions.Keepalive = time.Hour })
	c := dialWS(t, f.url, subprotocolLegacyWS)
	c.send(`{"id":"1","type":"start","payload":{"query":"{ __typename }"}}`)
	if e := c.expect("error"); e.ID != "1" {
		t.Errorf("start before init: %s", e.Payload)
	}
	c.send(`{"type":"connection_init","payload":{}}`)
	c.expect("connection_ack")
	c.expect("ka")
	c.send(`{"id":"1","type":"start","payload":{"query":"{ __typename }"}}`)
	if d := c.expect("data"); string(d.Payload) != `{"data":{"__typename":"Query"}}` {
		t.Errorf("data = %s", d.Payload)
	}
	c.expect("complete")
	c.send(`{"id":"2","type":"start","payload":{"query":"{ nope }"}}`)
	if d := c.expect("data"); !strings.Contains(string(d.Payload), `"errors"`) {
		t.Errorf("validation error data = %s", d.Payload)
	}
	c.expect("complete")
	c.send(`{"id":"3","type":"start","payload":{"query":"subscription { listen(topic: \"t\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("data")
	c.send(`{"id":"3","type":"stop"}`)
	c.send(`{"type":"connection_terminate"}`)
	if got := c.expectClose(); got != websocket.StatusNormalClosure {
		t.Errorf("close code %d", got)
	}
}

func TestWSCloseWebSockets(t *testing.T) {
	f := wsServer(t, nil)
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("next")
	closed := make(chan struct{})
	go func() {
		f.srv.CloseWebSockets()
		close(closed)
	}()
	if got := c.expectClose(); got != websocket.StatusGoingAway {
		t.Errorf("close code %d", got)
	}
	<-closed
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c2, _, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{Subprotocols: []string{subprotocolTransportWS}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c2.CloseNow() }()
	_, _, err = c2.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
		t.Errorf("new connection after CloseWebSockets: %d (%v)", got, err)
	}
}

func TestWSConcurrentClients(t *testing.T) {
	f := wsServer(t, nil)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			c := dialWS(t, f.url, subprotocolTransportWS)
			c.init("")
			for j := range 5 {
				id := string(rune('a'+i)) + string(rune('0'+j))
				c.send(`{"id":"` + id + `","type":"subscribe","payload":{"query":"subscription { listen(topic: \"shared\", initialEvent: true) { relatedNodeId } }"}}`)
				c.expect("next")
				c.send(`{"id":"` + id + `","type":"complete"}`)
			}
			c.send(`{"type":"ping"}`)
			c.expect("pong")
			if err := c.c.Close(websocket.StatusNormalClosure, ""); err != nil && !errors.Is(err, context.Canceled) {
				t.Log(err)
			}
		})
	}
	wg.Wait()
}

func TestRelatedNodeID(t *testing.T) {
	enc := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	p := func(s string) *string { return &s }
	for _, tc := range []struct {
		payload *string
		want    string
	}{
		{nil, ""},
		{p(""), ""},
		{p(`{}`), ""},
		{p(`{"__node__":null}`), ""},
		{p(`[1]`), ""},
		{p(`{"__node__":["User",1]}`), enc(`["User", 1]`)},
		{p(`{"__node__": [ "Pair" , 1 , "a b" ]}`), enc(`["Pair", 1, "a b"]`)},
		{p(`{"__node__":"x"}`), enc(`"x"`)},
	} {
		if got := relatedNodeID(tc.payload); got != tc.want {
			t.Errorf("relatedNodeID(%v) = %q, want %q", tc.payload, got, tc.want)
		}
	}
}
