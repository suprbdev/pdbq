// Package live serves live queries: it routes the change notifications that
// pdbq's per-table triggers send on one LISTEN channel to the live
// operations reading those tables, and paces how often each operation
// re-runs (throttle, a server-wide concurrency bound, and suppression of
// results identical to the last one sent).
package live

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/suprbdev/pdbcore/metrics"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

// DefaultChannel is the default LISTEN channel of table change
// notifications (server.live_channel).
const DefaultChannel = "pdbq_live"

// listenBuffer is the queue of the tracker's one listener; when it
// overflows, every live operation re-runs once.
const listenBuffer = 4096

const (
	minBackoff = 100 * time.Millisecond
	maxBackoff = 5 * time.Second
)

// Options configures a Tracker.
type Options struct {
	// Channel is the LISTEN channel the change triggers notify, with the
	// changed table's OID as the payload. Empty means DefaultChannel.
	Channel string
	// Throttle is the shortest time between the starts of two runs of one
	// live operation. Changes arriving in between are coalesced into one
	// run at the end of the interval.
	Throttle time.Duration
	// Concurrency bounds the live operation runs in flight across the
	// server; values below 1 mean 1.
	Concurrency int
	// Metrics records notifications, runs and operations; nil records
	// nothing.
	Metrics *Metrics
	Log     *slog.Logger
}

// Metrics are the live query counters.
type Metrics struct {
	// Changes counts table change notifications received.
	Changes *metrics.Counter
	// Refreshes counts runs of live operations by result: sent (a new
	// result went to the client), unchanged (identical to the last one,
	// not sent) or error (sent, carrying errors).
	Refreshes *metrics.Counter
	// Operations counts live operations by event: started, stopped, or
	// rejected by a cap.
	Operations *metrics.Counter
}

// NewMetrics registers live_changes_total, live_refreshes_total{result}
// and live_operations_total{event} on m. A nil m gives a nil *Metrics.
func NewMetrics(m *metrics.Metrics) (*Metrics, error) {
	if m == nil {
		return nil, nil
	}
	changes, err := m.NewCounter("live_changes_total", "Table change notifications received for live queries.")
	if err != nil {
		return nil, err
	}
	refreshes, err := m.NewCounter("live_refreshes_total", "Live query runs by result (sent, unchanged, error).", "result")
	if err != nil {
		return nil, err
	}
	for _, r := range []string{"sent", "unchanged", "error"} {
		refreshes.Init(r)
	}
	ops, err := m.NewCounter("live_operations_total", "Live query operations by event (started, stopped, rejected); started minus stopped is the number running.", "event")
	if err != nil {
		return nil, err
	}
	for _, e := range []string{"started", "stopped", "rejected"} {
		ops.Init(e)
	}
	return &Metrics{Changes: changes, Refreshes: refreshes, Operations: ops}, nil
}

func (m *Metrics) change() {
	if m != nil {
		m.Changes.Inc()
	}
}

func (m *Metrics) refresh(result string) {
	if m != nil {
		m.Refreshes.Inc(result)
	}
}

func (m *Metrics) operation(event string) {
	if m != nil {
		m.Operations.Inc(event)
	}
}

// Tracker routes table change notifications to the live operations that
// read the changed tables. It is safe for concurrent use. It LISTENs, through
// the hub, from the first Watch until Close.
type Tracker struct {
	hub  *subscribe.Hub
	opts Options
	log  *slog.Logger
	sem  chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	watches map[*Watch]struct{}
	byTable map[uint32]map[*Watch]struct{}
	start   *startState
	// tracked holds the tables that have a change trigger, nil when unknown.
	tracked map[uint32]bool
	warned  map[uint32]bool
}

type startState struct {
	done chan struct{}
	err  error
}

// NewTracker returns a tracker that LISTENs through hub. It installs the
// tracker's reconnect hook on hub: after the LISTEN connection is replaced,
// every live operation re-runs, since notifications sent in between were
// lost. The caller owns hub and closes it after the tracker.
func NewTracker(hub *subscribe.Hub, opts Options) *Tracker {
	if opts.Channel == "" {
		opts.Channel = DefaultChannel
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t := &Tracker{
		hub:     hub,
		opts:    opts,
		log:     opts.Log,
		sem:     make(chan struct{}, max(opts.Concurrency, 1)),
		ctx:     ctx,
		cancel:  cancel,
		watches: map[*Watch]struct{}{},
		byTable: map[uint32]map[*Watch]struct{}{},
		warned:  map[uint32]bool{},
	}
	hub.SetReconnectHook(t.changedAll)
	return t
}

// Channel returns the LISTEN channel of change notifications.
func (t *Tracker) Channel() string { return t.opts.Channel }

// SetTracked records the tables that have a change trigger (see Install).
// Watch then logs a warning, once per table, for a live operation reading a
// table without one: changes to it do not re-run the operation.
func (t *Tracker) SetTracked(tables map[uint32]bool) {
	t.mu.Lock()
	t.tracked = tables
	t.mu.Unlock()
}

// Rejected counts a live operation refused by a cap.
func (t *Tracker) Rejected() { t.opts.Metrics.operation("rejected") }

// Close stops listening and ends every stream with subscribe.ErrClosed. It
// is idempotent.
func (t *Tracker) Close() {
	t.cancel()
	t.wg.Wait()
}

// Watch registers interest in changes to tables, the qualified names of
// root tables by OID as exec.Executor.ReferencedTables returns them, and
// returns once the tracker is LISTENing, so every change committed after
// Watch returns is reported.
func (t *Tracker) Watch(ctx context.Context, tables map[uint32]string) (*Watch, error) {
	if err := t.listen(ctx); err != nil {
		return nil, err
	}
	w := &Watch{t: t, ch: make(chan struct{}, 1)}
	for oid := range tables {
		w.tables = append(w.tables, oid)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx.Err() != nil {
		return nil, subscribe.ErrClosed
	}
	t.watches[w] = struct{}{}
	for oid, name := range tables {
		set := t.byTable[oid]
		if set == nil {
			set = map[*Watch]struct{}{}
			t.byTable[oid] = set
		}
		set[w] = struct{}{}
		if t.tracked != nil && !t.tracked[oid] && !t.warned[oid] {
			t.warned[oid] = true
			t.log.Warn("live: a live query reads a table without the change trigger; its changes do not refresh live queries", "table", name)
		}
	}
	t.opts.Metrics.operation("started")
	return w, nil
}

// listen starts the tracker's listener on first use and waits until it is
// LISTENing. A failed start is reported to the callers waiting for it and
// retried by the next call.
func (t *Tracker) listen(ctx context.Context) error {
	t.mu.Lock()
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		return subscribe.ErrClosed
	}
	st := t.start
	if st == nil {
		st = &startState{done: make(chan struct{})}
		t.start = st
		t.wg.Add(1)
		go t.run(st)
	}
	t.mu.Unlock()
	select {
	case <-st.done:
		return st.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// run owns the listener. Once started it re-subscribes after an overflow
// (or a hub error) with backoff, and re-runs every live operation after
// each gap in delivery.
func (t *Tracker) run(st *startState) {
	defer t.wg.Done()
	l, err := t.hub.Subscribe(t.ctx, t.opts.Channel, listenBuffer)
	if err != nil {
		t.mu.Lock()
		st.err = err
		t.start = nil
		t.mu.Unlock()
		close(st.done)
		return
	}
	close(st.done)
	backoff := minBackoff
	for {
		err := t.consume(l)
		l.Close()
		if t.ctx.Err() != nil || errors.Is(err, subscribe.ErrClosed) {
			t.cancel()
			return
		}
		t.log.Warn("live: change notifications interrupted; re-running every live query", "err", err)
		t.changedAll()
		for {
			l, err = t.hub.Subscribe(t.ctx, t.opts.Channel, listenBuffer)
			if err == nil {
				backoff = minBackoff
				break
			}
			if t.ctx.Err() != nil || errors.Is(err, subscribe.ErrClosed) {
				t.cancel()
				return
			}
			select {
			case <-t.ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
		}
		t.changedAll()
	}
}

// consume dispatches l's notifications until it ends, returning why.
func (t *Tracker) consume(l *subscribe.Listener) error {
	for {
		select {
		case <-t.ctx.Done():
			return t.ctx.Err()
		case p, ok := <-l.Events():
			if !ok {
				if err := l.Err(); err != nil {
					return err
				}
				return subscribe.ErrClosed
			}
			t.dispatch(p)
		}
	}
}

func (t *Tracker) dispatch(payload string) {
	oid, err := strconv.ParseUint(payload, 10, 32)
	if err != nil {
		return
	}
	t.opts.Metrics.change()
	t.mu.Lock()
	defer t.mu.Unlock()
	for w := range t.byTable[uint32(oid)] {
		w.signal()
	}
}

// changedAll marks every live operation changed.
func (t *Tracker) changedAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for w := range t.watches {
		w.signal()
	}
}

// acquire takes one of the Concurrency run slots.
func (t *Tracker) acquire(ctx context.Context) (func(), error) {
	select {
	case t.sem <- struct{}{}:
		return func() { <-t.sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.ctx.Done():
		return nil, subscribe.ErrClosed
	}
}

// Watch is one live operation's interest in its tables.
type Watch struct {
	t      *Tracker
	tables []uint32
	ch     chan struct{}
	once   sync.Once
}

// Changed receives a value when a watched table changed since the last
// receive; changes in between coalesce into one value.
func (w *Watch) Changed() <-chan struct{} { return w.ch }

func (w *Watch) signal() {
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

// Close unregisters the watch. It is idempotent.
func (w *Watch) Close() {
	w.once.Do(func() {
		t := w.t
		t.mu.Lock()
		delete(t.watches, w)
		for _, oid := range w.tables {
			if set := t.byTable[oid]; set != nil {
				delete(set, w)
				if len(set) == 0 {
					delete(t.byTable, oid)
				}
			}
		}
		t.mu.Unlock()
		t.opts.Metrics.operation("stopped")
	})
}

// Refresh runs a live operation once and returns its encoded result;
// failed reports whether the result carries errors.
type Refresh func(ctx context.Context) (body []byte, failed bool)

// Stream paces the runs of one live operation.
type Stream struct {
	t       *Tracker
	w       *Watch
	refresh Refresh
	last    time.Time
	sum     [sha256.Size]byte
	started bool
}

// Stream returns the stream of results of the live operation that refresh
// runs, re-run when w reports a change.
func (t *Tracker) Stream(w *Watch, refresh Refresh) *Stream {
	return &Stream{t: t, w: w, refresh: refresh}
}

// Next returns the next result to send: the first run's, then the result
// of the first run after a change that differs from the last one returned.
// Runs start at least Throttle apart, and at most Concurrency run at once
// across the tracker. It returns ctx's error when ctx ends and
// subscribe.ErrClosed when the tracker closes.
func (s *Stream) Next(ctx context.Context) ([]byte, error) {
	for {
		if s.started {
			select {
			case <-s.w.ch:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-s.t.ctx.Done():
				return nil, subscribe.ErrClosed
			}
			if wait := time.Until(s.last.Add(s.t.opts.Throttle)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				case <-s.t.ctx.Done():
					timer.Stop()
					return nil, subscribe.ErrClosed
				}
			}
			select {
			case <-s.w.ch:
			default:
			}
		}
		release, err := s.t.acquire(ctx)
		if err != nil {
			return nil, err
		}
		s.last = time.Now()
		body, failed := s.refresh(ctx)
		release()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		if s.started && sum == s.sum {
			s.t.opts.Metrics.refresh("unchanged")
			continue
		}
		s.started, s.sum = true, sum
		if failed {
			s.t.opts.Metrics.refresh("error")
		} else {
			s.t.opts.Metrics.refresh("sent")
		}
		return body, nil
	}
}
