package subscribe

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// fakeConn is an in-memory LISTEN connection: Exec records statements and
// WaitForNotification delivers what notify queues.
type fakeConn struct {
	mu     sync.Mutex
	execs  []string
	queue  chan *pgconn.Notification
	broken chan struct{}
	closed bool
}

func newFakeConn() *fakeConn {
	return &fakeConn{queue: make(chan *pgconn.Notification, 64), broken: make(chan struct{})}
}

func (f *fakeConn) Exec(_ context.Context, sql string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.broken:
		return errors.New("connection reset")
	default:
	}
	f.execs = append(f.execs, sql)
	return nil
}

func (f *fakeConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	select {
	case n := <-f.queue:
		return n, nil
	case <-f.broken:
		return nil, errors.New("connection reset")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeConn) Close(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeConn) statements() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.execs)
}

func (f *fakeConn) notify(channel, payload string) {
	f.queue <- &pgconn.Notification{Channel: channel, Payload: payload}
}

// fakeDialer hands out fresh fakeConns and records them.
type fakeDialer struct {
	mu    sync.Mutex
	conns []*fakeConn
	fail  error
}

func (d *fakeDialer) dial(context.Context) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.fail != nil {
		return nil, d.fail
	}
	c := newFakeConn()
	d.conns = append(d.conns, c)
	return c, nil
}

func (d *fakeDialer) conn(i int) *fakeConn {
	d.mu.Lock()
	defer d.mu.Unlock()
	if i >= len(d.conns) {
		return nil
	}
	return d.conns[i]
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recv(t *testing.T, l *Listener) string {
	t.Helper()
	select {
	case p, ok := <-l.Events():
		if !ok {
			t.Fatalf("listener closed: %v", l.Err())
		}
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for an event")
	}
	return ""
}

func TestHubListenFanOutUnlisten(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	defer h.Close()
	ctx := context.Background()

	a, err := h.Subscribe(ctx, `postgraphile:a"b`, 4)
	if err != nil {
		t.Fatal(err)
	}
	b, err := h.Subscribe(ctx, `postgraphile:a"b`, 4)
	if err != nil {
		t.Fatal(err)
	}
	c := d.conn(0)
	if got, want := c.statements(), []string{`LISTEN "postgraphile:a""b"`}; !slices.Equal(got, want) {
		t.Fatalf("statements = %q, want %q", got, want)
	}
	c.notify(`postgraphile:a"b`, "one")
	c.notify("postgraphile:other", "ignored")
	if p := recv(t, a); p != "one" {
		t.Errorf("a got %q", p)
	}
	if p := recv(t, b); p != "one" {
		t.Errorf("b got %q", p)
	}

	a.Close()
	a.Close()
	if _, ok := <-a.Events(); ok {
		t.Error("closed listener still delivers")
	}
	if a.Err() != nil {
		t.Errorf("Err after Close = %v", a.Err())
	}
	b.Close()
	eventually(t, "UNLISTEN", func() bool {
		return slices.Contains(c.statements(), `UNLISTEN "postgraphile:a""b"`)
	})
	if got := h.Listening(); len(got) != 0 {
		t.Errorf("still listening on %q", got)
	}
}

func TestHubOverflow(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	defer h.Close()
	slow, err := h.Subscribe(context.Background(), "ch", 1)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := h.Subscribe(context.Background(), "ch", 8)
	if err != nil {
		t.Fatal(err)
	}
	c := d.conn(0)
	c.notify("ch", "1")
	c.notify("ch", "2")
	recv(t, fast)
	recv(t, fast)
	if p := recv(t, slow); p != "1" {
		t.Errorf("slow got %q", p)
	}
	eventually(t, "overflow", func() bool { return errors.Is(slow.Err(), ErrOverflow) })
	if _, ok := <-slow.Events(); ok {
		t.Error("overflowed listener still open")
	}
	c.notify("ch", "3")
	if p := recv(t, fast); p != "3" {
		t.Errorf("fast got %q after overflow of another listener", p)
	}
}

func TestHubReconnect(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	defer h.Close()
	l, err := h.Subscribe(context.Background(), "ch", 4)
	if err != nil {
		t.Fatal(err)
	}
	close(d.conn(0).broken)
	eventually(t, "reconnect", func() bool {
		c := d.conn(1)
		return c != nil && slices.Contains(c.statements(), `LISTEN "ch"`)
	})
	d.conn(1).notify("ch", "after")
	if p := recv(t, l); p != "after" {
		t.Errorf("got %q", p)
	}
}

func TestHubReconnectHook(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	defer h.Close()
	var calls atomic.Int32
	h.SetReconnectHook(func() { calls.Add(1) })
	if _, err := h.Subscribe(context.Background(), "ch", 4); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("hook called %d times on the first connection", n)
	}
	close(d.conn(0).broken)
	eventually(t, "reconnect hook", func() bool { return calls.Load() == 1 })
	if c := d.conn(1); !slices.Contains(c.statements(), `LISTEN "ch"`) {
		t.Errorf("hook ran before the channels were LISTENed again: %q", c.statements())
	}
}

func TestHubDialFailure(t *testing.T) {
	d := &fakeDialer{fail: errors.New("refused")}
	h := NewHub(d.dial, nil)
	defer h.Close()
	_, err := h.Subscribe(context.Background(), "ch", 4)
	if err == nil {
		t.Fatal("Subscribe succeeded without a connection")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	d.mu.Lock()
	d.fail = nil
	d.mu.Unlock()
	if _, err := h.Subscribe(ctx, "ch2", 4); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Subscribe: %v", err)
	}
	l, err := h.Subscribe(context.Background(), "ch3", 4)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestHubClose(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	l, err := h.Subscribe(context.Background(), "ch", 4)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	h.Close()
	if _, ok := <-l.Events(); ok {
		t.Error("listener open after Close")
	}
	if !errors.Is(l.Err(), ErrClosed) {
		t.Errorf("Err = %v", l.Err())
	}
	if _, err := h.Subscribe(context.Background(), "ch", 4); !errors.Is(err, ErrClosed) {
		t.Errorf("Subscribe after Close: %v", err)
	}
	if c := d.conn(0); c == nil || !c.closed {
		t.Error("connection not closed")
	}
	NewHub(d.dial, nil).Close()
}

func TestHubConcurrent(t *testing.T) {
	d := &fakeDialer{}
	h := NewHub(d.dial, nil)
	defer h.Close()
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			ch := fmt.Sprintf("ch%d", i%4)
			for range 20 {
				l, err := h.Subscribe(context.Background(), ch, 2)
				if err != nil {
					t.Error(err)
					return
				}
				if c := d.conn(0); c != nil {
					select {
					case c.queue <- &pgconn.Notification{Channel: ch, Payload: "x"}:
					default:
					}
				}
				l.Close()
			}
		})
	}
	wg.Wait()
	eventually(t, "all UNLISTENed", func() bool { return len(h.Listening()) == 0 })
}

// stalledDial never connects, so every Subscribe waits for its LISTEN until
// its context ends.
func stalledDial(ctx context.Context) (Conn, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestHubCancelledSubscribeReleasesWaiter(t *testing.T) {
	h := NewHub(stalledDial, nil)
	defer h.Close()
	var wg sync.WaitGroup
	for i := range 1000 {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if _, err := h.Subscribe(ctx, fmt.Sprintf("c%d", i), 1); !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("Subscribe = %v, want deadline exceeded", err)
			}
		})
	}
	wg.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.waiters) != 0 || len(h.subs) != 0 {
		t.Fatalf("after cancelled subscribes: %d waiter channels, %d subscribed channels", len(h.waiters), len(h.subs))
	}
}

func TestHubCancelledSubscribeKeepsOtherWaiters(t *testing.T) {
	h := NewHub(stalledDial, nil)
	defer h.Close()
	long, cancelLong := context.WithCancel(context.Background())
	defer cancelLong()
	done := make(chan error, 1)
	go func() {
		_, err := h.Subscribe(long, "shared", 1)
		done <- err
	}()
	eventually(t, "first waiter", func() bool { return h.Pending() == 1 })
	short, cancelShort := context.WithCancel(context.Background())
	go cancelShort()
	if _, err := h.Subscribe(short, "shared", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe = %v", err)
	}
	h.mu.Lock()
	n := len(h.waiters["shared"])
	h.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d waiters on shared, want the first one", n)
	}
	cancelLong()
	<-done
	if p := h.Pending(); p != 0 {
		t.Fatalf("pending = %d", p)
	}
}

func TestHubMaxPending(t *testing.T) {
	h := NewHub(stalledDial, nil)
	defer h.Close()
	h.SetMaxPending(2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for _, ch := range []string{"a", "b", "b"} {
		wg.Go(func() { _, _ = h.Subscribe(ctx, ch, 1) })
	}
	eventually(t, "two pending channels", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.waiters) == 2 && len(h.waiters["b"]) == 2
	})
	if _, err := h.Subscribe(ctx, "c", 1); !errors.Is(err, ErrBusy) {
		t.Fatalf("third channel: %v, want ErrBusy", err)
	}
	cancel()
	wg.Wait()
	if p := h.Pending(); p != 0 {
		t.Fatalf("pending = %d", p)
	}
}
