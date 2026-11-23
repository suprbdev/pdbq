package server

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/suprbdev/pdbq/internal/config"
)

// churn runs the subscribe/complete churn of the OOM report on conns
// connections for d: each keeps at most window ids open, completing the
// oldest right before subscribing to a fresh topic. It returns the highest
// goroutine count seen and the number of subscribe messages sent.
func churn(t *testing.T, url string, conns, window int, d time.Duration) (peak int, sent int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var wg sync.WaitGroup
	var total atomic.Int64
	for w := range conns {
		c := dialWS(t, url, subprotocolTransportWS)
		c.init("")
		wg.Go(func() {
			for {
				if _, _, err := c.c.Read(context.Background()); err != nil {
					return
				}
			}
		})
		wg.Go(func() {
			defer func() { _ = c.c.CloseNow() }()
			for i := 0; ctx.Err() == nil; i++ {
				id := fmt.Sprintf("c%d", i%window)
				if i >= window {
					if c.c.Write(ctx, websocket.MessageText, []byte(`{"id":"`+id+`","type":"complete"}`)) != nil {
						return
					}
				}
				q := fmt.Sprintf(`{"id":"%s","type":"subscribe","payload":{"query":"subscription { listen(topic: \"churn%d_%d\") { relatedNodeId } }"}}`, id, w, i)
				if c.c.Write(ctx, websocket.MessageText, []byte(q)) != nil {
					return
				}
				total.Add(1)
			}
		})
	}
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for ctx.Err() == nil {
		peak = max(peak, runtime.NumGoroutine())
		<-tick.C
	}
	wg.Wait()
	return peak, total.Load()
}

// connGoroutines bounds the goroutines of one test connection besides its
// operations: the server's read loop, init watcher and keepalive, and the
// client's reader and writer, plus the net/http and websocket internals.
const connGoroutines = 12

// settle waits until the goroutine count drops to at most n.
func settle(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.NumGoroutine() > n {
		if time.Now().After(deadline) {
			t.Fatalf("%d goroutines still running, want at most %d", runtime.NumGoroutine(), n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWSChurnKeepsGoroutinesBounded(t *testing.T) {
	for _, rate := range []int{0, 100} {
		t.Run(fmt.Sprintf("subscribe_rate=%d", rate), func(t *testing.T) {
			const conns, perConn = 8, 20
			f := wsServer(t, func(c *config.Server) {
				c.SubscriptionsMaxPerConnection = perConn
				c.SubscriptionsSubscribeRate = rate
				// A throttled connection whose client has left still has its
				// unread messages queued; the keepalive ping, whose pong waits
				// behind them, is what ends it.
				c.SubscriptionsKeepalive = time.Second
			})
			if rate == 0 {
				f.conn.setDelay(time.Millisecond)
			}
			base := runtime.NumGoroutine()
			peak, sent := churn(t, f.url, conns, perConn, 2*time.Second)
			bound := base + conns*(perConn+connGoroutines)
			t.Logf("sent %d subscribes; goroutines: base %d, peak %d, bound %d, hub pending %d", sent, base, peak, bound, f.hub.Pending())
			if peak > bound {
				t.Fatalf("goroutines peaked at %d, bound %d", peak, bound)
			}
			if sent < conns*perConn {
				t.Fatalf("only %d subscribes went through", sent)
			}
			listens := 0
			for _, st := range f.conn.statements() {
				if strings.HasPrefix(st, "LISTEN ") {
					listens++
				}
			}
			t.Logf("%d LISTENs", listens)
			if limit := conns * (perConn + rate*3); rate > 0 && listens > limit {
				t.Fatalf("%d subscriptions started in 2s, over the rate limit of %d/s per connection", listens, rate)
			}
			settle(t, base+4)
			if p := f.hub.Pending(); p != 0 {
				t.Fatalf("hub still has %d channels waiting for LISTEN", p)
			}
		})
	}
}

func TestWSCompletedOperationReleasesSlot(t *testing.T) {
	f := wsServer(t, func(c *config.Server) { c.SubscriptionsMaxPerConnection = 1 })
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	c.expect("next")
	c.send(`{"id":"a","type":"complete"}`)
	c.send(`{"id":"b","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\", initialEvent: true) { relatedNodeId } }"}}`)
	if m := c.expect("next"); m.ID != "b" {
		t.Fatalf("next for %q, want b", m.ID)
	}
}

func TestWSMaxConnections(t *testing.T) {
	f := wsServer(t, func(c *config.Server) { c.SubscriptionsMaxConnections = 2 })
	dialWS(t, f.url, subprotocolTransportWS).init("")
	dialWS(t, f.url, subprotocolTransportWS).init("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, f.url, &websocket.DialOptions{Subprotocols: []string{subprotocolTransportWS}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("third connection: err=%v resp=%v, want 503", err, resp)
	}
}

func TestWSMaxOperations(t *testing.T) {
	f := wsServer(t, func(c *config.Server) { c.SubscriptionsMaxOperations = 1 })
	a := dialWS(t, f.url, subprotocolTransportWS)
	a.init("")
	a.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\", initialEvent: true) { relatedNodeId } }"}}`)
	a.expect("next")
	b := dialWS(t, f.url, subprotocolTransportWS)
	b.init("")
	b.send(`{"id":"b","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\") { relatedNodeId } }"}}`)
	if e := b.expect("error"); !strings.Contains(string(e.Payload), "too many operations on this server") {
		t.Fatalf("second connection: %s", e.Payload)
	}
	a.send(`{"id":"a","type":"complete"}`)
	deadline := time.Now().Add(5 * time.Second)
	for f.srv.ws.ops.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("completed operation still counted")
		}
		time.Sleep(5 * time.Millisecond)
	}
	b.send(`{"id":"c","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\", initialEvent: true) { relatedNodeId } }"}}`)
	b.expect("next")
}

func TestWSPendingListenLimit(t *testing.T) {
	f := wsServer(t, nil)
	f.hub.SetMaxPending(1)
	f.conn.setDelay(time.Hour)
	c := dialWS(t, f.url, subprotocolTransportWS)
	c.init("")
	c.send(`{"id":"a","type":"subscribe","payload":{"query":"subscription { listen(topic: \"x\") { relatedNodeId } }"}}`)
	deadline := time.Now().Add(5 * time.Second)
	for f.hub.Pending() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("first subscription never waited for its LISTEN")
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.send(`{"id":"b","type":"subscribe","payload":{"query":"subscription { listen(topic: \"y\") { relatedNodeId } }"}}`)
	if e := c.expect("error"); e.ID != "b" || !strings.Contains(string(e.Payload), "try again later") {
		t.Fatalf("second subscription: %s %s", e.ID, e.Payload)
	}
}
