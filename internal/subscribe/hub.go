// Package subscribe multiplexes PostgreSQL LISTEN/NOTIFY for GraphQL
// subscriptions: one dedicated connection LISTENs on the channels that have
// subscribers (LISTEN on the first, UNLISTEN after the last leaves), fans
// each notification out to bounded per-subscriber queues, and reconnects
// with backoff when the connection fails.
package subscribe

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/suprbdev/pdbcore/pgexec"
)

// ErrOverflow ends a subscription whose queue filled up because its
// subscriber consumed notifications more slowly than they arrived.
var ErrOverflow = errors.New("subscription dropped: the subscriber fell too far behind")

// ErrClosed ends subscriptions when the hub shuts down, and is returned by
// Subscribe afterwards.
var ErrClosed = errors.New("subscriptions are shutting down")

// ErrBusy is returned by Subscribe when a new channel would exceed the
// number of channels waiting for their LISTEN (see SetMaxPending).
var ErrBusy = errors.New("too many subscriptions are waiting to start; try again later")

// DefaultMaxPending is the default bound of the channels waiting for their
// LISTEN.
const DefaultMaxPending = 1024

// Conn is the dedicated LISTEN connection. Exec runs one simple-protocol
// statement; WaitForNotification blocks for the next notification and must
// return promptly, leaving the connection usable, when ctx is cancelled.
type Conn interface {
	Exec(ctx context.Context, sql string) error
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close(ctx context.Context) error
}

// DialFunc opens a new LISTEN connection.
type DialFunc func(ctx context.Context) (Conn, error)

// healthInterval is how long the connection may sit idle before the hub
// pings it, so a silently dropped connection is noticed and re-established.
const healthInterval = 30 * time.Second

const (
	minBackoff = 100 * time.Millisecond
	maxBackoff = 5 * time.Second
	// execTimeout bounds LISTEN, UNLISTEN and the health ping, so a
	// connection that silently stopped answering is replaced.
	execTimeout = 10 * time.Second
)

// Hub owns the LISTEN connection. It is safe for concurrent use; the
// connection is opened on the first Subscribe.
type Hub struct {
	dial DialFunc
	log  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	mu        sync.Mutex
	started   bool
	closed    bool
	subs      map[string]map[*Listener]struct{}
	listening map[string]bool
	// unlistening holds channels whose UNLISTEN is in flight; they no
	// longer count as listening for Subscribe.
	unlistening map[string]bool
	waiters     map[string][]chan error
	dirty       bool
	interrupt   context.CancelFunc
	maxPending  int
}

// NewHub returns a hub that opens its LISTEN connection with dial.
func NewHub(dial DialFunc, log *slog.Logger) *Hub {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		dial:        dial,
		log:         log,
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
		subs:        map[string]map[*Listener]struct{}{},
		listening:   map[string]bool{},
		unlistening: map[string]bool{},
		waiters:     map[string][]chan error{},
		maxPending:  DefaultMaxPending,
	}
}

// SetMaxPending bounds the channels waiting for their LISTEN: the one LISTEN
// connection works through them in turn, so a Subscribe for a new channel
// beyond n fails with ErrBusy instead of queueing. n < 1 means
// DefaultMaxPending.
func (h *Hub) SetMaxPending(n int) {
	if n < 1 {
		n = DefaultMaxPending
	}
	h.mu.Lock()
	h.maxPending = n
	h.mu.Unlock()
}

// Pending returns the number of channels waiting for their LISTEN.
func (h *Hub) Pending() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.waiters)
}

// PoolDialer returns a DialFunc opening connections with pool's connection
// settings, outside the pool so LISTEN never holds one of its slots.
func PoolDialer(pool *pgxpool.Pool) DialFunc {
	return func(ctx context.Context) (Conn, error) {
		cc := pool.Config().ConnConfig.Copy()
		// The default handler cancels a waiting query with a cancel request
		// that can land on the next statement; a deadline just interrupts
		// the read and leaves the connection usable.
		cc.BuildContextWatcherHandler = func(pc *pgconn.PgConn) ctxwatch.Handler {
			return &pgconn.DeadlineContextWatcherHandler{Conn: pc.Conn()}
		}
		c, err := pgx.ConnectConfig(ctx, cc)
		if err != nil {
			return nil, err
		}
		return pgxConn{c}, nil
	}
}

type pgxConn struct{ c *pgx.Conn }

func (p pgxConn) Exec(ctx context.Context, sql string) error {
	_, err := p.c.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol)
	return err
}

func (p pgxConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	return p.c.WaitForNotification(ctx)
}

func (p pgxConn) Close(ctx context.Context) error { return p.c.Close(ctx) }

// Listener receives the payloads of the notifications on one channel.
type Listener struct {
	hub     *Hub
	channel string
	ch      chan string
	err     error
	closed  bool
}

// Events delivers notification payloads in arrival order. It is closed
// when the listener ends; Err then tells why.
func (l *Listener) Events() <-chan string { return l.ch }

// Err returns ErrOverflow or ErrClosed once Events is closed by the hub,
// and nil when the listener was closed by its owner or is still open.
func (l *Listener) Err() error {
	l.hub.mu.Lock()
	defer l.hub.mu.Unlock()
	return l.err
}

// Close stops delivery and releases the channel: the hub UNLISTENs once its
// last listener is gone. It is idempotent.
func (l *Listener) Close() {
	h := l.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	h.removeLocked(l, nil)
}

// Subscribe registers a listener on channel with a queue of buf payloads
// and returns once the hub is LISTENing on it, so every notification sent
// after Subscribe returns is delivered. A full queue ends the listener with
// ErrOverflow. channel is quoted as an identifier, never interpolated.
func (h *Hub) Subscribe(ctx context.Context, channel string, buf int) (*Listener, error) {
	if buf < 1 {
		buf = 1
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	if !h.listening[channel] && h.waiters[channel] == nil && len(h.waiters) >= h.maxPending {
		h.mu.Unlock()
		return nil, ErrBusy
	}
	l := &Listener{hub: h, channel: channel, ch: make(chan string, buf)}
	set := h.subs[channel]
	if set == nil {
		set = map[*Listener]struct{}{}
		h.subs[channel] = set
	}
	set[l] = struct{}{}
	if h.listening[channel] {
		h.mu.Unlock()
		return l, nil
	}
	wait := make(chan error, 1)
	h.waiters[channel] = append(h.waiters[channel], wait)
	if !h.started {
		h.started = true
		go h.run()
	}
	h.kickLocked()
	h.mu.Unlock()

	select {
	case err := <-wait:
		if err != nil {
			l.Close()
			return nil, err
		}
		return l, nil
	case <-ctx.Done():
		h.mu.Lock()
		h.dropWaiterLocked(channel, wait)
		h.removeLocked(l, nil)
		h.mu.Unlock()
		return nil, ctx.Err()
	}
}

// dropWaiterLocked removes wait from the Subscribe calls waiting for
// channel's LISTEN.
func (h *Hub) dropWaiterLocked(channel string, wait chan error) {
	ws := h.waiters[channel]
	if i := slices.Index(ws, wait); i >= 0 {
		ws = slices.Delete(ws, i, i+1)
	}
	if len(ws) == 0 {
		delete(h.waiters, channel)
	} else {
		h.waiters[channel] = ws
	}
}

// Close stops the hub: listeners end with ErrClosed and the connection is
// closed. It is idempotent.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	started := h.started
	for _, set := range h.subs {
		for l := range set {
			h.removeLocked(l, ErrClosed)
		}
	}
	h.failWaitersLocked(ErrClosed)
	h.mu.Unlock()
	h.cancel()
	if started {
		<-h.done
	}
}

// Listening reports the channels the connection currently LISTENs on,
// including those whose UNLISTEN has not completed yet.
func (h *Hub) Listening() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.listening)+len(h.unlistening))
	for ch := range h.listening {
		out = append(out, ch)
	}
	for ch := range h.unlistening {
		if !h.listening[ch] {
			out = append(out, ch)
		}
	}
	return out
}

func (h *Hub) removeLocked(l *Listener, err error) {
	if l.closed {
		return
	}
	l.closed, l.err = true, err
	close(l.ch)
	if set := h.subs[l.channel]; set != nil {
		delete(set, l)
		if len(set) == 0 {
			delete(h.subs, l.channel)
			h.kickLocked()
		}
	}
}

// kickLocked asks the run loop to reconcile LISTENs with h.subs,
// interrupting a pending WaitForNotification.
func (h *Hub) kickLocked() {
	h.dirty = true
	if h.interrupt != nil {
		h.interrupt()
	}
}

func (h *Hub) failWaitersLocked(err error) {
	for ch, ws := range h.waiters {
		for _, w := range ws {
			w <- err
		}
		delete(h.waiters, ch)
	}
}

func (h *Hub) run() {
	defer close(h.done)
	backoff := minBackoff
	for {
		conn, err := h.dial(h.ctx)
		if err == nil {
			err = h.serve(conn, func() { backoff = minBackoff })
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = conn.Close(closeCtx)
			cancel()
		}
		h.mu.Lock()
		h.listening, h.unlistening = map[string]bool{}, map[string]bool{}
		if h.ctx.Err() == nil {
			h.failWaitersLocked(fmt.Errorf("subscriptions unavailable: %w", err))
		}
		h.mu.Unlock()
		if h.ctx.Err() != nil {
			return
		}
		h.log.Warn("subscriptions: LISTEN connection failed; reconnecting", "err", err, "in", backoff)
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// serve runs one connection until it fails or the hub closes; healthy is
// called once its LISTENs are in place.
func (h *Hub) serve(conn Conn, healthy func()) error {
	h.mu.Lock()
	h.dirty = true
	h.mu.Unlock()
	for first := true; ; first = false {
		if err := h.reconcile(conn); err != nil {
			return err
		}
		if first {
			healthy()
		}
		wctx, cancel := context.WithTimeout(h.ctx, healthInterval)
		h.mu.Lock()
		h.interrupt = cancel
		if h.dirty {
			cancel()
		}
		h.mu.Unlock()
		n, err := conn.WaitForNotification(wctx)
		h.mu.Lock()
		h.interrupt = nil
		h.mu.Unlock()
		waitErr := wctx.Err()
		cancel()
		if n != nil {
			h.dispatch(n)
		}
		switch {
		case h.ctx.Err() != nil:
			return h.ctx.Err()
		case err == nil, errors.Is(waitErr, context.Canceled):
		case errors.Is(waitErr, context.DeadlineExceeded):
			if err := h.exec(conn, "-- ping"); err != nil {
				return err
			}
		default:
			return err
		}
	}
}

// reconcile LISTENs on channels that gained subscribers and UNLISTENs those
// that lost their last one, then releases the Subscribe calls waiting for
// their channel.
func (h *Hub) reconcile(conn Conn) error {
	for {
		h.mu.Lock()
		if !h.dirty {
			h.mu.Unlock()
			return nil
		}
		h.dirty = false
		var listen, unlisten []string
		for ch := range h.subs {
			if !h.listening[ch] {
				listen = append(listen, ch)
			}
		}
		for ch := range h.listening {
			if h.subs[ch] == nil {
				unlisten = append(unlisten, ch)
				delete(h.listening, ch)
				h.unlistening[ch] = true
			}
		}
		h.mu.Unlock()

		for _, ch := range unlisten {
			if err := h.exec(conn, "UNLISTEN "+pgexec.QuoteIdent(ch)); err != nil {
				return err
			}
			h.mu.Lock()
			delete(h.unlistening, ch)
			h.mu.Unlock()
		}
		for _, ch := range listen {
			if err := h.exec(conn, "LISTEN "+pgexec.QuoteIdent(ch)); err != nil {
				return err
			}
			h.mu.Lock()
			h.listening[ch] = true
			for _, w := range h.waiters[ch] {
				w <- nil
			}
			delete(h.waiters, ch)
			h.mu.Unlock()
		}
	}
}

func (h *Hub) exec(conn Conn, sql string) error {
	ctx, cancel := context.WithTimeout(h.ctx, execTimeout)
	defer cancel()
	return conn.Exec(ctx, sql)
}

func (h *Hub) dispatch(n *pgconn.Notification) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for l := range h.subs[n.Channel] {
		select {
		case l.ch <- n.Payload:
		default:
			h.removeLocked(l, ErrOverflow)
		}
	}
}
