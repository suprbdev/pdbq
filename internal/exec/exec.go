// Package exec owns the request lifecycle: parse/validate, depth and cost
// limits, per-operation transactions, RLS role switching + claims via
// set_config, execution of compiled statements, and PG->GraphQL error
// mapping.
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/parser"
	"github.com/vektah/gqlparser/v2/validator"

	"github.com/suprbdev/pdbq/internal/compile"
	"github.com/suprbdev/pdbq/internal/schema"
)

// Operation is one GraphQL operation in flight; RequestHook plugins can read
// and mutate it (notably ForceTx).
type Operation struct {
	Name       string
	Type       ast.Operation
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
type Result struct {
	Data   map[string]json.RawMessage
	Errors gqlerror.List
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
	// MaxPageSize caps first/last and is the default page size when neither
	// is given.
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
	// Mint signs function results of one composite type into JWTs.
	Mint   MintOptions
	Logger *slog.Logger
}

// Executor executes GraphQL requests against a pool.
type Executor struct {
	Pool    *pgxpool.Pool
	Built   *schema.Built
	Compile compile.Func
	Hooks   []RequestHook
	Opts    Options
}

func New(pool *pgxpool.Pool, built *schema.Built, compileFn compile.Func, hooks []RequestHook, opts Options) *Executor {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Isolation == "" {
		opts.Isolation = pgx.ReadCommitted
	}
	return &Executor{Pool: pool, Built: built, Compile: compileFn, Hooks: hooks, Opts: opts}
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
	res := &Result{Data: map[string]json.RawMessage{}}

	doc, err := parser.ParseQuery(&ast.Source{Input: req.Query})
	if err != nil {
		return errResult(err)
	}
	if listErr := validator.Validate(e.Built.Schema, doc); len(listErr) > 0 {
		return &Result{Errors: listErr}
	}
	opDef := doc.Operations.ForName(req.OperationName)
	if opDef == nil {
		if req.OperationName == "" && len(doc.Operations) == 1 {
			opDef = doc.Operations[0]
		} else {
			return errResult(gqlerror.Errorf("operation %q not found", req.OperationName))
		}
	}
	vars, verr := validator.VariableValues(e.Built.Schema, opDef, req.Variables)
	if verr != nil {
		return errResult(verr)
	}
	if opDef.Operation == ast.Subscription {
		return errResult(gqlerror.Errorf("subscriptions are not supported"))
	}
	if err := e.checkLimits(doc, opDef, vars); err != nil {
		return errResult(err)
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
			return errResult(gqlerror.Errorf("%s", err.Error()))
		}
		ctx = ctx2
	}
	e.executeOperation(ctx, op, res)
	for _, h := range e.Hooks {
		h.AfterOperation(ctx, op, res)
	}
	return res
}

func (e *Executor) executeOperation(ctx context.Context, op *Operation, res *Result) {
	rootFields := collectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars)
	needTx := op.ForceTx ||
		e.Opts.TxPerRequest ||
		(op.Type == ast.Mutation && e.Opts.TxMutations) ||
		(e.Opts.RLS && (op.Role != "" || len(op.Claims) > 0))

	run := func(q querier) {
		for _, f := range rootFields {
			raw, err := e.executeField(ctx, q, op, f)
			if err != nil {
				res.Errors = append(res.Errors, e.gqlError(err, f))
				res.Data[f.Alias] = json.RawMessage("null")
				if op.Type == ast.Mutation {
					break // abort remaining mutations; tx rolls back
				}
				continue
			}
			res.Data[f.Alias] = raw
		}
	}

	if !needTx {
		run(e.Pool)
		return
	}
	// Serialization failures and deadlocks roll the transaction back
	// completely, so re-running the whole operation is safe.
	for attempt := 0; ; attempt++ {
		e.runTx(ctx, op, res, run)
		if attempt >= e.Opts.TxRetries || !isSerializationFailure(res.Errors) || ctx.Err() != nil {
			return
		}
		e.Opts.Logger.Warn("retrying after serialization failure",
			"operation", op.Name, "attempt", attempt+1, "max_retries", e.Opts.TxRetries)
		res.Data = map[string]json.RawMessage{}
		res.Errors = nil
	}
}

// runTx executes one transactional attempt of the operation into res.
func (e *Executor) runTx(ctx context.Context, op *Operation, res *Result, run func(querier)) {
	tx, err := e.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: e.Opts.Isolation})
	if err != nil {
		res.Errors = append(res.Errors, e.gqlError(err, nil))
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	if err := e.applyRLS(ctx, tx, op); err != nil {
		res.Errors = append(res.Errors, e.gqlError(err, nil))
		return
	}
	run(tx)
	if len(res.Errors) > 0 && op.Type == ast.Mutation {
		return // rollback via defer
	}
	if err := tx.Commit(ctx); err != nil {
		res.Errors = append(res.Errors, e.gqlError(err, nil))
	}
}

// isSerializationFailure reports whether any error carries SQLSTATE 40001
// (serialization_failure) or 40P01 (deadlock_detected). The codes survive
// error mapping in both dev and prod detail modes via the code extension.
func isSerializationFailure(errs gqlerror.List) bool {
	for _, ge := range errs {
		if code, ok := ge.Extensions["code"].(string); ok && (code == "40001" || code == "40P01") {
			return true
		}
	}
	return false
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// applyRLS switches role and exposes claims inside the transaction. It uses
// SET LOCAL exclusively (never SET) so pooled connections cannot leak
// request identity across requests once the connection is reused.
func (e *Executor) applyRLS(ctx context.Context, tx pgx.Tx, op *Operation) error {
	if !e.Opts.RLS {
		return nil
	}
	if op.Role != "" {
		if !validRole(op.Role) {
			return fmt.Errorf("invalid role %q", op.Role)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quoteIdent(op.Role)); err != nil {
			return fmt.Errorf("set role: %w", err)
		}
	}
	prefix := e.Opts.ClaimsPrefix
	if prefix == "" {
		prefix = "pdbq.claims"
	}
	for k, v := range op.Claims {
		if !validClaimKey(k) {
			continue
		}
		val := ""
		switch tv := v.(type) {
		case string:
			val = tv
		default:
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			val = string(b)
		}
		// set_config(..., true) scopes the setting to the transaction.
		if _, err := tx.Exec(ctx, "SELECT set_config($1, $2, true)", prefix+"."+k, val); err != nil {
			return fmt.Errorf("set claim %s: %w", k, err)
		}
	}
	return nil
}

func (e *Executor) executeField(ctx context.Context, q querier, op *Operation, f *ast.Field) (json.RawMessage, error) {
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
			if meta := m[f.Name]; meta != nil && meta.Kind == schema.KindFunction && e.Opts.Mint.mintsFunction(meta.Function) {
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
	depth, cost := measure(sel, doc.Fragments, vars, pageCap, 1)
	if depth > maxDepth {
		return gqlerror.Errorf("operation depth %d exceeds limit %d", depth, maxDepth)
	}
	if cost > maxCost {
		return gqlerror.Errorf("operation cost %d exceeds limit %d", cost, maxCost)
	}
	return nil
}

func measure(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any, pageCap, depth int) (int, int) {
	return measureSel(sel, frags, vars, pageCap, depth, false)
}

// measureSel walks one selection set. paginated is true when the enclosing
// field already applied a page-size multiplier (a connection): its direct
// list children (nodes/edges) then count once instead of the ×10 list
// default, so pagination is not charged twice.
func measureSel(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any, pageCap, depth int, paginated bool) (int, int) {
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
			d, c := measureSel(v.SelectionSet, frags, vars, pageCap, depth+1, childPaginated)
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
			d, c := measureSel(v.SelectionSet, frags, vars, pageCap, depth, paginated)
			cost += c
			if d > maxDepth {
				maxDepth = d
			}
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, vars) {
				continue
			}
			if def := frags.ForName(v.Name); def != nil {
				d, c := measureSel(def.SelectionSet, frags, vars, pageCap, depth, paginated)
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
			msg = sanitizePGMessage(pgErr)
		}
		return &gqlerror.Error{Message: msg, Path: path, Extensions: ext}
	}
	msg := err.Error()
	if !e.Opts.DevErrors && !strings.HasPrefix(msg, "compile:") {
		msg = "internal error"
		e.Opts.Logger.Error("request failed", "err", err)
	}
	return &gqlerror.Error{Message: msg, Path: path}
}

// sanitizePGMessage keeps actionable constraint-class errors and hides the
// rest behind a generic message.
func sanitizePGMessage(pgErr *pgconn.PgError) string {
	switch pgErr.Code[:2] {
	case "23": // integrity constraint violations are user-actionable
		return pgErr.Message
	case "22": // data exceptions (bad input format)
		return pgErr.Message
	case "42": // syntax/authorization: could leak schema internals
		if pgErr.Code == "42501" {
			return "permission denied"
		}
		return "invalid operation"
	default:
		return "database error"
	}
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

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func validRole(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}

func validClaimKey(s string) bool {
	for _, r := range s {
		ok := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return s != ""
}

// MarshalJSON renders the result as a spec-shaped GraphQL response.
func (r *Result) MarshalJSON() ([]byte, error) {
	out := map[string]any{}
	if len(r.Data) > 0 || len(r.Errors) == 0 {
		out["data"] = r.Data
	}
	if len(r.Errors) > 0 {
		out["errors"] = r.Errors
	}
	return json.Marshal(out)
}
