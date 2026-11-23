package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbq/internal/exec"
	"github.com/suprbdev/pdbq/internal/schema"
	"github.com/suprbdev/pdbq/internal/subscribe"
)

// errSubscriptionsUnavailable is what a client sees when the LISTEN
// connection cannot be established; the cause is logged.
var errSubscriptionsUnavailable = gqlerror.Errorf("subscriptions are temporarily unavailable")

// listenStream is one running Subscription.listen operation: its prepared
// operation, the listen field, and the hub listener feeding it.
type listenStream struct {
	ex      *exec.Executor
	op      *exec.Operation
	ctx     context.Context
	field   *ast.Field
	l       *subscribe.Listener
	initial bool
	// timeout bounds the resolution of one event (server.request_timeout).
	timeout time.Duration
}

// startListen validates the listen field of a prepared subscription
// operation, maps its topic to a channel through the topic policy and
// waits until the hub LISTENs on it.
func (s *Server) startListen(ctx context.Context, ex *exec.Executor, op *exec.Operation) (*listenStream, error) {
	if s.hub == nil || ex.Built.Schema.Subscription == nil {
		return nil, gqlerror.Errorf("subscriptions are not enabled")
	}
	roots := exec.CollectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars)
	if len(roots) != 1 {
		return nil, gqlerror.Errorf("a subscription must select exactly one root field")
	}
	f := roots[0]
	if m := ex.Built.Meta[ex.Built.Schema.Subscription.Name][f.Name]; m == nil || m.Kind != schema.KindListen {
		return nil, gqlerror.Errorf("subscription field %q is not supported", f.Name)
	}
	args := f.ArgumentMap(op.Vars)
	topic, _ := args["topic"].(string)
	initial, _ := args["initialEvent"].(bool)
	channel, err := s.topics.Channel(topic)
	if err != nil {
		return nil, gqlerror.Errorf("%s", err.Error())
	}
	l, err := s.hub.Subscribe(ctx, channel, s.cfg.SubscriptionsBuffer)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, subscribe.ErrBusy) {
			return nil, gqlerror.Errorf("%s", err.Error())
		}
		s.log.Warn("subscriptions: listen failed", "channel", channel, "err", err)
		return nil, errSubscriptionsUnavailable
	}
	return &listenStream{ex: ex, op: op, ctx: ctx, field: f, l: l, initial: initial, timeout: s.cfg.RequestTimeout}, nil
}

// Close releases the stream's channel.
func (st *listenStream) Close() { st.l.Close() }

// Next blocks for the next event and returns its response: the initial
// event first when initialEvent was requested, then one per notification.
// It returns io.EOF when the stream ended normally and the hub's error
// (subscribe.ErrOverflow, subscribe.ErrClosed) when the hub ended it.
func (st *listenStream) Next(ctx context.Context) (*exec.Result, error) {
	if st.initial {
		st.initial = false
		return st.resolve(nil), nil
	}
	select {
	case p, ok := <-st.l.Events():
		if !ok {
			if err := st.l.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		return st.resolve(&p), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// listenPart is one response key of the listen payload selection.
type listenPart struct {
	key  string
	kind schema.FieldKind
	// field is the relatedNode field, merged across repeated selections.
	field *ast.Field
	// children are the query field's selections by response key, merged
	// across repeated selections, in selection order.
	children []*ast.Field
}

// resolve builds the response for one event (payload nil for the initial
// event). relatedNodeId is computed in Go; relatedNode and the query
// selections run as Query root fields in one ExecuteFields round trip under
// the subscriber's identity.
func (st *listenStream) resolve(payload *string) *exec.Result {
	frags, vars := st.op.Document.Fragments, st.op.Vars
	nodeID := relatedNodeID(payload)
	typeName := st.field.Definition.Type.Name()
	meta := st.ex.Built.Meta[typeName]

	var parts []*listenPart
	byKey := map[string]*listenPart{}
	for _, f := range exec.CollectRootFields(st.field.SelectionSet, frags, vars) {
		p := byKey[f.Alias]
		if p == nil {
			p = &listenPart{key: f.Alias}
			if f.Name == "__typename" {
				p.kind = schema.KindSynthetic
			} else if m := meta[f.Name]; m != nil {
				p.kind = m.Kind
			}
			byKey[f.Alias] = p
			parts = append(parts, p)
		}
		switch p.kind {
		case schema.KindListenRelatedNode:
			p.field = mergeField(p.field, f)
		case schema.KindListenQuery:
			for _, c := range exec.CollectRootFields(f.SelectionSet, frags, vars) {
				i := slices.IndexFunc(p.children, func(x *ast.Field) bool { return x.Alias == c.Alias })
				if i < 0 {
					p.children = append(p.children, c)
				} else {
					p.children[i] = mergeField(p.children[i], c)
				}
			}
		}
	}

	var fields []*ast.Field
	paths := map[string]ast.Path{}
	listenKey := ast.PathName(st.field.Alias)
	add := func(f *ast.Field, path ast.Path) string {
		alias := fmt.Sprintf("_%d", len(fields))
		cp := *f
		cp.Alias = alias
		fields = append(fields, &cp)
		paths[alias] = path
		return alias
	}
	nodeAliases := map[*listenPart]string{}
	childAliases := map[*ast.Field]string{}
	for _, p := range parts {
		switch p.kind {
		case schema.KindListenRelatedNode:
			if nodeID != "" {
				if nf := st.nodeField(p.field, nodeID); nf != nil {
					nodeAliases[p] = add(nf, ast.Path{listenKey, ast.PathName(p.key)})
				}
			}
		case schema.KindListenQuery:
			for _, c := range p.children {
				childAliases[c] = add(c, ast.Path{listenKey, ast.PathName(p.key), ast.PathName(c.Alias)})
			}
		}
	}

	res := &exec.Result{Data: map[string]json.RawMessage{}}
	if len(fields) > 0 {
		ctx := st.ctx
		if st.timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, st.timeout)
			defer cancel()
		}
		res = st.ex.ExecuteFields(ctx, st.op, fields)
	}
	value := func(alias string) json.RawMessage {
		if raw, ok := res.Get(alias); ok && len(raw) > 0 {
			return raw
		}
		return json.RawMessage("null")
	}

	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range parts {
		if i > 0 {
			b.WriteByte(',')
		}
		writeJSONString(&b, p.key)
		b.WriteByte(':')
		switch p.kind {
		case schema.KindSynthetic:
			writeJSONString(&b, typeName)
		case schema.KindListenRelatedNodeID:
			if nodeID == "" {
				b.WriteString("null")
			} else {
				writeJSONString(&b, nodeID)
			}
		case schema.KindListenRelatedNode:
			if alias, ok := nodeAliases[p]; ok {
				b.Write(value(alias))
			} else {
				b.WriteString("null")
			}
		case schema.KindListenQuery:
			b.WriteByte('{')
			for j, c := range p.children {
				if j > 0 {
					b.WriteByte(',')
				}
				writeJSONString(&b, c.Alias)
				b.WriteByte(':')
				b.Write(value(childAliases[c]))
			}
			b.WriteByte('}')
		default:
			b.WriteString("null")
		}
	}
	b.WriteByte('}')

	out := &exec.Result{}
	out.Set(st.field.Alias, b.Bytes())
	for _, e := range res.Errors {
		cp := *e
		if len(cp.Path) > 0 {
			if name, ok := cp.Path[0].(ast.PathName); ok {
				if prefix, ok := paths[string(name)]; ok {
					cp.Path = append(slices.Clone(prefix), cp.Path[1:]...)
				}
			}
		}
		out.Errors = append(out.Errors, &cp)
	}
	return out
}

// nodeField turns a relatedNode selection into the Query root field that
// resolves it: Query.node with the event's nodeId as a literal argument.
func (st *listenStream) nodeField(f *ast.Field, nodeID string) *ast.Field {
	query := st.ex.Built.Schema.Query
	var def *ast.FieldDefinition
	for _, fd := range query.Fields {
		if m := st.ex.Built.Meta[query.Name][fd.Name]; m != nil && m.Kind == schema.KindNode {
			def = fd
			break
		}
	}
	if def == nil {
		return nil
	}
	return &ast.Field{
		Alias:            f.Alias,
		Name:             def.Name,
		Arguments:        ast.ArgumentList{{Name: "nodeId", Value: &ast.Value{Kind: ast.StringValue, Raw: nodeID}}},
		Directives:       f.Directives,
		SelectionSet:     f.SelectionSet,
		Position:         f.Position,
		Definition:       def,
		ObjectDefinition: query,
	}
}

// mergeField merges a repeated selection of the same response key into
// prev, concatenating their selection sets as GraphQL field merging does.
func mergeField(prev, f *ast.Field) *ast.Field {
	if prev == nil {
		return f
	}
	cp := *prev
	cp.SelectionSet = append(slices.Clone(prev.SelectionSet), f.SelectionSet...)
	return &cp
}

// relatedNodeID encodes the payload's __node__ as a nodeId, or returns ""
// when the payload is absent, not a JSON object or has no __node__. An
// array is rendered as PostgreSQL renders jsonb (["Post", 1]) so the ID
// equals the nodeId pdbq returns for that row; anything else is base64 of
// its compact JSON, as PostGraphile does.
func relatedNodeID(payload *string) string {
	if payload == nil {
		return ""
	}
	var msg struct {
		Node json.RawMessage `json:"__node__"`
	}
	if json.Unmarshal([]byte(*payload), &msg) != nil || len(msg.Node) == 0 || string(msg.Node) == "null" {
		return ""
	}
	var items []json.RawMessage
	var b bytes.Buffer
	if json.Unmarshal(msg.Node, &items) == nil {
		b.WriteByte('[')
		for i, it := range items {
			if i > 0 {
				b.WriteString(", ")
			}
			_ = json.Compact(&b, it)
		}
		b.WriteByte(']')
	} else {
		_ = json.Compact(&b, msg.Node)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b.Truncate(b.Len() - 1)
}
