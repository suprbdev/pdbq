// Package exec owns the request lifecycle: parse/validate, depth and cost
// limits, per-operation transactions, RLS role switching + claims via
// set_config, execution of compiled statements, and PG->GraphQL error
// mapping.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbcore/pgexec"
	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Operation is one GraphQL operation in flight; RequestHook plugins can read
// and mutate it (notably ForceTx).
type Operation struct {
	Name string
	Type ast.Operation
	// Document is the parsed and validated document. It may be shared with
	// concurrent requests through the executor's document cache and must not
	// be modified.
	Document   *ast.QueryDocument
	Definition *ast.OperationDefinition
	Vars       map[string]any
	// Claims are the verified request claims exposed to RLS policies.
	Claims map[string]any
	// Role is the database role this operation runs as ("" = no switch).
	Role string
	// ForceTx forces a transaction even when the global policy would skip it.
	ForceTx bool
}

// Result is the GraphQL response for one operation.
//
// Data maps each response key to the raw JSON value PostgreSQL (or the
// in-memory introspection resolver) produced for it. The response writes
// root fields in selection order: keys stored with Set keep the order in
// which they were first set, and keys added to Data directly follow them in
// sorted order.
type Result struct {
	Data   map[string]json.RawMessage
	Errors gqlerror.List
	// Extensions, when non-empty, is written as the response's top-level
	// "extensions" entry.
	Extensions map[string]any

	keys []string
}

// Set stores raw as the value of the response key, recording the key's
// position in the response the first time it is set.
func (r *Result) Set(key string, raw json.RawMessage) {
	if r.Data == nil {
		r.Data = map[string]json.RawMessage{}
	}
	if _, ok := r.Data[key]; !ok {
		r.keys = append(r.keys, key)
	}
	r.Data[key] = raw
}

// Get returns the raw value of the response key and whether it is present.
func (r *Result) Get(key string) (json.RawMessage, bool) {
	raw, ok := r.Data[key]
	return raw, ok
}

// Keys returns the keys of Data in response order.
func (r *Result) Keys() []string {
	out := make([]string, 0, len(r.Data))
	seen := make(map[string]struct{}, len(r.Data))
	for _, k := range r.keys {
		if _, ok := r.Data[k]; !ok {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	known := len(out)
	for k := range r.Data {
		if _, ok := seen[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out[known:])
	return out
}

func (r *Result) reset() {
	r.Data = map[string]json.RawMessage{}
	r.Errors = nil
	r.keys = r.keys[:0]
}

// RequestHook mirrors plugin.RequestHook (redeclared here to avoid an import
// cycle; the interfaces are structurally identical).
type RequestHook interface {
	BeforeOperation(ctx context.Context, op *Operation) (context.Context, error)
	AfterOperation(ctx context.Context, op *Operation, res *Result)
}

// Options for the executor.
type Options struct {
	MaxDepth int
	MaxCost  int
	// MaxPageSize is the largest accepted first/last (larger is rejected)
	// and the default page size when neither is given.
	MaxPageSize int
	// TxMutations wraps every mutation in a transaction.
	TxMutations bool
	// TxPerRequest wraps the entire request (queries included) in one
	// transaction, so every root field reads a single snapshot.
	TxPerRequest bool
	// TxRetries re-runs a transactional operation after a serialization
	// failure or deadlock (SQLSTATE 40001/40P01); safe because the failed
	// attempt rolled back completely.
	TxRetries int
	Isolation pgx.TxIsoLevel
	// RLS enables SET LOCAL ROLE + claims set_config per operation.
	RLS          bool
	ClaimsPrefix string
	// DevErrors exposes full PG error details in GraphQL errors.
	DevErrors bool
	// StrictErrors hides every PG error message, including the
	// constraint-violation and data-exception classes that prod mode passes
	// through (errors.detail: strict). Ignored when DevErrors is set.
	StrictErrors bool
	// DisableIntrospection rejects __schema / __type root fields
	// (server.disable_introspection). __typename is unaffected.
	DisableIntrospection bool
	// ReadOnly, when non-nil and true, rejects mutation operations before
	// they compile or touch the database. It is a shared pointer so the
	// embedding App can flip it at runtime and the state survives watch-mode
	// executor swaps.
	ReadOnly *atomic.Bool
	// Mint signs function results of one composite type into JWTs.
	Mint MintOptions
	// DocumentCacheSize is the number of parsed and validated query
	// documents the executor keeps, keyed by query text (least recently used
	// evicted first). 0 disables the cache. The cache belongs to the
	// executor, so replacing the executor (watch mode) discards it with the
	// schema it was validated against.
	DocumentCacheSize int
	Logger            *slog.Logger
}

// Executor executes GraphQL requests against a pool.
type Executor struct {
	Pool    *pgxpool.Pool
	Built   *schema.Built
	Compile compile.Func
	Hooks   []RequestHook
	Opts    Options

	docs *docCache
}

// New builds an executor for built. Executors constructed without New run
// without a document cache.
func New(pool *pgxpool.Pool, built *schema.Built, compileFn compile.Func, hooks []RequestHook, opts Options) *Executor {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Isolation == "" {
		opts.Isolation = pgx.ReadCommitted
	}
	var sch *ast.Schema
	if built != nil {
		sch = built.Schema
	}
	return &Executor{
		Pool:    pool,
		Built:   built,
		Compile: compileFn,
		Hooks:   hooks,
		Opts:    opts,
		docs:    newDocCache(sch, opts.DocumentCacheSize, documentCacheBytes),
	}
}

// Request is a raw GraphQL HTTP/CLI request.
type Request struct {
	Query         string
	OperationName string
	Variables     map[string]any
	Claims        map[string]any
	Role          string
}

// Execute runs one GraphQL request end to end.
func (e *Executor) Execute(ctx context.Context, req Request) *Result {
	ctx, op, err := e.prepare(ctx, req)
	if err != nil {
		return errResult(err)
	}
	res := &Result{Data: map[string]json.RawMessage{}}
	e.executeOperation(ctx, op, res)
	for _, h := range e.Hooks {
		h.AfterOperation(ctx, op, res)
	}
	return res
}

// prepare runs the pre-execution pipeline shared by Execute and
// CompileRequest: parse, validate, operation selection, variable coercion,
// limits, and BeforeOperation hooks.
func (e *Executor) prepare(ctx context.Context, req Request) (context.Context, *Operation, error) {
	doc, err := e.document(req.Query)
	if err != nil {
		return ctx, nil, err
	}
	opDef := doc.Operations.ForName(req.OperationName)
	if opDef == nil {
		if req.OperationName == "" && len(doc.Operations) == 1 {
			opDef = doc.Operations[0]
		} else {
			return ctx, nil, gqlerror.Errorf("operation %q not found", req.OperationName)
		}
	}
	vars, verr := validator.VariableValues(e.Built.Schema, opDef, req.Variables)
	if verr != nil {
		return ctx, nil, verr
	}
	if opDef.Operation == ast.Subscription {
		return ctx, nil, gqlerror.Errorf("subscriptions are not supported")
	}
	if opDef.Operation == ast.Mutation && e.Opts.ReadOnly != nil && e.Opts.ReadOnly.Load() {
		return ctx, nil, gqlerror.Errorf("server is in read-only mode: mutations are disabled")
	}
	if e.Opts.DisableIntrospection {
		// __typename stays allowed: it reveals only the root type name, and
		// clients routinely send it as a liveness probe.
		for _, f := range collectRootFields(opDef.SelectionSet, doc.Fragments, vars) {
			if f.Name == "__schema" || f.Name == "__type" {
				return ctx, nil, gqlerror.Errorf("introspection is disabled")
			}
		}
	}
	if err := e.checkLimits(doc, opDef, vars); err != nil {
		return ctx, nil, err
	}

	op := &Operation{
		Name:       opDef.Name,
		Type:       opDef.Operation,
		Document:   doc,
		Definition: opDef,
		Vars:       vars,
		Claims:     req.Claims,
		Role:       req.Role,
	}
	// Expose the operation (claims, role, vars) to everything downstream that
	// only sees a context — notably CompileHook plugins.
	ctx = WithOperation(ctx, op)
	for _, h := range e.Hooks {
		ctx2, err := h.BeforeOperation(ctx, op)
		if err != nil {
			return ctx, nil, gqlerror.Errorf("%s", err.Error())
		}
		ctx = ctx2
	}
	return ctx, op, nil
}

// CompiledField is one root field's compiled statement, as produced by
// CompileRequest.
type CompiledField struct {
	Field    string
	SQL      string
	Args     []any
	Mutation bool
}

// CompileRequest runs the request pipeline up to (but not including)
// execution and returns the compiled statement for every root field. The
// database is never touched. Introspection root fields (__schema, __type)
// are resolved in memory and produce no SQL, so they are omitted.
func (e *Executor) CompileRequest(ctx context.Context, req Request) ([]CompiledField, error) {
	ctx, op, err := e.prepare(ctx, req)
	if err != nil {
		return nil, err
	}
	var out []CompiledField
	for _, f := range collectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars) {
		if isIntrospectionField(f.Name) {
			continue
		}
		stmt, err := e.Compile(ctx, &compile.Request{
			Field:       f,
			Fragments:   op.Document.Fragments,
			Vars:        op.Vars,
			Built:       e.Built,
			MaxDepth:    e.Opts.MaxDepth,
			MaxPageSize: e.Opts.MaxPageSize,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, CompiledField{Field: f.Alias, SQL: stmt.SQL, Args: stmt.Args, Mutation: stmt.Mutation})
	}
	return out, nil
}

func (e *Executor) executeOperation(ctx context.Context, op *Operation, res *Result) {
	rootFields := collectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars)
	needTx := op.ForceTx ||
		e.Opts.TxPerRequest ||
		(op.Type == ast.Mutation && e.Opts.TxMutations) ||
		(e.Opts.RLS && (op.Role != "" || len(op.Claims) > 0))

	// run executes every root field into res and returns the raw error of the
	// first failed field plus the first serialization failure, if any.
	run := func(q pgexec.Querier) (first, serialization error) {
		for _, f := range rootFields {
			raw, err := e.executeField(ctx, q, op, f)
			if err != nil {
				res.Errors = append(res.Errors, e.gqlError(err, f))
				res.Set(f.Alias, json.RawMessage("null"))
				if first == nil {
					first = err
				}
				if serialization == nil && pgexec.IsSerializationFailure(err) {
					serialization = err
				}
				if op.Type == ast.Mutation {
					break // abort remaining mutations; tx rolls back
				}
				continue
			}
			res.Set(f.Alias, raw)
		}
		return first, serialization
	}

	if !needTx {
		_, _ = run(e.Pool) // errors are already in res
		return
	}
	// RunTx re-invokes fn on serialization failures/deadlocks (the failed
	// attempt rolled back completely), so per-attempt state is reset inside
	// fn. lastFnErr lets the caller tell "fn's own error came back" from
	// "a later attempt failed before reaching fn".
	var lastFnErr error
	err := pgexec.RunTx(ctx, e.Pool, pgexec.TxOptions{
		Isolation:  e.Opts.Isolation,
		MaxRetries: e.Opts.TxRetries,
		RLS:        e.Opts.RLS,
		Log:        e.Opts.Logger.With("operation", op.Name),
	}, pgexec.Identity{
		Role:         op.Role,
		Claims:       op.Claims,
		ClaimsPrefix: e.Opts.ClaimsPrefix,
	}, func(q pgexec.Querier) error {
		res.reset()
		first, serialization := run(q)
		switch {
		case serialization != nil:
			// Queries retry too: the raw PG error drives RunTx's retry.
			lastFnErr = serialization
		case first != nil && op.Type == ast.Mutation:
			lastFnErr = first // no commit; rollback via RunTx
		default:
			lastFnErr = nil // queries commit even after a field error
		}
		return lastFnErr
	})
	if err == nil || (lastFnErr != nil && errors.Is(err, lastFnErr)) {
		return // fn's errors are already in res
	}
	if lastFnErr != nil {
		// A retry attempt failed in begin/identity before reaching fn: drop
		// the previous attempt's results as fn would have.
		res.reset()
	}
	res.Errors = append(res.Errors, e.gqlError(err, nil))
}

func (e *Executor) executeField(ctx context.Context, q pgexec.Querier, op *Operation, f *ast.Field) (json.RawMessage, error) {
	if isIntrospectionField(f.Name) {
		return e.resolveIntrospection(op, f)
	}
	stmt, err := e.Compile(ctx, &compile.Request{
		Field:       f,
		Fragments:   op.Document.Fragments,
		Vars:        op.Vars,
		Built:       e.Built,
		MaxDepth:    e.Opts.MaxDepth,
		MaxPageSize: e.Opts.MaxPageSize,
	})
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = q.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		if stmt.Mutation {
			return nil, gqlerror.Errorf("no row matched %q", f.Name)
		}
		return json.RawMessage("null"), nil
	}
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return json.RawMessage("null"), nil
	}
	if e.Opts.Mint.Enabled() {
		root := "Query"
		if op.Type == ast.Mutation {
			root = "Mutation"
		}
		if m, ok := e.Built.Meta[root]; ok {
			if meta := m[f.Name]; meta != nil && meta.Kind == schema.KindFunction && e.Opts.Mint.MintsFunction(meta.Function) {
				return e.mintField(raw, op, f, meta)
			}
		}
	}
	return raw, nil
}

// checkLimits enforces max depth and a simple cost heuristic (each field
// costs 1; a paginated field multiplies its subtree by the requested
// first/last page size — assuming 10 when neither is given — and its direct
// list children (nodes/edges) are not multiplied again; list fields outside
// a paginated container multiply by 10).
func (e *Executor) checkLimits(doc *ast.QueryDocument, op *ast.OperationDefinition, vars map[string]any) error {
	maxDepth, maxCost := e.Opts.MaxDepth, e.Opts.MaxCost
	if maxDepth <= 0 {
		maxDepth = 15
	}
	if maxCost <= 0 {
		maxCost = 10000
	}
	pageCap := e.Opts.MaxPageSize
	if pageCap <= 0 {
		pageCap = 100
	}
	// Introspection root fields (__schema, __type) are resolved in memory,
	// never against the database, so they are exempt from SQL-oriented
	// depth/cost limits (the standard introspection query would blow both).
	sel := make(ast.SelectionSet, 0, len(op.SelectionSet))
	for _, item := range op.SelectionSet {
		if f, ok := item.(*ast.Field); ok && isIntrospectionField(f.Name) {
			continue
		}
		sel = append(sel, item)
	}
	depth, cost := measure(sel, doc.Fragments, vars, pageCap, 1, e.Built.Meta)
	if depth > maxDepth {
		return gqlerror.Errorf("operation depth %d exceeds limit %d", depth, maxDepth)
	}
	if cost > maxCost {
		return gqlerror.Errorf("operation cost %d exceeds limit %d", cost, maxCost)
	}
	return nil
}

func measure(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any, pageCap, depth int, meta schema.Meta) (int, int) {
	return measureSel(sel, frags, vars, pageCap, depth, false, meta)
}

// fieldCostMultiplier returns the @costMultiplier override bound to a field,
// or 0 when the field has none. The lookup is by parent object type + field
// name, the same binding the compiler uses (see compile.parentTypeName).
func fieldCostMultiplier(f *ast.Field, meta schema.Meta) int {
	if meta == nil || f.ObjectDefinition == nil {
		return 0
	}
	fields, ok := meta[f.ObjectDefinition.Name]
	if !ok {
		return 0
	}
	fm, ok := fields[f.Name]
	if !ok || fm == nil {
		return 0
	}
	return fm.CostMultiplier
}

// measureSel walks one selection set. paginated is true when the enclosing
// field already applied a page-size multiplier (a connection): its direct
// list children (nodes/edges) then count once instead of the ×10 list
// default, so pagination is not charged twice.
func measureSel(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any, pageCap, depth int, paginated bool, meta schema.Meta) (int, int) {
	maxDepth, cost := depth, 0
	for _, item := range sel {
		switch v := item.(type) {
		case *ast.Field:
			// Selections excluded by @skip/@include never compile, so they
			// cost nothing (charging them could reject a valid query).
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			mult, childPaginated := 1, false
			if n, ok := pageArg(v, vars); ok {
				// first/last drive how many rows this subtree repeats for;
				// clamp to the page-size cap the compiler enforces anyway.
				mult = min(max(n, 1), pageCap)
				childPaginated = true
			} else if hasPageArgs(v) {
				mult = 10 // connection without explicit first/last
				childPaginated = true
			} else if !paginated && v.Definition != nil && v.Definition.Type.NamedType == "" {
				mult = 10 // list field outside a paginated container
			}
			// An @costMultiplier override replaces the page-size assumption
			// with the schema's declared expected fan-out. It only ever lowers
			// the estimate: a query asking for fewer rows than the declared
			// average must not be charged the average.
			if over := fieldCostMultiplier(v, meta); over > 0 && over < mult {
				mult = over
			}
			d, c := measureSel(v.SelectionSet, frags, vars, pageCap, depth+1, childPaginated, meta)
			cost += 1 + mult*c
			if len(v.SelectionSet) == 0 {
				d = depth
			}
			if d > maxDepth {
				maxDepth = d
			}
		case *ast.InlineFragment:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			d, c := measureSel(v.SelectionSet, frags, vars, pageCap, depth, paginated, meta)
			cost += c
			if d > maxDepth {
				maxDepth = d
			}
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			if def := frags.ForName(v.Name); def != nil {
				d, c := measureSel(def.SelectionSet, frags, vars, pageCap, depth, paginated, meta)
				cost += c
				if d > maxDepth {
					maxDepth = d
				}
			}
		}
	}
	return maxDepth, cost
}

// hasPageArgs reports whether the field is pagination-capable (declares a
// first or last argument), i.e. compiles to a paginated row set even when
// the query omits an explicit page size.
func hasPageArgs(f *ast.Field) bool {
	if f.Definition == nil {
		return false
	}
	return f.Definition.Arguments.ForName("first") != nil ||
		f.Definition.Arguments.ForName("last") != nil
}

// pageArg extracts a field's first/last argument as an int, resolving
// variables against the coerced variable values.
func pageArg(f *ast.Field, vars map[string]any) (int, bool) {
	for _, name := range []string{"first", "last"} {
		arg := f.Arguments.ForName(name)
		if arg == nil || arg.Value == nil {
			continue
		}
		switch arg.Value.Kind {
		case ast.IntValue:
			if n, err := strconv.Atoi(arg.Value.Raw); err == nil {
				return n, true
			}
		case ast.Variable:
			switch n := vars[arg.Value.Raw].(type) {
			case int64:
				return int(n), true
			case int:
				return n, true
			case float64:
				return int(n), true
			case json.Number:
				if i, err := n.Int64(); err == nil {
					return int(i), true
				}
			}
		}
	}
	return 0, false
}

// collectRootFields flattens the operation's selection set to executable root
// fields, dropping selections excluded by @skip/@include (an excluded root
// field is simply absent from the response).
func collectRootFields(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any) []*ast.Field {
	var out []*ast.Field
	for _, item := range sel {
		switch v := item.(type) {
		case *ast.Field:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			out = append(out, v)
		case *ast.InlineFragment:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			out = append(out, collectRootFields(v.SelectionSet, frags, vars)...)
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			if def := frags.ForName(v.Name); def != nil {
				out = append(out, collectRootFields(def.SelectionSet, frags, vars)...)
			}
		}
	}
	return out
}

// gqlError maps any execution error to a GraphQL error honouring the
// errors.detail policy: dev exposes PG details, prod sanitizes.
func (e *Executor) gqlError(err error, f *ast.Field) *gqlerror.Error {
	var gerr *gqlerror.Error
	if errors.As(err, &gerr) {
		return gerr
	}
	path := ast.Path{}
	if f != nil {
		path = append(path, ast.PathName(f.Alias))
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		ext := map[string]any{"code": pgErr.Code}
		msg := pgErr.Message
		if e.Opts.DevErrors {
			ext["detail"] = pgErr.Detail
			ext["hint"] = pgErr.Hint
			ext["table"] = pgErr.TableName
			ext["constraint"] = pgErr.ConstraintName
		} else {
			msg = pgexec.SanitizeMessage(pgErr, e.Opts.StrictErrors)
		}
		return &gqlerror.Error{Message: msg, Path: path, Extensions: ext}
	}
	msg := err.Error()
	if !e.Opts.DevErrors {
		// Only compile errors explicitly marked client-safe pass through in
		// prod; other compile errors can carry schema internals (table,
		// column, type names) and are reduced to a generic message.
		var uerr *compile.UserError
		if !errors.As(err, &uerr) {
			e.Opts.Logger.Error("request failed", "err", err)
			if strings.HasPrefix(msg, "compile:") {
				msg = "compile: invalid query"
			} else {
				msg = "internal error"
			}
		}
	}
	return &gqlerror.Error{Message: msg, Path: path}
}

func errResult(err error) *Result {
	var list gqlerror.List
	if !errors.As(err, &list) {
		var gerr *gqlerror.Error
		if errors.As(err, &gerr) {
			list = gqlerror.List{gerr}
		} else {
			list = gqlerror.List{gqlerror.Errorf("%s", err.Error())}
		}
	}
	return &Result{Errors: list}
}

// MarshalJSON renders the result as a spec-shaped GraphQL response; see
// AppendJSON.
func (r *Result) MarshalJSON() ([]byte, error) {
	return r.AppendJSON(nil)
}

// AppendJSON appends the result as a spec-shaped GraphQL response to b:
// "data" (omitted when it is empty and there are errors), then "errors"
// and "extensions" when non-empty. Root field values are copied verbatim, in
// the order given by Keys, without being re-encoded.
func (r *Result) AppendJSON(b []byte) ([]byte, error) {
	b = append(b, '{')
	sep := false
	if len(r.Data) > 0 || len(r.Errors) == 0 {
		b = append(b, `"data":`...)
		if r.Data == nil {
			b = append(b, "null"...)
		} else {
			b = append(b, '{')
			for i, k := range r.Keys() {
				if i > 0 {
					b = append(b, ',')
				}
				var err error
				if b, err = appendJSONString(b, k); err != nil {
					return nil, err
				}
				b = append(b, ':')
				if raw := r.Data[k]; len(raw) > 0 {
					b = append(b, raw...)
				} else {
					b = append(b, "null"...)
				}
			}
			b = append(b, '}')
		}
		sep = true
	}
	if len(r.Errors) > 0 {
		if sep {
			b = append(b, ',')
		}
		errs, err := json.Marshal(r.Errors)
		if err != nil {
			return nil, err
		}
		b = append(b, `"errors":`...)
		b = append(b, errs...)
		sep = true
	}
	if len(r.Extensions) > 0 {
		if sep {
			b = append(b, ',')
		}
		ext, err := json.Marshal(r.Extensions)
		if err != nil {
			return nil, err
		}
		b = append(b, `"extensions":`...)
		b = append(b, ext...)
	}
	return append(b, '}'), nil
}

// appendJSONString appends s as a JSON string, taking a copy-only fast path
// for the plain ASCII names GraphQL response keys normally are.
func appendJSONString(b []byte, s string) ([]byte, error) {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\\' || c == '<' || c == '>' || c == '&' {
			q, err := json.Marshal(s)
			if err != nil {
				return nil, err
			}
			return append(b, q...), nil
		}
	}
	b = append(b, '"')
	b = append(b, s...)
	return append(b, '"'), nil
}
