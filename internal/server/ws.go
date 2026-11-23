package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/exec"
)

// WebSocket subprotocols: graphql-transport-ws is the graphql-ws library's
// protocol; graphql-ws is the legacy subscriptions-transport-ws protocol.
const (
	subprotocolTransportWS = "graphql-transport-ws"
	subprotocolLegacyWS    = "graphql-ws"
)

const (
	// wsInitTimeout is how long a client may take to send connection_init.
	wsInitTimeout = 10 * time.Second
	// wsWriteTimeout bounds one message write to a slow client.
	wsWriteTimeout = 10 * time.Second
)

// graphql-transport-ws close codes.
const (
	closeBadRequest       websocket.StatusCode = 4400
	closeUnauthorized     websocket.StatusCode = 4401
	closeForbidden        websocket.StatusCode = 4403
	closeSubprotocol      websocket.StatusCode = 4406
	closeInitTimeout      websocket.StatusCode = 4408
	closeSubscriberExists websocket.StatusCode = 4409
	closeTooManyInits     websocket.StatusCode = 4429
)

type wsMessage struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// isWebSocketUpgrade reports whether r asks to switch to WebSocket.
func isWebSocketUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		headerHasToken(r.Header, "Connection", "upgrade")
}

func headerHasToken(h http.Header, name, token string) bool {
	for _, v := range h.Values(name) {
		for t := range strings.SplitSeq(v, ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

// hijackWriter clears the read and write deadlines net/http leaves on a
// hijacked connection (Server.ReadTimeout), which would otherwise cut every
// WebSocket after that timeout.
type hijackWriter struct{ http.ResponseWriter }

func (w hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, brw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err == nil {
		err = c.SetDeadline(time.Time{})
	}
	return c, brw, err
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !s.reserveConnection() {
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "too many WebSocket connections")
		return
	}
	defer s.releaseConnection()
	opts := &websocket.AcceptOptions{
		Subprotocols:   []string{subprotocolTransportWS, subprotocolLegacyWS},
		OriginPatterns: s.cfg.CORSOrigins,
	}
	if slices.Contains(s.cfg.CORSOrigins, "*") {
		opts.InsecureSkipVerify, opts.OriginPatterns = true, nil
	}
	conn, err := websocket.Accept(hijackWriter{w}, r, opts)
	if err != nil {
		s.log.Debug("websocket: accept failed", "err", err)
		return
	}
	proto := conn.Subprotocol()
	if proto != subprotocolTransportWS && proto != subprotocolLegacyWS {
		_ = conn.Close(closeSubprotocol, "Subprotocol not acceptable")
		return
	}
	conn.SetReadLimit(s.cfg.MaxBodyBytes)
	ctx, cancel := context.WithCancel(context.Background())
	sess := &wsSession{
		s:      s,
		conn:   conn,
		legacy: proto == subprotocolLegacyWS,
		req:    r,
		ctx:    ctx,
		cancel: cancel,
		inited: make(chan struct{}),
		ops:    map[string]*wsOp{},
		slots:  make(chan struct{}, s.cfg.SubscriptionsMaxPerConnection),
		tokens: float64(s.cfg.SubscriptionsMaxPerConnection),
	}
	if !s.trackSession(sess) {
		cancel()
		_ = conn.Close(websocket.StatusGoingAway, "server shutting down")
		return
	}
	defer s.untrackSession(sess)
	sess.run()
}

// reserveConnection counts a WebSocket connection against
// server.subscriptions_max_connections; false when none is left.
func (s *Server) reserveConnection() bool {
	s.ws.mu.Lock()
	defer s.ws.mu.Unlock()
	if s.ws.conns >= s.cfg.SubscriptionsMaxConnections {
		return false
	}
	s.ws.conns++
	return true
}

func (s *Server) releaseConnection() {
	s.ws.mu.Lock()
	s.ws.conns--
	s.ws.mu.Unlock()
}

// trackSession registers sess for CloseWebSockets; false once closing.
func (s *Server) trackSession(sess *wsSession) bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	if s.wsClosed {
		return false
	}
	s.wsSessions[sess] = struct{}{}
	return true
}

func (s *Server) untrackSession(sess *wsSession) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	delete(s.wsSessions, sess)
}

// CloseWebSockets closes every WebSocket connection with 1001 (going away)
// and refuses new ones. net/http's graceful shutdown does not track
// hijacked connections; ListenAndServe calls this when its context ends,
// and embedders serving Handler call it on shutdown.
func (s *Server) CloseWebSockets() {
	s.wsMu.Lock()
	s.wsClosed = true
	sessions := make([]*wsSession, 0, len(s.wsSessions))
	for sess := range s.wsSessions {
		sessions = append(sessions, sess)
	}
	s.wsMu.Unlock()
	var wg sync.WaitGroup
	for _, sess := range sessions {
		wg.Go(func() { sess.close(websocket.StatusGoingAway, "server shutting down") })
	}
	wg.Wait()
}

// wsSession is one WebSocket connection.
type wsSession struct {
	s      *Server
	conn   *websocket.Conn
	legacy bool
	req    *http.Request
	ctx    context.Context
	cancel context.CancelFunc
	inited chan struct{}

	// wmu serializes writes with stop, so nothing is written for an
	// operation once the client completed it.
	wmu sync.Mutex

	mu       sync.Mutex
	initSeen bool
	acked    bool
	claims   map[string]any
	role     string
	expires  time.Time
	// ops holds the operations the client has not completed, by id.
	ops map[string]*wsOp
	wg  sync.WaitGroup

	// slots holds one token per operation goroutine still running,
	// completed or not: server.subscriptions_max_per_connection bounds
	// those, not just the entries of ops.
	slots chan struct{}

	// tokens and last are the subscribe rate limiter's bucket, used only by
	// the read loop.
	tokens float64
	last   time.Time
}

// wsOp is one running operation of a session.
type wsOp struct {
	cancel context.CancelFunc
}

func (c *wsSession) run() {
	defer func() {
		c.cancel()
		c.wg.Wait()
		_ = c.conn.CloseNow()
	}()
	c.wg.Go(c.watchInit)
	if k := c.s.cfg.SubscriptionsKeepalive; k > 0 {
		c.wg.Go(func() { c.keepalive(k) })
	}
	for {
		typ, data, err := c.conn.Read(context.Background())
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			c.close(closeBadRequest, "Invalid message received")
			return
		}
		var m wsMessage
		if err := json.Unmarshal(data, &m); err != nil || m.Type == "" {
			if c.legacy {
				c.send(c.ctx, legacyError("", "Invalid message received"))
				continue
			}
			c.close(closeBadRequest, "Invalid message received")
			return
		}
		if c.legacy {
			if !c.handleLegacy(m) {
				return
			}
		} else if !c.handleTransport(m) {
			return
		}
	}
}

// watchInit closes the connection when connection_init does not arrive in
// time.
func (c *wsSession) watchInit() {
	t := time.NewTimer(c.s.wsInitTimeout)
	defer t.Stop()
	select {
	case <-c.inited:
	case <-c.ctx.Done():
	case <-t.C:
		c.close(closeInitTimeout, "Connection initialisation timeout")
	}
}

// keepalive pings the peer every interval, disconnecting it when a pong
// does not arrive within the interval; legacy clients also get the ka
// message their keepalive timer expects.
func (c *wsSession) keepalive(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-t.C:
		}
		if c.legacy && c.isAcked() {
			c.send(c.ctx, []byte(`{"type":"ka"}`))
		}
		ctx, cancel := context.WithTimeout(context.Background(), interval)
		err := c.conn.Ping(ctx)
		cancel()
		if err != nil {
			c.cancel()
			_ = c.conn.CloseNow()
			return
		}
	}
}

// handleTransport handles one graphql-transport-ws message; false closes
// the connection.
func (c *wsSession) handleTransport(m wsMessage) bool {
	switch m.Type {
	case "connection_init":
		if !c.markInit() {
			c.close(closeTooManyInits, "Too many initialisation requests")
			return false
		}
		if err := c.authenticate(m.Payload); err != nil {
			c.close(closeForbidden, closeReason("Forbidden: "+err.Error()))
			return false
		}
		c.send(c.ctx, []byte(`{"type":"connection_ack"}`))
	case "ping":
		c.send(c.ctx, []byte(`{"type":"pong"}`))
	case "pong":
	case "subscribe":
		if !c.isAcked() {
			c.close(closeUnauthorized, "Unauthorized")
			return false
		}
		if m.ID == "" {
			c.close(closeBadRequest, "Invalid message received")
			return false
		}
		var req gqlHTTPRequest
		if err := json.Unmarshal(m.Payload, &req); err != nil {
			c.close(closeBadRequest, "Invalid message received")
			return false
		}
		if !c.start(m.ID, req) {
			c.close(closeSubscriberExists, closeReason("Subscriber for "+m.ID+" already exists"))
			return false
		}
	case "complete":
		c.stop(m.ID)
	default:
		c.close(closeBadRequest, closeReason("Invalid message received: unknown type "+m.Type))
		return false
	}
	return true
}

// handleLegacy handles one subscriptions-transport-ws message; false
// closes the connection.
func (c *wsSession) handleLegacy(m wsMessage) bool {
	switch m.Type {
	case "connection_init":
		if !c.markInit() {
			c.send(c.ctx, legacyMessage("connection_error", "", map[string]string{"message": "Too many initialisation requests"}))
			return true
		}
		if err := c.authenticate(m.Payload); err != nil {
			c.send(c.ctx, legacyMessage("connection_error", "", map[string]string{"message": err.Error()}))
			c.close(closeForbidden, "Forbidden")
			return false
		}
		c.send(c.ctx, []byte(`{"type":"connection_ack"}`))
		if c.s.cfg.SubscriptionsKeepalive > 0 {
			c.send(c.ctx, []byte(`{"type":"ka"}`))
		}
	case "start":
		if !c.isAcked() {
			c.send(c.ctx, legacyError(m.ID, "connection not initialised: send connection_init first"))
			return true
		}
		var req gqlHTTPRequest
		if m.ID == "" || json.Unmarshal(m.Payload, &req) != nil {
			c.send(c.ctx, legacyError(m.ID, "Invalid message received"))
			return true
		}
		if !c.start(m.ID, req) {
			c.send(c.ctx, legacyError(m.ID, "Subscriber for "+m.ID+" already exists"))
		}
	case "stop":
		c.stop(m.ID)
	case "connection_terminate":
		c.close(websocket.StatusNormalClosure, "")
		return false
	default:
		c.send(c.ctx, legacyError(m.ID, "Invalid message type "+m.Type))
	}
	return true
}

func (c *wsSession) markInit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initSeen {
		return false
	}
	c.initSeen = true
	return true
}

func (c *wsSession) isAcked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acked
}

// authenticate resolves the connection's identity from the upgrade
// request, with the connection_init payload's authorization (or
// headers.authorization) entry standing in for its Authorization header:
// browsers cannot set headers on a WebSocket. Only that one header is taken
// from the payload, so claim headers (headers mode) must come from the
// trusted gateway on the upgrade request.
func (c *wsSession) authenticate(payload json.RawMessage) error {
	r := c.req.Clone(c.ctx)
	if authz := initAuthorization(payload); authz != "" {
		r.Header.Set("Authorization", authz)
	}
	claims, role, err := c.s.auth.Authenticate(r)
	if err != nil {
		return err
	}
	if role != "" && !pgexec.ValidRole(role) {
		return fmt.Errorf("role %q is not allowed", role)
	}
	exp := claimExpiry(claims)
	c.mu.Lock()
	c.claims, c.role, c.expires, c.acked = claims, role, exp, true
	c.mu.Unlock()
	close(c.inited)
	if !exp.IsZero() {
		c.wg.Go(func() { c.expireAt(exp) })
	}
	return nil
}

// expireAt closes the connection with 4403 when its token expires.
func (c *wsSession) expireAt(exp time.Time) {
	t := time.NewTimer(time.Until(exp))
	defer t.Stop()
	select {
	case <-t.C:
		c.close(closeForbidden, "Forbidden: token expired")
	case <-c.ctx.Done():
	}
}

func initAuthorization(payload json.RawMessage) string {
	var m map[string]any
	if len(payload) == 0 || json.Unmarshal(payload, &m) != nil {
		return ""
	}
	find := func(m map[string]any) string {
		for k, v := range m {
			if s, ok := v.(string); ok && strings.EqualFold(k, "authorization") {
				return s
			}
		}
		return ""
	}
	if s := find(m); s != "" {
		return s
	}
	for k, v := range m {
		if h, ok := v.(map[string]any); ok && strings.EqualFold(k, "headers") {
			return find(h)
		}
	}
	return ""
}

// claimExpiry returns the exp claim as a time, zero when absent.
func claimExpiry(claims map[string]any) time.Time {
	var sec float64
	switch v := claims["exp"].(type) {
	case float64:
		sec = v
	case int64:
		sec = float64(v)
	case int:
		sec = float64(v)
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return time.Time{}
		}
		sec = f
	default:
		return time.Time{}
	}
	return time.Unix(int64(sec), 0)
}

// expired reports whether the connection's token has expired.
func (c *wsSession) expired() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.expires.IsZero() && time.Now().After(c.expires)
}

// start launches operation id; false when id is already running.
//
// Every operation holds a slot of the connection until its goroutine has
// returned, and one of the server's server.subscriptions_max_operations. A
// client that completes operations faster than they stop therefore waits
// for their slots (its next message is read later) instead of piling up
// goroutines; one whose operations are all still running gets an error.
func (c *wsSession) start(id string, req gqlHTTPRequest) bool {
	if c.expired() {
		c.close(closeForbidden, "Forbidden: token expired")
		return true
	}
	c.throttle()
	c.mu.Lock()
	_, dup := c.ops[id]
	live := len(c.ops)
	c.mu.Unlock()
	if dup {
		return false
	}
	if !c.acquireSlot(live) {
		if c.ctx.Err() == nil {
			c.sendErrors(c.ctx, id, gqlerror.List{gqlerror.Errorf("too many operations on this connection (limit %d)", c.s.cfg.SubscriptionsMaxPerConnection)})
		}
		return true
	}
	if !c.s.reserveOperation() {
		<-c.slots
		c.sendErrors(c.ctx, id, gqlerror.List{gqlerror.Errorf("too many operations on this server; try again later")})
		return true
	}
	ctx, cancel := context.WithCancel(c.ctx)
	op := &wsOp{cancel: cancel}
	c.mu.Lock()
	c.ops[id] = op
	claims, role := c.claims, c.role
	c.mu.Unlock()
	c.wg.Go(func() {
		defer func() {
			c.mu.Lock()
			if c.ops[id] == op {
				delete(c.ops, id)
			}
			c.mu.Unlock()
			cancel()
			c.s.releaseOperation()
			<-c.slots
		}()
		c.execute(ctx, id, req, claims, role)
	})
	return true
}

// acquireSlot takes a slot for a new operation. When every slot is held it
// waits for one only if some holder is an operation the client already
// completed (live is the number it has not), which stops on its own.
func (c *wsSession) acquireSlot(live int) bool {
	select {
	case c.slots <- struct{}{}:
		return true
	default:
	}
	if live >= cap(c.slots) {
		return false
	}
	select {
	case c.slots <- struct{}{}:
		return true
	case <-c.ctx.Done():
		return false
	}
}

// throttle delays the read loop so the connection starts at most
// server.subscriptions_subscribe_rate operations per second, in bursts of up
// to server.subscriptions_max_per_connection.
func (c *wsSession) throttle() {
	rate := float64(c.s.cfg.SubscriptionsSubscribeRate)
	if rate <= 0 {
		return
	}
	burst := float64(cap(c.slots))
	now := time.Now()
	if !c.last.IsZero() {
		c.tokens = min(burst, c.tokens+now.Sub(c.last).Seconds()*rate)
	}
	c.last = now
	if c.tokens >= 1 {
		c.tokens--
		return
	}
	wait := time.Duration((1 - c.tokens) / rate * float64(time.Second))
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
	case <-c.ctx.Done():
	}
	c.last = time.Now()
	c.tokens = 0
}

// reserveOperation counts an operation against
// server.subscriptions_max_operations; false when none is left.
func (s *Server) reserveOperation() bool {
	if s.ws.ops.Add(1) > int64(s.cfg.SubscriptionsMaxOperations) {
		s.ws.ops.Add(-1)
		return false
	}
	return true
}

func (s *Server) releaseOperation() { s.ws.ops.Add(-1) }

// stop cancels operation id; the client already considers it complete, so
// nothing more is sent for it.
func (c *wsSession) stop(id string) {
	c.mu.Lock()
	op := c.ops[id]
	delete(c.ops, id)
	c.mu.Unlock()
	if op != nil {
		c.wmu.Lock()
		op.cancel()
		c.wmu.Unlock()
	}
}

// execute runs one operation: a query or mutation yields one result, a
// subscription one result per event until it ends.
func (c *wsSession) execute(ctx context.Context, id string, req gqlHTTPRequest, claims map[string]any, role string) {
	if perr := c.s.applyPersisted(&req); perr != nil {
		c.sendErrors(ctx, id, gqlerror.List{perr.err})
		return
	}
	if req.Query == "" {
		c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("missing query")})
		return
	}
	ex := c.s.executor.Load()
	pctx, op, err := ex.Prepare(ctx, exec.Request{
		Query:         req.Query,
		OperationName: req.OperationName,
		Variables:     req.Variables,
		Claims:        claims,
		Role:          role,
	})
	if err != nil {
		c.sendErrors(ctx, id, errorList(err))
		return
	}
	if op.Type != ast.Subscription {
		if t := c.s.cfg.RequestTimeout; t > 0 {
			var cancel context.CancelFunc
			pctx, cancel = context.WithTimeout(pctx, t)
			defer cancel()
		}
		if c.s.cfg.IncrementalDelivery {
			_ = ex.ExecuteIncremental(pctx, op, exec.IncrementalV02, func(body []byte, _ bool) error {
				c.sendPayload(ctx, id, body)
				return ctx.Err()
			})
		} else {
			c.sendNext(ctx, id, ex.ExecutePrepared(pctx, op))
		}
		c.sendComplete(ctx, id)
		return
	}
	st, err := c.s.startListen(pctx, ex, op)
	if err != nil {
		if ctx.Err() == nil {
			c.sendErrors(ctx, id, errorList(err))
		}
		return
	}
	defer st.Close()
	for {
		res, err := st.Next(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, io.EOF):
			c.sendComplete(ctx, id)
			return
		case err != nil:
			c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("%s", err.Error())})
			return
		}
		if c.expired() {
			c.close(closeForbidden, "Forbidden: token expired")
			return
		}
		c.sendNext(ctx, id, res)
	}
}

func errorList(err error) gqlerror.List {
	var list gqlerror.List
	if errors.As(err, &list) {
		return list
	}
	var gerr *gqlerror.Error
	if errors.As(err, &gerr) {
		return gqlerror.List{gerr}
	}
	return gqlerror.List{gqlerror.Errorf("%s", err.Error())}
}

func (c *wsSession) sendNext(ctx context.Context, id string, res *exec.Result) {
	body, err := res.AppendJSON(nil)
	if err != nil {
		c.s.log.Error("websocket: encode result", "err", err)
		body = []byte(`{"errors":[{"message":"internal error"}]}`)
	}
	c.sendPayload(ctx, id, body)
}

// sendPayload sends body, one execution result, as a next message (a data
// message on the legacy protocol).
func (c *wsSession) sendPayload(ctx context.Context, id string, body []byte) {
	typ := "next"
	if c.legacy {
		typ = "data"
	}
	c.send(ctx, frame(typ, id, body))
}

func (c *wsSession) sendComplete(ctx context.Context, id string) {
	c.send(ctx, frame("complete", id, nil))
}

// sendErrors ends operation id with request errors: an error message on
// graphql-transport-ws, a data message carrying the errors followed by
// complete on the legacy protocol (as subscriptions-transport-ws does).
func (c *wsSession) sendErrors(ctx context.Context, id string, errs gqlerror.List) {
	if c.legacy {
		c.sendNext(ctx, id, &exec.Result{Errors: errs})
		c.sendComplete(ctx, id)
		return
	}
	body, err := json.Marshal(errs)
	if err != nil {
		body = []byte(`[{"message":"internal error"}]`)
	}
	c.send(ctx, frame("error", id, body))
}

// send writes one message unless ctx (the operation, or the connection)
// has ended; a failed write tears the connection down.
func (c *wsSession) send(ctx context.Context, msg []byte) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if ctx.Err() != nil || c.ctx.Err() != nil {
		return
	}
	wctx, cancel := context.WithTimeout(context.Background(), wsWriteTimeout)
	defer cancel()
	if err := c.conn.Write(wctx, websocket.MessageText, msg); err != nil {
		c.cancel()
		_ = c.conn.CloseNow()
	}
}

// close ends the session with a close frame carrying code and reason.
// Reads and writes never use the session context: cancelling a
// coder/websocket read or write drops the connection without a close frame.
func (c *wsSession) close(code websocket.StatusCode, reason string) {
	_ = c.conn.Close(code, reason)
	c.cancel()
}

func frame(typ, id string, payload []byte) []byte {
	b := []byte(`{`)
	if id != "" {
		b = append(b, `"id":`...)
		idJSON, _ := json.Marshal(id)
		b = append(b, idJSON...)
		b = append(b, ',')
	}
	b = append(b, `"type":"`...)
	b = append(b, typ...)
	b = append(b, '"')
	if payload != nil {
		b = append(b, `,"payload":`...)
		b = append(b, payload...)
	}
	return append(b, '}')
}

func legacyMessage(typ, id string, payload any) []byte {
	body, _ := json.Marshal(payload)
	return frame(typ, id, body)
}

func legacyError(id, msg string) []byte {
	return legacyMessage("error", id, map[string]string{"message": msg})
}

// closeReason truncates reason to the 123 bytes a close frame allows.
func closeReason(reason string) string {
	if len(reason) <= 123 {
		return reason
	}
	return reason[:123]
}
