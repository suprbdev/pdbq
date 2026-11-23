package server

import (
	"context"
	"errors"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/live"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

// SetLive makes the server answer live queries with tracker. It has an
// effect only with live.enabled on; call it before Handler. The caller owns
// tracker and closes it after the server, before the hub.
func (s *Server) SetLive(tracker *live.Tracker) { s.live = tracker }

// liveFields reports whether op is a live query, a subscription selecting
// a Query field repeated on the Subscription root or a query operation
// marked with the live directive, and returns its root fields bound to
// the Query type.
func (s *Server) liveFields(ex *exec.Executor, op *exec.Operation) ([]*ast.Field, bool) {
	if !s.cfg.Live.Enabled || ex.Built.Schema.Subscription == nil {
		return nil, false
	}
	roots := exec.CollectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars)
	switch op.Type {
	case ast.Query:
		return roots, op.Definition.Directives.ForName("live") != nil
	case ast.Subscription:
		query := ex.Built.Schema.Query
		sub := ex.Built.Schema.Subscription.Name
		out := make([]*ast.Field, 0, len(roots))
		for _, f := range roots {
			m := ex.Built.Meta[sub][f.Name]
			def := query.Fields.ForName(f.Name)
			if m == nil || def == nil || m.Kind == schema.KindListen {
				return nil, false
			}
			cp := *f
			cp.Definition, cp.ObjectDefinition = def, query
			out = append(out, &cp)
		}
		return out, len(out) > 0
	}
	return nil, false
}

// executeLive serves a live query: it finds the tables the operation reads,
// watches them, and sends the first result and then each changed one,
// until ctx ends.
func (c *wsSession) executeLive(ctx, pctx context.Context, id string, ex *exec.Executor, op *exec.Operation, fields []*ast.Field) {
	s := c.s
	if s.live == nil {
		c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("live queries are not enabled")})
		return
	}
	if !c.reserveLive() {
		s.live.Rejected()
		c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("too many live queries on this connection (limit %d)", s.cfg.Live.MaxPerConnection)})
		return
	}
	defer c.releaseLive()
	if !s.reserveLiveOperation() {
		s.live.Rejected()
		c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("too many live queries on this server; try again later")})
		return
	}
	defer s.releaseLiveOperation()

	tctx, cancel := c.withTimeout(pctx)
	tables, err := ex.ReferencedTables(tctx, op, fields)
	cancel()
	if err != nil {
		if ctx.Err() == nil {
			c.sendErrors(ctx, id, errorList(err))
		}
		return
	}
	w, err := s.live.Watch(pctx, tables)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Warn("live: listen failed", "err", err)
			c.sendErrors(ctx, id, gqlerror.List{errSubscriptionsUnavailable})
		}
		return
	}
	defer w.Close()
	stream := s.live.Stream(w, func(rctx context.Context) ([]byte, bool) {
		rctx, cancel := c.withTimeout(rctx)
		defer cancel()
		res := ex.ExecuteFields(rctx, op, fields)
		body, err := res.AppendJSON(nil)
		if err != nil {
			s.log.Error("websocket: encode result", "err", err)
			return []byte(`{"errors":[{"message":"internal error"}]}`), true
		}
		return body, len(res.Errors) > 0
	})
	for {
		body, err := stream.Next(pctx)
		switch {
		case ctx.Err() != nil:
			return
		case errors.Is(err, subscribe.ErrClosed):
			c.sendErrors(ctx, id, gqlerror.List{gqlerror.Errorf("%s", err.Error())})
			return
		case err != nil:
			c.sendErrors(ctx, id, errorList(err))
			return
		}
		if c.expired() {
			c.close(closeForbidden, "Forbidden: token expired")
			return
		}
		c.sendPayload(ctx, id, body)
	}
}

// withTimeout bounds ctx by server.request_timeout, when set.
func (c *wsSession) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if t := c.s.cfg.Server.RequestTimeout; t > 0 {
		return context.WithTimeout(ctx, t)
	}
	return context.WithCancel(ctx)
}

// reserveLive counts a live query against
// live.max_per_connection; false when none is left.
func (c *wsSession) reserveLive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live >= c.s.cfg.Live.MaxPerConnection {
		return false
	}
	c.live++
	return true
}

func (c *wsSession) releaseLive() {
	c.mu.Lock()
	c.live--
	c.mu.Unlock()
}

// reserveLiveOperation counts a live query against
// live.max_operations; false when none is left.
func (s *Server) reserveLiveOperation() bool {
	if s.ws.live.Add(1) > int64(s.cfg.Live.MaxOperations) {
		s.ws.live.Add(-1)
		return false
	}
	return true
}

func (s *Server) releaseLiveOperation() { s.ws.live.Add(-1) }
