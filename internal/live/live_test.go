package live

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/suprbdev/pdbq/internal/subscribe"
)

// fakeConn is an in-memory LISTEN connection.
type fakeConn struct {
	queue  chan *pgconn.Notification
	broken chan struct{}
	once   sync.Once
}

func newFakeConn() *fakeConn {
	return &fakeConn{queue: make(chan *pgconn.Notification, 64), broken: make(chan struct{})}
}

func (f *fakeConn) Exec(context.Context, string) error {
	select {
	case <-f.broken:
		return errors.New("connection reset")
	default:
		return nil
	}
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

func (f *fakeConn) Close(context.Context) error { return nil }

func (f *fakeConn) breakConn() { f.once.Do(func() { close(f.broken) }) }

type fixture struct {
	t     *testing.T
	mu    sync.Mutex
	conns []*fakeConn
	hub   *subscribe.Hub
	tr    *Tracker
}

func newFixture(t *testing.T, opts Options) *fixture {
	t.Helper()
	f := &fixture{t: t}
	f.hub = subscribe.NewHub(func(context.Context) (subscribe.Conn, error) {
		c := newFakeConn()
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		return c, nil
	}, nil)
	f.tr = NewTracker(f.hub, opts)
	t.Cleanup(func() {
		f.tr.Close()
		f.hub.Close()
	})
	return f
}

func (f *fixture) conn() *fakeConn {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns[len(f.conns)-1]
}

func (f *fixture) notify(oid uint32) {
	f.conn().queue <- &pgconn.Notification{Channel: f.tr.Channel(), Payload: strconv.FormatUint(uint64(oid), 10)}
}

func (f *fixture) watch(tables ...uint32) *Watch {
	f.t.Helper()
	m := map[uint32]string{}
	for _, oid := range tables {
		m[oid] = "t" + strconv.Itoa(int(oid))
	}
	w, err := f.tr.Watch(context.Background(), m)
	if err != nil {
		f.t.Fatal(err)
	}
	return w
}

// counter is a Refresh returning the value of a shared counter, so tests
// decide whether a run's result changes.
type counter struct {
	value atomic.Int64
	runs  atomic.Int64
}

func (c *counter) refresh(context.Context) ([]byte, bool) {
	c.runs.Add(1)
	return []byte(strconv.FormatInt(c.value.Load(), 10)), false
}

// next calls s.Next with a timeout and returns its result, or "" when it
// times out.
func next(t *testing.T, s *Stream, d time.Duration) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	b, err := s.Next(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStreamRefreshesOnChange(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	w := f.watch(10, 11)
	s := f.tr.Stream(w, c.refresh)
	if got := next(t, s, time.Second); got != "0" {
		t.Fatalf("first result %q", got)
	}
	c.value.Store(1)
	f.notify(99)
	if got := next(t, s, 100*time.Millisecond); got != "" {
		t.Fatalf("a change to an unwatched table sent %q", got)
	}
	f.notify(11)
	if got := next(t, s, time.Second); got != "1" {
		t.Fatalf("result after change %q, want 1", got)
	}
}

func TestStreamDedupes(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	s := f.tr.Stream(f.watch(10), c.refresh)
	next(t, s, time.Second)
	f.notify(10)
	if got := next(t, s, 200*time.Millisecond); got != "" {
		t.Fatalf("identical result sent again: %q", got)
	}
	if c.runs.Load() != 2 {
		t.Fatalf("runs = %d, want 2 (the change re-ran the operation)", c.runs.Load())
	}
	c.value.Store(2)
	f.notify(10)
	if got := next(t, s, time.Second); got != "2" {
		t.Fatalf("changed result %q", got)
	}
}

func TestStreamThrottles(t *testing.T) {
	const throttle = 300 * time.Millisecond
	f := newFixture(t, Options{Throttle: throttle})
	c := &counter{}
	s := f.tr.Stream(f.watch(10), c.refresh)
	start := time.Now()
	next(t, s, time.Second)
	for i := range 10 {
		c.value.Store(int64(i + 1))
		f.notify(10)
	}
	if got := next(t, s, 2*time.Second); got != "10" {
		t.Fatalf("throttled result %q, want 10", got)
	}
	if d := time.Since(start); d < throttle {
		t.Errorf("second run %v after the first, want >= %v", d, throttle)
	}
	if n := c.runs.Load(); n != 2 {
		t.Errorf("runs = %d, want 2: the burst coalesces into one run", n)
	}
}

func TestWatchCloseUnregisters(t *testing.T) {
	f := newFixture(t, Options{})
	a, b := f.watch(10, 11), f.watch(11)
	a.Close()
	a.Close()
	f.tr.mu.Lock()
	_, has10 := f.tr.byTable[10]
	n11, nw := len(f.tr.byTable[11]), len(f.tr.watches)
	f.tr.mu.Unlock()
	if has10 || n11 != 1 || nw != 1 {
		t.Fatalf("after Close: table 10 registered %v, table 11 watches %d, watches %d", has10, n11, nw)
	}
	b.Close()
	f.tr.mu.Lock()
	defer f.tr.mu.Unlock()
	if len(f.tr.byTable) != 0 || len(f.tr.watches) != 0 {
		t.Fatalf("watches left: %v %v", f.tr.byTable, f.tr.watches)
	}
}

func TestReconnectRerunsAll(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	s := f.tr.Stream(f.watch(10), c.refresh)
	next(t, s, time.Second)
	c.value.Store(5)
	f.conn().breakConn()
	if got := next(t, s, 2*time.Second); got != "5" {
		t.Fatalf("result after reconnect %q, want 5", got)
	}
}

func TestOverflowRerunsAll(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	w := f.watch(10)
	s := f.tr.Stream(w, c.refresh)
	next(t, s, time.Second)
	c.value.Store(7)
	f.tr.mu.Lock()
	for range listenBuffer + 64 {
		f.notify(99)
	}
	time.Sleep(50 * time.Millisecond)
	f.tr.mu.Unlock()
	if got := next(t, s, 5*time.Second); got != "7" {
		t.Fatalf("result after overflow %q, want 7", got)
	}
}

func TestConcurrencyBound(t *testing.T) {
	f := newFixture(t, Options{Concurrency: 2})
	var inFlight, peak atomic.Int64
	release := make(chan struct{})
	refresh := func(context.Context) ([]byte, bool) {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		inFlight.Add(-1)
		return []byte("x"), false
	}
	var wg sync.WaitGroup
	for range 6 {
		s := f.tr.Stream(f.watch(10), refresh)
		wg.Go(func() { _, _ = s.Next(context.Background()) })
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if p := peak.Load(); p != 2 {
		t.Fatalf("peak concurrent runs %d, want 2", p)
	}
}

func TestCloseEndsStreams(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	s := f.tr.Stream(f.watch(10), c.refresh)
	next(t, s, time.Second)
	done := make(chan error, 1)
	go func() {
		_, err := s.Next(context.Background())
		done <- err
	}()
	f.tr.Close()
	select {
	case err := <-done:
		if !errors.Is(err, subscribe.ErrClosed) {
			t.Fatalf("Next after Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Next did not return after Close")
	}
	if _, err := f.tr.Watch(context.Background(), nil); !errors.Is(err, subscribe.ErrClosed) {
		t.Fatalf("Watch after Close: %v", err)
	}
}

func TestDispatchIgnoresBadPayloads(t *testing.T) {
	f := newFixture(t, Options{})
	c := &counter{}
	s := f.tr.Stream(f.watch(10), c.refresh)
	next(t, s, time.Second)
	c.value.Store(1)
	for _, p := range []string{"", "abc", "-10", "4294967306", "10 "} {
		f.conn().queue <- &pgconn.Notification{Channel: f.tr.Channel(), Payload: p}
	}
	if got := next(t, s, 200*time.Millisecond); got != "" {
		t.Fatalf("malformed payload woke the stream: %q", got)
	}
}

func TestValidChannel(t *testing.T) {
	for ch, want := range map[string]bool{
		"pdbq_live":                             true,
		"_x":                                    true,
		"Live2":                                 true,
		"":                                      false,
		"2live":                                 false,
		"pdbq-live":                             false,
		`pdbq"live`:                             false,
		"pdbq live":                             false,
		string(make([]byte, MaxChannelBytes+1)): false,
	} {
		if got := ValidChannel(ch); got != want {
			t.Errorf("ValidChannel(%q) = %v, want %v", ch, got, want)
		}
	}
	long := ""
	for range MaxChannelBytes {
		long += "a"
	}
	if !ValidChannel(long) || ValidChannel(long+"a") {
		t.Errorf("channel length limit is not %d bytes", MaxChannelBytes)
	}
}
