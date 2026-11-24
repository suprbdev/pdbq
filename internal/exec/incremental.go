package exec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/errcode"
)

// Incremental delivery defaults (see IncrementalOptions).
const (
	DefaultMaxDeferred     = 16
	DefaultStreamBatchSize = 10
)

// IncrementalOptions configure the defer and stream directives
// (incremental.enabled).
type IncrementalOptions struct {
	// Enabled plans query operations for incremental delivery and enforces
	// the directives' validation rules. The schema must declare the
	// directives (schema.Options.IncrementalDelivery).
	Enabled bool
	// MaxDeferred bounds the deferred fragments and streamed fields of one
	// operation; 0 means DefaultMaxDeferred.
	MaxDeferred int
	// StreamBatchSize is the number of items each subsequent payload of a
	// streamed list carries; 0 means DefaultStreamBatchSize.
	StreamBatchSize int
}

// IncrementalFormat is the payload shape of an incremental response.
type IncrementalFormat int

const (
	// IncrementalV02 is the shape of graphql-js 17 and the incremental
	// delivery RFC (HTTP incrementalSpec=v0.2): the initial payload
	// announces each deferred fragment and stream under an id in pending,
	// later payloads carry incremental entries by id and completed entries,
	// and every payload has hasNext.
	IncrementalV02 IncrementalFormat = iota
	// IncrementalDefer20220824 is the earlier shape (HTTP
	// deferSpec=20220824, graphql-js 17.0.0-alpha.2): incremental entries
	// carry their path and label, and there is no pending or completed.
	IncrementalDefer20220824
)

// Emit receives the payloads of an operation in order. incremental is false
// when the operation is answered by a single result, which body then holds
// whole; otherwise every payload is a part of an incremental response, and
// the last one has hasNext false.
type Emit func(body []byte, incremental bool) error

// errIncrementalTimeout completes the deferred fragments and streams still
// pending when the operation's context ends.
var errIncrementalTimeout = errcode.Errorf(errcode.Timeout, "request timeout")

// ExecuteIncremental executes an operation returned by Prepare and delivers
// its result through emit. A query whose deferred fragments or streamed
// lists the executor delivers incrementally runs all its statements in one
// transaction, at the repeatable read isolation level (serializable when
// transactions.isolation asks for it), so every payload reads the same
// snapshot; other operations are answered by one result, with the defer and
// stream directives ignored. The AfterOperation hooks receive the result of
// the initial payload. The error is emit's, or the context's when it ended
// the delivery.
func (e *Executor) ExecuteIncremental(ctx context.Context, op *Operation, format IncrementalFormat, emit Emit) error {
	if op.inc == nil || op.Type != ast.Query {
		res := e.ExecutePrepared(ctx, op)
		body, err := res.AppendJSON(nil)
		if err != nil {
			return err
		}
		return emit(body, false)
	}
	x := &incRun{e: e, ctx: ctx, op: op, plan: op.inc, format: format, emit: emit, open: map[string]bool{}}
	opts, id := e.txOptions(op)
	opts.Isolation = pgx.RepeatableRead
	if e.Opts.Isolation == pgx.Serializable {
		opts.Isolation = pgx.Serializable
	}
	opts.MaxRetries = 0
	runTx := e.runTx
	if runTx == nil {
		runTx = pgexec.RunTx
	}
	err := runTx(ctx, e.Pool, opts, id, x.deliver)
	if !x.started {
		res := &Result{}
		if err != nil {
			res = errResult(e.gqlError(err, nil))
		}
		for _, h := range e.Hooks {
			h.AfterOperation(ctx, op, res)
		}
		body, merr := res.AppendJSON(nil)
		if merr != nil {
			return merr
		}
		return emit(body, false)
	}
	for _, h := range e.Hooks {
		h.AfterOperation(ctx, op, x.initial)
	}
	if x.emitErr == nil && !x.done {
		x.abort(err)
		if x.emitErr == nil {
			return err
		}
	}
	return x.emitErr
}

// incKind is what an incremental record delivers.
type incKind uint8

const (
	deferKind incKind = iota
	streamKind
)

// pathElem is one step of a response path template: a response key, or
// (list) every item of a list.
type pathElem struct {
	key  string
	list bool
}

// incRecord is one deferred fragment or streamed field of an operation.
type incRecord struct {
	kind  incKind
	label string
	// id identifies a streamed field's window (compile.StreamWindow).
	id int
	// parent is the record whose payload delivers this record's position,
	// nil for the initial payload.
	parent   *incRecord
	children []*incRecord
	// steps are the planned selections from the operation root down to the
	// deferred inline fragment or the streamed field, both included.
	steps []ast.Selection
	// tmpl is the response path of the fragment's object or the streamed
	// list, with a list element per list level crossed.
	tmpl         []pathElem
	initialCount int
}

// incPlan is the incremental delivery plan of an operation: its selections
// with fragment spreads expanded into inline fragments, so that every
// selection position has its own node, and the records found in them.
type incPlan struct {
	sel     ast.SelectionSet
	records []*incRecord
	roots   []*incRecord
	of      map[ast.Selection]*incRecord
}

// usesIncremental reports whether the operation or a fragment of doc uses
// the defer or stream directive.
func usesIncremental(doc *ast.QueryDocument, op *ast.OperationDefinition) bool {
	var walk func(sel ast.SelectionSet) bool
	walk = func(sel ast.SelectionSet) bool {
		for _, item := range sel {
			switch v := item.(type) {
			case *ast.Field:
				if v.Directives.ForName("stream") != nil || walk(v.SelectionSet) {
					return true
				}
			case *ast.InlineFragment:
				if v.Directives.ForName("defer") != nil || walk(v.SelectionSet) {
					return true
				}
			case *ast.FragmentSpread:
				if v.Directives.ForName("defer") != nil {
					return true
				}
			}
		}
		return false
	}
	if walk(op.SelectionSet) {
		return true
	}
	for _, f := range doc.Fragments {
		if walk(f.SelectionSet) {
			return true
		}
	}
	return false
}

// planIncremental validates the defer and stream directives of op and plans
// its incremental delivery; the plan is nil when nothing is delivered
// incrementally.
func (e *Executor) planIncremental(doc *ast.QueryDocument, op *ast.OperationDefinition, vars map[string]any) (*incPlan, error) {
	if !usesIncremental(doc, op) {
		return nil, nil
	}
	p := &planner{
		schema: e.Built.Schema,
		frags:  doc.Fragments,
		vars:   vars,
		max:    orDefault(e.Opts.Incremental.MaxDeferred, DefaultMaxDeferred),
		labels: map[string]*ast.Directive{},
		plan:   &incPlan{of: map[ast.Selection]*incRecord{}},
	}
	sel, err := p.selection(op.SelectionSet, walkCtx{exact: true}, true)
	if err != nil {
		return nil, err
	}
	if len(p.plan.records) == 0 || op.Operation != ast.Query {
		return nil, nil
	}
	p.plan.sel = sel
	return p.plan, nil
}

type planner struct {
	schema *ast.Schema
	frags  ast.FragmentDefinitionList
	vars   map[string]any
	max    int
	labels map[string]*ast.Directive
	plan   *incPlan
}

// walkCtx is the position of a selection in the planner's walk.
type walkCtx struct {
	parent *incRecord
	steps  []ast.Selection
	tmpl   []pathElem
	// exact is true while every field above has an object type, so a
	// fragment's type condition holds for every object at the position.
	exact bool
	// inStream is true below a streamed field, whose items are delivered
	// whole.
	inStream bool
}

func (c walkCtx) below(sel ast.Selection) walkCtx {
	c.steps = append(slices.Clip(c.steps), sel)
	return c
}

// selection plans sel, returning it with fragment spreads expanded and the
// selections excluded by skip or include left out. root is true at the
// operation's root, whose introspection fields are kept as they are.
func (p *planner) selection(sel ast.SelectionSet, c walkCtx, root bool) (ast.SelectionSet, error) {
	out := make(ast.SelectionSet, 0, len(sel))
	for _, item := range sel {
		var planned ast.Selection
		var err error
		switch v := item.(type) {
		case *ast.Field:
			if compile.SkipByDirectives(v.Directives, p.vars) {
				continue
			}
			if root && isIntrospectionField(v.Name) {
				planned = v
				break
			}
			planned, err = p.field(v, c)
		case *ast.InlineFragment:
			if compile.SkipByDirectives(v.Directives, p.vars) {
				continue
			}
			planned, err = p.fragment(v.TypeCondition, v.Directives, v.SelectionSet, v.ObjectDefinition, v.Position, c, root)
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, p.vars) {
				continue
			}
			def := p.frags.ForName(v.Name)
			if def == nil {
				continue
			}
			planned, err = p.fragment(def.TypeCondition, v.Directives, def.SelectionSet, def.Definition, v.Position, c, root)
		}
		if err != nil {
			return nil, err
		}
		if planned != nil {
			out = append(out, planned)
		}
	}
	return out, nil
}

func (p *planner) field(f *ast.Field, c walkCtx) (ast.Selection, error) {
	nf := *f
	if f.Definition == nil {
		return &nf, nil
	}
	lists := 0
	for t := f.Definition.Type; t.Elem != nil; t = t.Elem {
		lists++
	}
	child := c.below(&nf)
	child.tmpl = append(slices.Clip(c.tmpl), pathElem{key: f.Alias})
	if d := f.Directives.ForName("stream"); d != nil {
		on, label, initial, err := p.streamArgs(d, f, lists)
		if err != nil {
			return nil, err
		}
		if on && c.exact && !c.inStream && !hasList(c.tmpl) {
			rec, err := p.add(streamKind, label, c.parent, child.steps, child.tmpl)
			if err != nil {
				return nil, err
			}
			rec.initialCount = initial
			child.inStream = true
		}
	}
	for range lists {
		child.tmpl = append(child.tmpl, pathElem{list: true})
	}
	named := p.schema.Types[f.Definition.Type.Name()]
	child.exact = c.exact && named != nil && named.Kind == ast.Object
	sel, err := p.selection(f.SelectionSet, child, false)
	if err != nil {
		return nil, err
	}
	nf.SelectionSet = sel
	return &nf, nil
}

func (p *planner) fragment(cond string, dirs ast.DirectiveList, sel ast.SelectionSet, def *ast.Definition, pos *ast.Position, c walkCtx, root bool) (ast.Selection, error) {
	nf := &ast.InlineFragment{TypeCondition: cond, Directives: dirs, ObjectDefinition: def, Position: pos}
	child := c.below(nf)
	if d := dirs.ForName("defer"); d != nil {
		on, label, err := p.deferArgs(d)
		if err != nil {
			return nil, err
		}
		if on && c.exact && !c.inStream {
			rec, err := p.add(deferKind, label, c.parent, child.steps, slices.Clone(c.tmpl))
			if err != nil {
				return nil, err
			}
			child.parent = rec
		}
	}
	planned, err := p.selection(sel, child, root)
	if err != nil {
		return nil, err
	}
	nf.SelectionSet = planned
	return nf, nil
}

func (p *planner) add(kind incKind, label string, parent *incRecord, steps []ast.Selection, tmpl []pathElem) (*incRecord, error) {
	if len(p.plan.records) >= p.max {
		return nil, errcode.Errorf(errcode.DocumentLimitExceeded, "operation defers or streams more than %d selections", p.max)
	}
	rec := &incRecord{kind: kind, label: label, id: len(p.plan.records), parent: parent, steps: steps, tmpl: tmpl}
	p.plan.records = append(p.plan.records, rec)
	p.plan.of[steps[len(steps)-1]] = rec
	if parent == nil {
		p.plan.roots = append(p.plan.roots, rec)
	} else {
		parent.children = append(parent.children, rec)
	}
	return rec, nil
}

func hasList(tmpl []pathElem) bool {
	for _, e := range tmpl {
		if e.list {
			return true
		}
	}
	return false
}

// deferArgs evaluates a defer directive: whether it applies, and its label.
func (p *planner) deferArgs(d *ast.Directive) (bool, string, error) {
	label, err := p.label(d)
	if err != nil {
		return false, "", err
	}
	return p.ifArg(d), label, nil
}

// streamArgs evaluates the stream directive of f, a field lists list levels
// deep: whether it applies, its label and its initialCount.
func (p *planner) streamArgs(d *ast.Directive, f *ast.Field, lists int) (bool, string, int, error) {
	if lists == 0 {
		parent := ""
		if f.ObjectDefinition != nil {
			parent = f.ObjectDefinition.Name + "."
		}
		return false, "", 0, errcode.With(gqlerror.ErrorPosf(d.Position, `Directive "@stream" cannot be used on non-list field "%s%s".`, parent, f.Name), errcode.GraphQLValidationFailed)
	}
	label, err := p.label(d)
	if err != nil {
		return false, "", 0, err
	}
	initial := 0
	if a := d.Arguments.ForName("initialCount"); a != nil {
		v, err := a.Value.Value(p.vars)
		if err != nil {
			return false, "", 0, err
		}
		if n, ok := v.(int64); ok {
			if n < 0 {
				return false, "", 0, errcode.With(gqlerror.ErrorPosf(d.Position, "initialCount must be a positive integer"), errcode.GraphQLValidationFailed)
			}
			initial = int(min(n, 1<<31))
		}
	}
	return p.ifArg(d), label, initial, nil
}

// ifArg evaluates the if argument of a defer or stream directive, true when
// absent.
func (p *planner) ifArg(d *ast.Directive) bool {
	a := d.Arguments.ForName("if")
	if a == nil {
		return true
	}
	v, err := a.Value.Value(p.vars)
	if err != nil {
		return true
	}
	b, ok := v.(bool)
	return !ok || b
}

// label returns the label of a defer or stream directive, which must be a
// literal string no other such directive of the document uses.
func (p *planner) label(d *ast.Directive) (string, error) {
	a := d.Arguments.ForName("label")
	if a == nil || a.Value.Kind == ast.NullValue {
		return "", nil
	}
	if a.Value.Kind != ast.StringValue && a.Value.Kind != ast.BlockValue {
		return "", errcode.With(gqlerror.ErrorPosf(a.Position, `Argument "label" on directive "@%s" must be a static string.`, d.Name), errcode.GraphQLValidationFailed)
	}
	label := a.Value.Raw
	if prev, ok := p.labels[label]; ok && prev != d {
		return "", errcode.With(gqlerror.ErrorPosf(a.Position, "Defer/Stream directive label argument must be unique."), errcode.GraphQLValidationFailed)
	}
	p.labels[label] = d
	return label, nil
}

// strip returns sel without the deferred fragments of the plan, and with
// its streamed fields windowed to their initial items. sel itself is
// returned when nothing changed.
func (pl *incPlan) strip(sel ast.SelectionSet) ast.SelectionSet {
	var out ast.SelectionSet
	for i, item := range sel {
		repl := item
		rec := pl.of[item]
		switch v := item.(type) {
		case *ast.InlineFragment:
			if rec != nil {
				repl = nil
			} else if sub := pl.strip(v.SelectionSet); len(sub) == 0 {
				repl = nil
			} else if !sameSelection(sub, v.SelectionSet) {
				c := *v
				c.SelectionSet = sub
				repl = &c
			}
		case *ast.Field:
			if rec != nil {
				repl = compile.StreamWindow(v, rec.id, 0, rec.initialCount)
			} else if sub := pl.strip(v.SelectionSet); !sameSelection(sub, v.SelectionSet) {
				c := *v
				c.SelectionSet = sub
				repl = &c
			}
		}
		if repl == item && out == nil {
			continue
		}
		if out == nil {
			out = append(make(ast.SelectionSet, 0, len(sel)), sel[:i]...)
		}
		if repl != nil {
			out = append(out, repl)
		}
	}
	if out == nil {
		return sel
	}
	return out
}

func sameSelection(a, b ast.SelectionSet) bool {
	return len(a) == len(b) && (len(a) == 0 || &a[0] == &b[0])
}

// prune returns the root selection set that reaches leaf, the last of steps
// replaced, through the other steps alone.
func prune(steps []ast.Selection, leaf ast.Selection) ast.SelectionSet {
	cur := leaf
	for i := len(steps) - 2; i >= 0; i-- {
		switch v := steps[i].(type) {
		case *ast.Field:
			c := *v
			c.SelectionSet = ast.SelectionSet{cur}
			cur = &c
		case *ast.InlineFragment:
			c := *v
			c.SelectionSet = ast.SelectionSet{cur}
			cur = &c
		}
	}
	return ast.SelectionSet{cur}
}

// deferSelection is the root selection set of a deferred fragment's
// statements.
func (pl *incPlan) deferSelection(rec *incRecord) ast.SelectionSet {
	frag := rec.steps[len(rec.steps)-1].(*ast.InlineFragment)
	leaf := *frag
	leaf.SelectionSet = pl.strip(frag.SelectionSet)
	return prune(rec.steps, &leaf)
}

// streamSelection is the root selection set of the statements reading
// count items of a streamed list from position start.
func streamSelection(rec *incRecord, start, count int) ast.SelectionSet {
	f := rec.steps[len(rec.steps)-1].(*ast.Field)
	return prune(rec.steps, compile.StreamWindow(f, rec.id, start, count))
}

// incPending is one announced instance of a record: a deferred fragment at
// one object, or a streamed list.
type incPending struct {
	id   string
	rec  *incRecord
	path []any
}

// incQueued is a record waiting to be delivered, with its instances.
type incQueued struct {
	rec  *incRecord
	inst []incPending
	// next is the position of the next item a stream delivers.
	next int
}

type incEntry struct {
	p      incPending
	data   json.RawMessage
	items  []json.RawMessage
	start  int
	stream bool
	errors gqlerror.List
}

type incDone struct {
	p      incPending
	start  int
	errors gqlerror.List
}

// incPayload is one payload of an incremental response; initial is set for
// the first.
type incPayload struct {
	initial     *Result
	pending     []incPending
	incremental []incEntry
	completed   []incDone
	hasNext     bool
}

// incRun delivers one operation incrementally.
type incRun struct {
	e      *Executor
	ctx    context.Context
	op     *Operation
	plan   *incPlan
	format IncrementalFormat
	emit   Emit
	q      pgexec.Querier

	nextID  int
	open    map[string]bool
	queue   []incQueued
	initial *Result
	// started is set once the first payload was emitted, done once the
	// last was.
	started, done bool
	emitErr       error
}

// deliver is the transaction body: the initial payload, then each queued
// record in turn.
func (x *incRun) deliver(q pgexec.Querier) error {
	x.q = q
	res, streamed := x.run(x.plan.strip(x.plan.sel))
	x.initial = res
	pending := x.discover(x.plan.roots, res, streamed)
	if len(pending) == 0 {
		x.started, x.done = true, true
		body, err := res.AppendJSON(nil)
		if err != nil {
			return err
		}
		if x.emitErr = x.emit(body, false); x.emitErr != nil {
			return x.emitErr
		}
		return nil
	}
	x.queue = pending
	if err := x.send(incPayload{initial: res, pending: flatten(pending), hasNext: true}); err != nil {
		return err
	}
	for len(x.queue) > 0 {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		item := x.queue[0]
		x.queue = x.queue[1:]
		var err error
		if item.rec.kind == deferKind {
			err = x.deliverDefer(item)
		} else {
			err = x.deliverStream(item)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func flatten(qs []incQueued) []incPending {
	var out []incPending
	for _, q := range qs {
		out = append(out, q.inst...)
	}
	return out
}

func (x *incRun) deliverDefer(item incQueued) error {
	res, streamed := x.run(x.plan.deferSelection(item.rec))
	if err := x.ctx.Err(); err != nil {
		x.queue = append([]incQueued{item}, x.queue...)
		return err
	}
	found := map[string]json.RawMessage{}
	for _, l := range locate(res, item.rec.tmpl) {
		found[pathKey(l.path)] = l.raw
	}
	var p incPayload
	errs := res.Errors
	for _, inst := range item.inst {
		data, ok := found[pathKey(inst.path)]
		if !ok {
			if len(errs) == 0 {
				errs = gqlerror.List{errcode.Errorf(errcode.Internal, "deferred fragment data is missing")}
			}
			p.completed = append(p.completed, incDone{p: inst, errors: withPath(errs, inst.path)})
			continue
		}
		p.incremental = append(p.incremental, incEntry{p: inst, data: data, errors: errs})
		errs = nil
		p.completed = append(p.completed, incDone{p: inst})
	}
	children := x.discover(item.rec.children, res, streamed)
	p.pending = flatten(children)
	x.queue = append(x.queue, children...)
	p.hasNext = len(x.queue) > 0
	return x.send(p)
}

func (x *incRun) deliverStream(item incQueued) error {
	inst := item.inst[0]
	batch := orDefault(x.e.Opts.Incremental.StreamBatchSize, DefaultStreamBatchSize)
	start := item.next
	for {
		res, _ := x.run(streamSelection(item.rec, start, batch))
		if err := x.ctx.Err(); err != nil {
			item.next = start
			x.queue = append([]incQueued{item}, x.queue...)
			return err
		}
		var items []json.RawMessage
		locs := locate(res, item.rec.tmpl)
		ok := len(locs) == 1 && json.Unmarshal(locs[0].raw, &items) == nil
		var p incPayload
		switch {
		case !ok:
			errs := res.Errors
			if len(errs) == 0 {
				errs = gqlerror.List{errcode.Errorf(errcode.Internal, "streamed list is missing")}
			}
			p.completed = []incDone{{p: inst, start: start, errors: withPath(errs, inst.path)}}
		default:
			if len(items) > 0 || len(res.Errors) > 0 {
				p.incremental = []incEntry{{p: inst, items: items, start: start, stream: true, errors: res.Errors}}
			}
			if len(items) < batch {
				p.completed = []incDone{{p: inst, start: start + len(items)}}
			}
		}
		last := len(p.completed) > 0
		p.hasNext = !last || len(x.queue) > 0
		if err := x.send(p); err != nil {
			return err
		}
		if last {
			return nil
		}
		start += len(items)
	}
}

// run compiles and runs the root fields of sel as one result, and returns
// the ids of the streamed fields whose window the statements applied.
func (x *incRun) run(sel ast.SelectionSet) (*Result, map[int]bool) {
	res := &Result{Data: map[string]json.RawMessage{}}
	streamed := map[int]bool{}
	for _, f := range collectRootFields(sel, x.op.Document.Fragments, x.op.Vars) {
		var raw json.RawMessage
		var err error
		if isIntrospectionField(f.Name) {
			raw, err = x.e.resolveIntrospection(x.op, f)
		} else {
			var stmt *compile.Statement
			if stmt, err = x.e.compileField(x.ctx, x.op, f); err == nil {
				for _, id := range stmt.Streamed {
					streamed[id] = true
				}
				raw, err = x.e.queryStatement(x.ctx, x.q, x.op, f, stmt)
			}
		}
		if err != nil {
			res.Errors = append(res.Errors, x.e.gqlError(err, f))
			res.Set(f.Alias, json.RawMessage("null"))
			continue
		}
		res.Set(f.Alias, raw)
	}
	return res, streamed
}

// discover announces the instances of recs found in res, the result whose
// payload delivers their positions.
func (x *incRun) discover(recs []*incRecord, res *Result, streamed map[int]bool) []incQueued {
	var out []incQueued
	for _, r := range recs {
		if r.kind == streamKind && !streamed[r.id] {
			continue
		}
		q := incQueued{rec: r, next: r.initialCount}
		for _, l := range locate(res, r.tmpl) {
			if r.kind == streamKind {
				var items []json.RawMessage
				if json.Unmarshal(l.raw, &items) != nil || len(items) < r.initialCount {
					continue
				}
			}
			id := strconv.Itoa(x.nextID)
			x.nextID++
			x.open[id] = true
			q.inst = append(q.inst, incPending{id: id, rec: r, path: l.path})
		}
		if len(q.inst) > 0 {
			out = append(out, q)
		}
	}
	return out
}

// send encodes and emits one payload.
func (x *incRun) send(p incPayload) error {
	for _, d := range p.completed {
		delete(x.open, d.p.id)
	}
	body, err := x.encode(p)
	if err != nil {
		return err
	}
	x.started = true
	if !p.hasNext {
		x.done = true
	}
	if x.emitErr = x.emit(body, true); x.emitErr != nil {
		return x.emitErr
	}
	return nil
}

// abort ends a delivery that stopped early (the context ended, or the
// transaction failed) with a last payload completing every pending
// instance with an error.
func (x *incRun) abort(cause error) {
	gerr := errIncrementalTimeout
	if !errors.Is(cause, context.DeadlineExceeded) && x.ctx.Err() == nil {
		gerr = x.e.gqlError(cause, nil)
	}
	var p incPayload
	for _, q := range x.queue {
		for _, inst := range q.inst {
			if x.open[inst.id] {
				p.completed = append(p.completed, incDone{p: inst, start: q.next, errors: withPath(gqlerror.List{gerr}, inst.path)})
			}
		}
	}
	_ = x.send(p)
}

// located is a value found at a concrete response path.
type located struct {
	path []any
	raw  json.RawMessage
}

// locate returns the non-null values of res's data at the positions tmpl
// describes.
func locate(res *Result, tmpl []pathElem) []located {
	var out []located
	if len(tmpl) == 0 {
		b, err := res.appendData(nil)
		if err != nil {
			return nil
		}
		return []located{{path: []any{}, raw: b}}
	}
	raw, ok := res.Data[tmpl[0].key]
	if !ok {
		return nil
	}
	walkPath(raw, tmpl[1:], []any{tmpl[0].key}, &out)
	return out
}

func walkPath(raw json.RawMessage, tmpl []pathElem, path []any, out *[]located) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return
	}
	if len(tmpl) == 0 {
		*out = append(*out, located{path: slices.Clone(path), raw: raw})
		return
	}
	if tmpl[0].list {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil {
			return
		}
		for i, it := range items {
			walkPath(it, tmpl[1:], append(path, i), out)
		}
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	if v, ok := obj[tmpl[0].key]; ok {
		walkPath(v, tmpl[1:], append(path, tmpl[0].key), out)
	}
}

func pathKey(path []any) string {
	b, _ := json.Marshal(path)
	return string(b)
}

// withPath returns copies of errs located at path.
func withPath(errs gqlerror.List, path []any) gqlerror.List {
	p := make(ast.Path, len(path))
	for i, el := range path {
		switch v := el.(type) {
		case string:
			p[i] = ast.PathName(v)
		case int:
			p[i] = ast.PathIndex(v)
		}
	}
	out := make(gqlerror.List, len(errs))
	for i, err := range errs {
		c := *err
		c.Path = p
		out[i] = &c
	}
	return out
}

// encode renders p in the run's format.
func (x *incRun) encode(p incPayload) ([]byte, error) {
	var b []byte
	if p.initial != nil {
		var err error
		if b, err = p.initial.AppendJSON(nil); err != nil {
			return nil, err
		}
		b = b[:len(b)-1]
		if x.format == IncrementalV02 {
			b = append(b, `,"pending":[`...)
			for i, inst := range p.pending {
				if i > 0 {
					b = append(b, ',')
				}
				if b, err = appendPending(b, inst); err != nil {
					return nil, err
				}
			}
			b = append(b, ']')
		}
		return append(b, `,"hasNext":true}`...), nil
	}
	b = append(b, `{"hasNext":`...)
	b = strconv.AppendBool(b, p.hasNext)
	if x.format == IncrementalDefer20220824 {
		return appendLegacy(b, p)
	}
	var err error
	if len(p.pending) > 0 {
		b = append(b, `,"pending":[`...)
		for i, inst := range p.pending {
			if i > 0 {
				b = append(b, ',')
			}
			if b, err = appendPending(b, inst); err != nil {
				return nil, err
			}
		}
		b = append(b, ']')
	}
	if len(p.incremental) > 0 {
		b = append(b, `,"incremental":[`...)
		for i, en := range p.incremental {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"id":`...)
			if b, err = appendJSONString(b, en.p.id); err != nil {
				return nil, err
			}
			b = appendValue(append(b, ','), en)
			if b, err = appendErrors(b, en.errors); err != nil {
				return nil, err
			}
			b = append(b, '}')
		}
		b = append(b, ']')
	}
	if len(p.completed) > 0 {
		b = append(b, `,"completed":[`...)
		for i, d := range p.completed {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, `{"id":`...)
			if b, err = appendJSONString(b, d.p.id); err != nil {
				return nil, err
			}
			if b, err = appendErrors(b, d.errors); err != nil {
				return nil, err
			}
			b = append(b, '}')
		}
		b = append(b, ']')
	}
	return append(b, '}'), nil
}

// appendLegacy appends the incremental entries of p in the
// deferSpec=20220824 shape, where a fragment or stream that failed is an
// entry with null data or items, and closes the payload.
func appendLegacy(b []byte, p incPayload) ([]byte, error) {
	type entry struct {
		inst  incPending
		start int
		value func([]byte) []byte
		errs  gqlerror.List
	}
	var entries []entry
	for _, en := range p.incremental {
		entries = append(entries, entry{inst: en.p, start: en.start, value: func(b []byte) []byte { return appendValue(b, en) }, errs: en.errors})
	}
	for _, d := range p.completed {
		if len(d.errors) == 0 {
			continue
		}
		null := `"data":null`
		if d.p.rec.kind == streamKind {
			null = `"items":null`
		}
		entries = append(entries, entry{inst: d.p, start: d.start, value: func(b []byte) []byte { return append(b, null...) }, errs: d.errors})
	}
	if len(entries) == 0 {
		return append(b, '}'), nil
	}
	b = append(b, `,"incremental":[`...)
	for i, en := range entries {
		if i > 0 {
			b = append(b, ',')
		}
		b = en.value(append(b, '{'))
		path := en.inst.path
		if en.inst.rec.kind == streamKind {
			path = append(slices.Clip(path), en.start)
		}
		pj, err := json.Marshal(path)
		if err != nil {
			return nil, err
		}
		b = append(b, `,"path":`...)
		b = append(b, pj...)
		if b, err = appendLabel(b, en.inst.rec.label); err != nil {
			return nil, err
		}
		if b, err = appendErrors(b, en.errs); err != nil {
			return nil, err
		}
		b = append(b, '}')
	}
	return append(b, "]}"...), nil
}

// appendValue appends the data or items member of en.
func appendValue(b []byte, en incEntry) []byte {
	if !en.stream {
		b = append(b, `"data":`...)
		return append(b, en.data...)
	}
	b = append(b, `"items":[`...)
	for i, it := range en.items {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, it...)
	}
	return append(b, ']')
}

func appendPending(b []byte, inst incPending) ([]byte, error) {
	pj, err := json.Marshal(inst.path)
	if err != nil {
		return nil, err
	}
	b = append(b, `{"id":`...)
	if b, err = appendJSONString(b, inst.id); err != nil {
		return nil, err
	}
	b = append(b, `,"path":`...)
	b = append(b, pj...)
	if b, err = appendLabel(b, inst.rec.label); err != nil {
		return nil, err
	}
	return append(b, '}'), nil
}

func appendLabel(b []byte, label string) ([]byte, error) {
	if label == "" {
		return b, nil
	}
	b = append(b, `,"label":`...)
	return appendJSONString(b, label)
}

func appendErrors(b []byte, errs gqlerror.List) ([]byte, error) {
	if len(errs) == 0 {
		return b, nil
	}
	ej, err := json.Marshal(errs)
	if err != nil {
		return nil, err
	}
	b = append(b, `,"errors":`...)
	return append(b, ej...), nil
}

// ExecuteIncrementalRequest is Execute delivering the result through emit
// as ExecuteIncremental does. A request that fails before execution is
// answered by one result holding its errors.
func (e *Executor) ExecuteIncrementalRequest(ctx context.Context, req Request, format IncrementalFormat, emit Emit) error {
	ctx, op, err := e.prepare(ctx, req)
	if err == nil && op.Type == ast.Subscription {
		err = errSubscriptionTransport
	}
	if err != nil {
		body, merr := errResult(err).AppendJSON(nil)
		if merr != nil {
			return merr
		}
		return emit(body, false)
	}
	return e.ExecuteIncremental(ctx, op, format, emit)
}
