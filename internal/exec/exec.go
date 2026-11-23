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
	"time"

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
	// Partial runs each root field of a mutation in its own savepoint, so a
	// failed field is reported without rolling back the others (see
	// Request.PartialMutations).
	Partial bool
	// Built is the schema the operation was validated against.
	Built *schema.Built

	introBytes int64
	// inc is the incremental delivery plan, nil when the operation has
	// nothing to deliver incrementally.
	inc *incPlan
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
	// PartialMutations lets a request run each mutation root field in its own
	// savepoint (transactions.partial_mutations); without it such a request
	// is rejected.
	PartialMutations bool
	// TxPerRequest wraps the entire request (queries included) in one
	// transaction, so every root field reads a single snapshot.
	TxPerRequest bool
	// TxRetries re-runs a transactional operation after a serialization
	// failure or deadlock (SQLSTATE 40001/40P01); safe because the failed
	// attempt rolled back completely.
	TxRetries int
	Isolation pgx.TxIsoLevel
	// ClaimsPrefix is the namespace of the per-claim settings
	// (claims.prefix); empty sets none.
	ClaimsPrefix string
	// ClaimsJSONSetting is the setting receiving every claim as one JSON
	// object (claims.json_setting); empty sets none. When set, every
	// operation runs in a transaction so the setting is always defined.
	ClaimsJSONSetting string
	// DevErrors exposes full PG error details in GraphQL errors.
	DevErrors bool
	// StrictErrors hides every PG error message, including the
	// constraint-violation and data-exception classes that prod mode passes
	// through (errors.detail: strict). Ignored when DevErrors is set.
	StrictErrors bool
	// DisableIntrospection rejects __schema / __type root fields
	// (server.disable_introspection) and validates without the "Did you
	// mean" suggestions, which would name schema elements close to an
	// unknown field, argument, type or enum value. __typename is
	// unaffected.
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
	// DocumentCacheBytes bounds the estimated memory the document cache
	// retains (query text plus parsed and validated AST); 0 means
	// DefaultDocumentCacheBytes.
	DocumentCacheBytes int
	// DocumentCacheMaxEntryBytes is the estimated size above which a
	// document is not cached; 0 means DefaultDocumentCacheMaxEntryBytes.
	DocumentCacheMaxEntryBytes int
	// MaxQueryTokens is the most lexical tokens a query document may have
	// (the parser stops at the first one beyond it); 0 means
	// DefaultMaxQueryTokens.
	MaxQueryTokens int
	// MaxInputDepth bounds the nesting of input values (list and input
	// object literals, and variable values of list and input object types);
	// 0 means DefaultMaxInputDepth.
	MaxInputDepth int
	// MaxSelections bounds the selections (fields, fragment spreads and
	// inline fragments) of a document with every fragment spread expanded;
	// 0 means DefaultMaxSelections. Every walk of an accepted operation's
	// selections is bounded by it.
	MaxSelections int
	// MaxIntrospectionBytes bounds the estimated JSON size of the
	// introspection root fields (__schema, __type) of one operation; 0 means
	// DefaultMaxIntrospectionBytes. Aliases multiply the output of every
	// type and field they select, which the cost check does not see.
	MaxIntrospectionBytes int
	// MaxFieldComparisons bounds the field pairs validation compares to
	// check that fields sharing a response key can be merged; 0 means
	// DefaultMaxFieldComparisons.
	MaxFieldComparisons int
	// Incremental configures the defer and stream directives.
	Incremental IncrementalOptions
	// Metrics records database operations, compile time and the product
	// counters; nil records nothing.
	Metrics *Metrics
	Logger  *slog.Logger
}

// Executor executes GraphQL requests against a pool.
type Executor struct {
	Pool    *pgxpool.Pool
	Built   *schema.Built
	Compile compile.Func
	Hooks   []RequestHook
	Opts    Options

	docs *docCache
	// queryTx replaces pgexec.QueryTx in tests.
	queryTx func(ctx context.Context, db pgexec.ConnAcquirer, opts pgexec.TxOptions, id pgexec.Identity, stmts ...pgexec.Stmt) error
	// runTx replaces pgexec.RunTx for incremental delivery and partial
	// mutations in tests.
	runTx func(ctx context.Context, db pgexec.TxBeginner, opts pgexec.TxOptions, id pgexec.Identity, fn func(q pgexec.Querier) error) error
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
		docs: newDocCache(sch, opts.DocumentCacheSize,
			orDefault(opts.DocumentCacheBytes, DefaultDocumentCacheBytes),
			orDefault(opts.DocumentCacheMaxEntryBytes, DefaultDocumentCacheMaxEntryBytes)),
	}
}

// Request is a raw GraphQL HTTP/CLI request.
type Request struct {
	Query         string
	OperationName string
	Variables     map[string]any
	Claims        map[string]any
	Role          string
	// PartialMutations asks to run each root field of a mutation operation in
	// its own savepoint. It is rejected while Options.PartialMutations is off
	// and ignored for queries.
	PartialMutations bool
}

// Execute runs one GraphQL request end to end. Subscription operations are
// rejected: they need a long-lived transport (see Prepare).
func (e *Executor) Execute(ctx context.Context, req Request) *Result {
	ctx, op, err := e.prepare(ctx, req)
	if err == nil && op.Type == ast.Subscription {
		err = errSubscriptionTransport
	}
	if err != nil {
		return errResult(err)
	}
	return e.ExecutePrepared(ctx, op)
}

// errSubscriptionTransport rejects a subscription sent over plain HTTP.
var errSubscriptionTransport = gqlerror.Errorf("subscriptions are only served over WebSocket (graphql-transport-ws)")

// Prepare runs the pre-execution pipeline of Execute: parse, validate,
// operation selection, variable coercion, read-only and introspection
// policy, depth and cost limits, and the BeforeOperation hooks. It accepts
// subscription operations, which Execute rejects, so a subscription
// transport can validate an operation once and resolve its events later.
// The returned context carries the operation (see OperationFromContext).
func (e *Executor) Prepare(ctx context.Context, req Request) (context.Context, *Operation, error) {
	return e.prepare(ctx, req)
}

// ExecutePrepared executes a query or mutation operation returned by
// Prepare and runs the AfterOperation hooks.
func (e *Executor) ExecutePrepared(ctx context.Context, op *Operation) *Result {
	res := &Result{Data: map[string]json.RawMessage{}}
	e.executeOperation(ctx, op, res)
	for _, h := range e.Hooks {
		h.AfterOperation(ctx, op, res)
	}
	return res
}

// ExecuteFields runs fields, root fields of the Query type, as a query
// operation under op's role, claims, variables and fragments, and returns
// their values keyed by alias. All fields run in one round trip (one
// statement each, in one transaction when RLS or the transaction policy
// asks for it). The hooks do not run again: op already passed Prepare.
// Subscriptions use it to resolve the Query selections of each event.
func (e *Executor) ExecuteFields(ctx context.Context, op *Operation, fields []*ast.Field) *Result {
	sel := make(ast.SelectionSet, len(fields))
	for i, f := range fields {
		sel[i] = f
	}
	q := *op
	q.Type = ast.Query
	q.Definition = &ast.OperationDefinition{
		Operation:           ast.Query,
		Name:                op.Definition.Name,
		VariableDefinitions: op.Definition.VariableDefinitions,
		SelectionSet:        sel,
		Position:            op.Definition.Position,
	}
	res := &Result{Data: map[string]json.RawMessage{}}
	e.executeOperation(WithOperation(ctx, &q), &q, res)
	return res
}

// prepare runs the pre-execution pipeline shared by Execute, Prepare and
// CompileRequest: parse, validate, operation selection, variable coercion,
// limits, and BeforeOperation hooks.
func (e *Executor) prepare(ctx context.Context, req Request) (context.Context, *Operation, error) {
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	doc, err := e.document(req.Query)
	if err != nil {
		return ctx, nil, err
	}
	if err := ctx.Err(); err != nil {
		return ctx, nil, err
	}
	opDef, err := selectOperation(doc, req.OperationName)
	if err != nil {
		return ctx, nil, err
	}
	if err := e.checkVariableDepth(opDef, req.Variables); err != nil {
		return ctx, nil, err
	}
	vars, verr := validator.VariableValues(e.Built.Schema, opDef, req.Variables)
	if verr != nil {
		return ctx, nil, verr
	}
	if opDef.Operation == ast.Subscription && e.Built.Schema.Subscription == nil {
		return ctx, nil, gqlerror.Errorf("subscriptions are not supported")
	}
	if opDef.Operation == ast.Mutation && e.Opts.ReadOnly != nil && e.Opts.ReadOnly.Load() {
		return ctx, nil, gqlerror.Errorf("server is in read-only mode: mutations are disabled")
	}
	if req.PartialMutations && opDef.Operation == ast.Mutation && !e.Opts.PartialMutations {
		return ctx, nil, gqlerror.Errorf("partial mutations are disabled: set transactions.partial_mutations")
	}
	if e.Opts.DisableIntrospection {
		// __typename stays allowed: it reveals only the root type name, and
		// clients routinely send it as a liveness probe.
		for _, f := range e.queryRootFields(opDef, doc.Fragments, vars) {
			if f.Name == "__schema" || f.Name == "__type" {
				return ctx, nil, gqlerror.Errorf("introspection is disabled")
			}
		}
	}
	if err := e.checkLimits(doc, opDef, vars); err != nil {
		return ctx, nil, err
	}
	var inc *incPlan
	if e.Opts.Incremental.Enabled {
		if inc, err = e.planIncremental(doc, opDef, vars); err != nil {
			return ctx, nil, err
		}
	}

	op := &Operation{
		Name:       opDef.Name,
		Type:       opDef.Operation,
		Document:   doc,
		Definition: opDef,
		Vars:       vars,
		Claims:     req.Claims,
		Role:       req.Role,
		Partial:    req.PartialMutations && opDef.Operation == ast.Mutation,
		Built:      e.Built,
		inc:        inc,
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

// OperationType returns the type of the operation that query and
// operationName select, parsing and validating query through the document
// cache. Its error is the parse, validation or operation-selection error
// Execute would report for the same request.
func (e *Executor) OperationType(query, operationName string) (ast.Operation, error) {
	doc, err := e.document(query)
	if err != nil {
		return "", err
	}
	opDef, err := selectOperation(doc, operationName)
	if err != nil {
		return "", err
	}
	return opDef.Operation, nil
}

// selectOperation returns the operation of doc named name, or its only
// operation when name is empty.
func selectOperation(doc *ast.QueryDocument, name string) (*ast.OperationDefinition, error) {
	if opDef := doc.Operations.ForName(name); opDef != nil {
		return opDef, nil
	}
	return nil, gqlerror.Errorf("operation %q not found", name)
}

// queryRootFields returns an operation's root fields plus, for a
// subscription, the selections of each listen payload's query field, which
// resolve at the Query root.
func (e *Executor) queryRootFields(opDef *ast.OperationDefinition, frags ast.FragmentDefinitionList, vars map[string]any) []*ast.Field {
	roots := collectRootFields(opDef.SelectionSet, frags, vars)
	if opDef.Operation != ast.Subscription {
		return roots
	}
	out := slices.Clone(roots)
	for _, f := range roots {
		if f.Definition == nil {
			continue
		}
		payload := e.Built.Meta[f.Definition.Type.Name()]
		for _, pf := range collectRootFields(f.SelectionSet, frags, vars) {
			if m := payload[pf.Name]; m != nil && m.Kind == schema.KindListenQuery {
				out = append(out, collectRootFields(pf.SelectionSet, frags, vars)...)
			}
		}
	}
	return out
}

// CompiledField is one root field's compiled statement, as produced by
// CompileRequest.
type CompiledField struct {
	Field    string
	SQL      string
	Args     []any
	Mutation bool
	// Then is the statement run after this one (see
	// compile.Statement.Then), with an empty Field.
	Then *CompiledField
}

// CompileRequest runs the request pipeline up to (but not including)
// execution and returns the compiled statement for every root field. The
// database is never touched. Introspection root fields (__schema, __type)
// are resolved in memory and produce no SQL, so they are omitted.
func (e *Executor) CompileRequest(ctx context.Context, req Request) ([]CompiledField, error) {
	ctx, op, err := e.prepare(ctx, req)
	if err == nil && op.Type == ast.Subscription {
		err = errSubscriptionTransport
	}
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
		cf := CompiledField{Field: f.Alias, SQL: stmt.SQL, Args: stmt.Args, Mutation: stmt.Mutation}
		if t := stmt.Then; t != nil {
			cf.Then = &CompiledField{SQL: t.SQL, Args: t.Args, Mutation: t.Mutation}
		}
		out = append(out, cf)
	}
	return out, nil
}

func (e *Executor) executeOperation(ctx context.Context, op *Operation, res *Result) {
	rootFields := collectRootFields(op.Definition.SelectionSet, op.Document.Fragments, op.Vars)
	needTx := op.ForceTx || op.Partial ||
		e.Opts.TxPerRequest ||
		(op.Type == ast.Mutation && e.Opts.TxMutations) ||
		op.Role != "" || len(op.Claims) > 0 || e.Opts.ClaimsJSONSetting != ""
	if needTx {
		e.executeTx(ctx, op, rootFields, res)
		return
	}
	for _, f := range rootFields {
		raw, err := e.executeField(ctx, e.Pool, op, f)
		if err != nil {
			res.Errors = append(res.Errors, e.gqlError(err, f))
			res.Set(f.Alias, json.RawMessage("null"))
			if op.Type == ast.Mutation {
				break // abort remaining mutations
			}
			continue
		}
		res.Set(f.Alias, raw)
	}
}

// errTxAborted is the error PostgreSQL reports for a statement sent after
// an earlier statement of the same transaction failed. batchTx gives it to
// the query fields that pgexec.QueryTx skipped after a failure, which is the
// error they got when every field ran as its own round trip.
var errTxAborted = &pgconn.PgError{
	Severity: "ERROR",
	Code:     "25P02",
	Message:  "current transaction is aborted, commands ignored until end of transaction block",
}

// txField is one root field of a transactional operation.
type txField struct {
	field *ast.Field
	// stmt is the compiled statement, or nil for a field resolved in memory
	// (introspection) or one that failed to compile.
	stmt *compile.Statement
	raw  json.RawMessage
	err  error
	// scanned reports whether the current attempt read the statement's
	// result.
	scanned bool
}

// executeTx runs a transactional operation. Every root field is compiled
// (or, for introspection, resolved) before anything is sent; a compile error
// is that field's error, and a mutation stops at it, unless it runs with
// partial success (see partialTx).
//
// Queries, and mutations with at most one statement and no compile error,
// run as one pgexec.QueryTx batch (see batchTx). A mutation with more
// statements, or one that must roll back because a field failed to compile,
// runs statement by statement through pgexec.RunTx (see interactiveTx): once
// a mutation field fails, the fields after it must not run at all, and a
// batch would already have sent them. So does an operation with a field
// compiled to a statement pair (compile.Statement.Then), whose second
// statement takes the first one's result.
func (e *Executor) executeTx(ctx context.Context, op *Operation, rootFields []*ast.Field, res *Result) {
	mutation := op.Type == ast.Mutation
	partial := mutation && op.Partial
	fields := make([]txField, 0, len(rootFields))
	stmts, then := 0, false
	for _, f := range rootFields {
		tf := txField{field: f}
		if isIntrospectionField(f.Name) {
			tf.raw, tf.err = e.resolveIntrospection(op, f)
		} else if tf.stmt, tf.err = e.compileField(ctx, op, f); tf.stmt != nil {
			stmts++
			if tf.stmt.Then != nil {
				stmts++
				then = true
			}
		}
		fields = append(fields, tf)
		if mutation && !partial && tf.err != nil {
			e.interactiveTx(ctx, op, fields, res)
			return
		}
	}
	if partial {
		e.partialTx(ctx, op, fields, res)
		return
	}
	if (mutation && stmts > 1) || then {
		e.interactiveTx(ctx, op, fields, res)
		return
	}
	e.batchTx(ctx, op, fields, stmts, res)
}

// txOptions are the transaction options of op; Isolation is mapped by
// batchIsolation.
func (e *Executor) txOptions(op *Operation) (pgexec.TxOptions, pgexec.Identity) {
	return pgexec.TxOptions{
		Isolation:  batchIsolation(e.Opts.Isolation),
		MaxRetries: e.Opts.TxRetries,
		RLS:        true,
		Log:        e.Opts.Logger.With("operation", op.Name),
		Observer:   e.Opts.Metrics.observer(),
	}, pgexec.Identity{
		Role:              op.Role,
		Claims:            op.Claims,
		ClaimsPrefix:      e.Opts.ClaimsPrefix,
		ClaimsJSONSetting: e.Opts.ClaimsJSONSetting,
	}
}

// batchTx sends the identity and one statement per compiled field as a
// single pgexec.QueryTx batch.
//
// A query runs in the pipeline's implicit transaction (no BEGIN/COMMIT unless
// transactions.isolation is stricter than read committed). A field error
// raised in Go is recorded and the batch goes on. Once a statement fails,
// PostgreSQL skips the rest of the batch: the skipped fields get the 25P02
// error, and the response the "commit unexpectedly resulted in rollback"
// error, that they got when the transaction ran statement by statement
// (except after a serialization failure, as before).
//
// A mutation holds COMMIT back until its result was read
// (ScanBeforeCommit), so a Go-side field error such as "no row matched" or
// a JWT mint failure rolls it back.
//
// A serialization failure or deadlock re-runs the whole batch up to
// transactions.max_retries times, and the response holds only the last
// attempt.
func (e *Executor) batchTx(ctx context.Context, op *Operation, fields []txField, n int, res *Result) {
	mutation := op.Type == ast.Mutation
	var scanErr error
	var scannedAny bool
	stmts := make([]pgexec.Stmt, 0, n)
	for i := range fields {
		tf := &fields[i]
		if tf.stmt == nil {
			continue
		}
		stmts = append(stmts, pgexec.Stmt{SQL: tf.stmt.SQL, Args: tf.stmt.Args, Scan: func(row pgx.Row) error {
			scannedAny, tf.scanned = true, true
			var raw []byte
			err := row.Scan(&raw)
			tf.raw, tf.err = e.fieldResult(op, tf.field, tf.stmt, raw, err)
			if tf.err != nil && (mutation || err != nil) {
				scanErr = tf.err
				return tf.err
			}
			return nil
		}})
	}

	queryTx := e.queryTx
	if queryTx == nil {
		queryTx = pgexec.QueryTx
	}
	opts, id := e.txOptions(op)
	maxRetries := opts.MaxRetries
	opts.MaxRetries = 0
	opts.ScanBeforeCommit = mutation
	observer := opts.Observer
	var err error
	for attempt := 0; ; attempt++ {
		if observer != nil && attempt > 0 {
			opts.Observer = attemptObserver{next: observer, attempt: attempt}
		}
		scanErr, scannedAny = nil, false
		for i := range fields {
			if fields[i].stmt != nil {
				fields[i].raw, fields[i].err, fields[i].scanned = nil, nil, false
			}
		}
		err = queryTx(ctx, e.Pool, opts, id, stmts...)
		if err == nil || attempt >= maxRetries || !pgexec.IsSerializationFailure(err) || ctx.Err() != nil {
			break
		}
		opts.Log.Warn("retrying after serialization failure",
			"attempt", attempt+1, "max_retries", maxRetries)
	}

	if err == nil || scanErr != nil || scannedAny {
		for i := range fields {
			tf := &fields[i]
			if tf.stmt != nil && !tf.scanned {
				tf.err = errTxAborted
			}
			if tf.err != nil {
				res.Errors = append(res.Errors, e.gqlError(tf.err, tf.field))
				res.Set(tf.field.Alias, json.RawMessage("null"))
				if mutation {
					break
				}
				continue
			}
			res.Set(tf.field.Alias, tf.raw)
		}
	}
	switch {
	case err == nil:
	case scanErr != nil:
		if !mutation && !pgexec.IsSerializationFailure(scanErr) {
			res.Errors = append(res.Errors, e.gqlError(pgx.ErrTxCommitRollback, nil))
		}
	default:
		res.Errors = append(res.Errors, e.gqlError(err, nil))
	}
}

// interactiveTx runs a mutation's fields one statement at a time in a
// pgexec.RunTx transaction, a field's Then statement right after its own.
// The first failing field stops the loop and rolls the transaction back;
// the fields after it are left out of the response.
func (e *Executor) interactiveTx(ctx context.Context, op *Operation, fields []txField, res *Result) {
	opts, id := e.txOptions(op)
	// RunTx re-invokes fn on serialization failures/deadlocks (the failed
	// attempt rolled back completely), so per-attempt state is reset inside
	// fn. lastFnErr lets the caller tell "fn's own error came back" from
	// "a later attempt failed before reaching fn".
	var lastFnErr error
	err := pgexec.RunTx(ctx, e.Pool, opts, id, func(q pgexec.Querier) error {
		res.reset()
		lastFnErr = nil
		for _, tf := range fields {
			raw, err := tf.raw, tf.err
			if tf.stmt != nil {
				raw, err = e.queryStatement(ctx, q, op, tf.field, tf.stmt)
			}
			if err != nil {
				res.Errors = append(res.Errors, e.gqlError(err, tf.field))
				res.Set(tf.field.Alias, json.RawMessage("null"))
				lastFnErr = err
				break
			}
			res.Set(tf.field.Alias, raw)
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

// partialTx runs a mutation's fields one statement at a time in a
// pgexec.RunTx transaction, each field (with its Then statement) in its own
// savepoint. A field that fails, in the database or in Go, is rolled back to
// its savepoint and reported with a null value while the other fields run
// and commit.
//
// A serialization failure or deadlock is not a field error: it aborts the
// transaction, which RunTx re-runs up to transactions.max_retries times, and
// when the retries run out nothing is committed and the response holds that
// one error. The same goes for a failure to commit.
func (e *Executor) partialTx(ctx context.Context, op *Operation, fields []txField, res *Result) {
	opts, id := e.txOptions(op)
	runTx := e.runTx
	if runTx == nil {
		runTx = pgexec.RunTx
	}
	err := runTx(ctx, e.Pool, opts, id, func(q pgexec.Querier) error {
		res.reset()
		for _, tf := range fields {
			raw, err := tf.raw, tf.err
			if err == nil && tf.stmt != nil {
				err = pgexec.Savepoint(ctx, q, func() (err error) {
					raw, err = e.queryStatement(ctx, q, op, tf.field, tf.stmt)
					return err
				})
				var rolledBack *pgexec.RolledBackError
				switch {
				case errors.As(err, &rolledBack):
					err = rolledBack.Err
				case err != nil:
					return err
				}
			}
			if err != nil {
				res.Errors = append(res.Errors, e.gqlError(err, tf.field))
				res.Set(tf.field.Alias, json.RawMessage("null"))
				continue
			}
			res.Set(tf.field.Alias, raw)
		}
		return nil
	})
	if err != nil {
		res.reset()
		res.Errors = append(res.Errors, e.gqlError(err, nil))
	}
}

// batchIsolation is the pgexec.QueryTx isolation for transactions.isolation:
// read committed (the default) runs in the pipeline's implicit transaction
// at the server's default_transaction_isolation, saving the BEGIN/COMMIT;
// stricter levels need an explicit BEGIN ISOLATION LEVEL.
func batchIsolation(iso pgx.TxIsoLevel) pgx.TxIsoLevel {
	if iso == pgx.ReadCommitted {
		return ""
	}
	return iso
}

func (e *Executor) compileField(ctx context.Context, op *Operation, f *ast.Field) (*compile.Statement, error) {
	defer e.Opts.Metrics.compiled(time.Now())
	return e.Compile(ctx, &compile.Request{
		Field:       f,
		Fragments:   op.Document.Fragments,
		Vars:        op.Vars,
		Built:       e.Built,
		MaxDepth:    e.Opts.MaxDepth,
		MaxPageSize: e.Opts.MaxPageSize,
	})
}

func (e *Executor) executeField(ctx context.Context, q pgexec.Querier, op *Operation, f *ast.Field) (json.RawMessage, error) {
	if isIntrospectionField(f.Name) {
		return e.resolveIntrospection(op, f)
	}
	stmt, err := e.compileField(ctx, op, f)
	if err != nil {
		return nil, err
	}
	if stmt.Then != nil {
		var raw json.RawMessage
		opts, id := e.txOptions(op)
		err := pgexec.RunTx(ctx, e.Pool, opts, id, func(tx pgexec.Querier) error {
			var err error
			raw, err = e.queryStatement(ctx, tx, op, f, stmt)
			return err
		})
		return raw, err
	}
	var raw []byte
	start := time.Now()
	err = q.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&raw)
	pgexec.Observe(ctx, e.Opts.Metrics.observer(), "query", start, err)
	return e.fieldResult(op, f, stmt, raw, err)
}

// queryStatement runs stmt on q, then its Then statement with the data stmt
// returned as $1, and returns the field value of the last one.
func (e *Executor) queryStatement(ctx context.Context, q pgexec.Querier, op *Operation, f *ast.Field, stmt *compile.Statement) (json.RawMessage, error) {
	var raw []byte
	err := q.QueryRow(ctx, stmt.SQL, stmt.Args...).Scan(&raw)
	for err == nil && stmt.Then != nil {
		var data any
		if raw != nil {
			data = string(raw)
		}
		stmt, raw = stmt.Then, nil
		err = q.QueryRow(ctx, stmt.SQL, append([]any{data}, stmt.Args...)...).Scan(&raw)
	}
	return e.fieldResult(op, f, stmt, raw, err)
}

// fieldResult turns the outcome of scanning stmt's single data column (raw
// and the Scan error) into the field's response value.
func (e *Executor) fieldResult(op *Operation, f *ast.Field, stmt *compile.Statement, raw []byte, err error) (json.RawMessage, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		if stmt.Mutation {
			return nil, &gqlerror.Error{Message: "no row matched " + strconv.Quote(f.Alias), Path: ast.Path{ast.PathName(f.Alias)}}
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
// a paginated container multiply by 10; a field with a ScanCost adds it, and
// multiplies its subtree by the page-size cap when list-typed, e.g.
// groupedAggregates; aggregate orderBy values and relation-aggregate filters
// add their aggregates' Cost, tripled under a cursor).
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
	m := newMeasurer(doc.Fragments, vars, pageCap, e.Built, maxDepth, maxCost)
	depth, cost := m.measure(sel)
	if depth > maxDepth {
		return gqlerror.Errorf("operation depth %d exceeds limit %d", depth, maxDepth)
	}
	if cost > maxCost {
		e.Opts.Metrics.costRejected()
		return gqlerror.Errorf("operation cost %d exceeds limit %d", cost, maxCost)
	}
	return nil
}

// measure returns the depth and cost of sel without limits (see
// checkLimits); depth is the depth sel starts at.
func measure(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any, pageCap, depth int, built *schema.Built) (int, int) {
	m := newMeasurer(frags, vars, pageCap, built, 0, 0)
	d, c := m.measure(sel)
	return d + depth - 1, c
}

// costCeiling saturates cost arithmetic, so deeply nested multipliers cannot
// overflow into a small or negative cost.
const costCeiling = 1 << 40

// measurer computes the depth and cost of an operation's selections. Each
// fragment is measured once per pagination context and its result reused
// for every spread, so the walk is linear in the document even when
// fragments spread each other repeatedly. With limits set, the walk stops as
// soon as the depth or the running cost exceeds them.
type measurer struct {
	frags    ast.FragmentDefinitionList
	vars     map[string]any
	pageCap  int
	built    *schema.Built
	meta     schema.Meta
	maxDepth int
	maxCost  int
	memo     map[fragmentKey]measured
	// over is set once the limits are exceeded; the walk then unwinds.
	over bool
}

type fragmentKey struct {
	name      string
	paginated bool
}

// measured is the depth (relative to the selection set's own level) and
// cost of a selection set.
type measured struct{ depth, cost int }

func newMeasurer(frags ast.FragmentDefinitionList, vars map[string]any, pageCap int, built *schema.Built, maxDepth, maxCost int) *measurer {
	m := &measurer{frags: frags, vars: vars, pageCap: pageCap, built: built, maxDepth: maxDepth, maxCost: maxCost, memo: map[fragmentKey]measured{}}
	if built != nil {
		m.meta = built.Meta
	}
	return m
}

// measure returns the depth and cost of sel at the operation root; once a
// limit is exceeded they are lower bounds that already exceed it.
func (m *measurer) measure(sel ast.SelectionSet) (int, int) {
	r := m.sel(sel, 1, false)
	return 1 + r.depth, r.cost
}

func satAdd(a, b int) int { return min(a+b, costCeiling) }

func satMul(a, b int) int {
	if a == 0 || b == 0 {
		return 0
	}
	if a > costCeiling/b {
		return costCeiling
	}
	return min(a*b, costCeiling)
}

// sel walks one selection set at absolute depth (used only to stop early).
// paginated is true when the enclosing field already applied a page-size
// multiplier (a connection): its direct list children (nodes/edges) then
// count once instead of the ×10 list default, so pagination is not charged
// twice.
func (m *measurer) sel(sel ast.SelectionSet, depth int, paginated bool) measured {
	var out measured
	for _, item := range sel {
		if m.over {
			return out
		}
		var r measured
		switch v := item.(type) {
		case *ast.Field:
			// Selections excluded by @skip/@include never compile, so they
			// cost nothing (charging them could reject a valid query).
			if compile.SkipByDirectives(v.Directives, m.vars) {
				continue
			}
			r = m.field(v, depth, paginated)
		case *ast.InlineFragment:
			if compile.SkipByDirectives(v.Directives, m.vars) {
				continue
			}
			r = m.sel(v.SelectionSet, depth, paginated)
		case *ast.FragmentSpread:
			if compile.SkipByDirectives(v.Directives, m.vars) {
				continue
			}
			r = m.fragment(v.Name, depth, paginated)
		}
		out.depth = max(out.depth, r.depth)
		out.cost = satAdd(out.cost, r.cost)
		if (m.maxDepth > 0 && depth+out.depth > m.maxDepth) || (m.maxCost > 0 && out.cost > m.maxCost) {
			m.over = true
		}
	}
	return out
}

func (m *measurer) fragment(name string, depth int, paginated bool) measured {
	k := fragmentKey{name: name, paginated: paginated}
	if r, ok := m.memo[k]; ok {
		return r
	}
	def := m.frags.ForName(name)
	if def == nil {
		return measured{}
	}
	r := m.sel(def.SelectionSet, depth, paginated)
	if !m.over {
		m.memo[k] = r
	}
	return r
}

func (m *measurer) field(v *ast.Field, depth int, paginated bool) measured {
	mult, childPaginated := 1, false
	if n, ok := pageArg(v, m.vars); ok {
		// first/last drive how many rows this subtree repeats for;
		// clamp to the page-size cap the compiler enforces anyway.
		mult = min(max(n, 1), m.pageCap)
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
	if over := fieldCostMultiplier(v, m.meta); over > 0 && over < mult {
		mult = over
	}
	scan := fieldScanCost(v, m.meta)
	if scan > 0 && v.Definition != nil && v.Definition.Type.NamedType == "" {
		mult, childPaginated = m.pageCap, true
	}
	scan = satAdd(scan, aggregateArgCost(v, m.vars, m.built))
	scan = satAdd(scan, fieldRowCost(v, m.meta, m.vars))
	if len(v.SelectionSet) == 0 {
		return measured{depth: 0, cost: satAdd(1, scan)}
	}
	c := m.sel(v.SelectionSet, depth+1, childPaginated)
	return measured{depth: 1 + c.depth, cost: satAdd(satAdd(1, scan), satMul(mult, c.cost))}
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

// fieldScanCost returns the ScanCost surcharge bound to a field (aggregates
// over a whole filtered row set), or 0 when the field has none.
func fieldScanCost(f *ast.Field, meta schema.Meta) int {
	if meta == nil || f.ObjectDefinition == nil {
		return 0
	}
	if fm := meta[f.ObjectDefinition.Name][f.Name]; fm != nil {
		return fm.ScanCost
	}
	return 0
}

// fieldRowCost returns the per-evaluation surcharge bound to a field: its
// RowCost plus the ArgCost of every argument given a non-null value.
func fieldRowCost(f *ast.Field, meta schema.Meta, vars map[string]any) int {
	if meta == nil || f.ObjectDefinition == nil {
		return 0
	}
	fm := meta[f.ObjectDefinition.Name][f.Name]
	if fm == nil {
		return 0
	}
	cost := fm.RowCost
	for name, c := range fm.ArgCost {
		arg := f.Arguments.ForName(name)
		if arg == nil {
			continue
		}
		if v, err := arg.Value.Value(vars); err == nil && v != nil {
			cost = satAdd(cost, c)
		}
	}
	return cost
}

// aggregateArgCost charges the aggregates a field's arguments evaluate per
// candidate row: the Cost of each aggregate orderBy value (tripled when an
// after/before cursor repeats it in the keyset predicate) and of each
// aggregate comparison in a relation-aggregate filter.
func aggregateArgCost(f *ast.Field, vars map[string]any, built *schema.Built) int {
	if built == nil || f.Definition == nil {
		return 0
	}
	cost := 0
	if ov, enum := argValue(f, "orderBy", vars); ov != nil {
		vals, _ := ov.([]any)
		if name, ok := ov.(string); ok {
			vals = []any{name}
		}
		for _, v := range vals {
			name, _ := v.(string)
			if rel := built.OrderBy[enum][name].Relation; rel != nil && rel.Aggregate != nil {
				cost += rel.Aggregate.Cost
			}
		}
		if after, _ := argValue(f, "after", vars); after != nil {
			cost *= 3
		} else if before, _ := argValue(f, "before", vars); before != nil {
			cost *= 3
		}
	}
	if fv, filterType := argValue(f, "filter", vars); fv != nil {
		cost += filterAggregateCost(built, filterType, fv)
	}
	return cost
}

// argValue resolves a field argument against the variables, returning its
// value and the argument's named type.
func argValue(f *ast.Field, name string, vars map[string]any) (any, string) {
	arg, def := f.Arguments.ForName(name), f.Definition.Arguments.ForName(name)
	if arg == nil || def == nil {
		return nil, ""
	}
	v, err := arg.Value.Value(vars)
	if err != nil {
		return nil, ""
	}
	return v, def.Type.Name()
}

// filterAggregateCost walks a <Type>Filter value through and/or/not and
// relation filters, summing the Cost of every aggregate a to-many
// relation's aggregates filter compares.
func filterAggregateCost(built *schema.Built, typeName string, v any) int {
	m, ok := v.(map[string]any)
	if !ok {
		return 0
	}
	cost := 0
	for key, val := range m {
		if val == nil {
			continue
		}
		switch key {
		case "and", "or":
			items, _ := val.([]any)
			for _, item := range items {
				cost += filterAggregateCost(built, typeName, item)
			}
		case "not":
			cost += filterAggregateCost(built, typeName, val)
		default:
			rel := built.FilterRelations[typeName][key]
			if rel == nil || rel.Exists {
				continue
			}
			if rel.Forward && rel.FK.ReferencesOne() {
				cost += filterAggregateCost(built, rel.FilterType, val)
				continue
			}
			wrapper := inputFieldType(built, typeName, key)
			quants, _ := val.(map[string]any)
			for q, qv := range quants {
				if q != "aggregates" {
					cost += filterAggregateCost(built, rel.FilterType, qv)
					continue
				}
				aggType := inputFieldType(built, wrapper, q)
				groups, _ := qv.(map[string]any)
				for g, gv := range groups {
					if g == "filter" {
						cost += filterAggregateCost(built, rel.FilterType, gv)
						continue
					}
					cols, _ := gv.(map[string]any)
					bindings := built.InputAggregates[inputFieldType(built, aggType, g)]
					for col, ops := range cols {
						if a := bindings[col]; a != nil && ops != nil {
							cost += a.Cost
						}
					}
				}
			}
		}
	}
	return cost
}

// inputFieldType returns the named type of field on the input object
// typeName, or "" when either is unknown.
func inputFieldType(built *schema.Built, typeName, field string) string {
	if built.Schema == nil {
		return ""
	}
	def := built.Schema.Types[typeName]
	if def == nil {
		return ""
	}
	if fd := def.Fields.ForName(field); fd != nil {
		return fd.Type.Name()
	}
	return ""
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

// CollectRootFields returns the fields of a selection set with fragment
// spreads and inline fragments expanded and @skip/@include applied, as the
// executor resolves an operation's root fields; RequestHook plugins use it
// to inspect what an operation will run. For an operation that passed
// Prepare, the result is bounded by the executor's MaxSelections.
func CollectRootFields(sel ast.SelectionSet, frags ast.FragmentDefinitionList, vars map[string]any) []*ast.Field {
	return collectRootFields(sel, frags, vars)
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
// errors.detail policy: dev exposes PG details, prod sanitizes. An error of
// root field f carries the path of its response key; a GraphQL error that
// has no path yet gets that path too.
func (e *Executor) gqlError(err error, f *ast.Field) *gqlerror.Error {
	e.Opts.Metrics.fieldError(err)
	path := ast.Path{}
	if f != nil {
		path = append(path, ast.PathName(f.Alias))
	}
	var gerr *gqlerror.Error
	if errors.As(err, &gerr) {
		if len(gerr.Path) == 0 && f != nil {
			withPath := *gerr
			withPath.Path = path
			return &withPath
		}
		return gerr
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
			if e.Built != nil && !e.Opts.StrictErrors {
				msg = integrityMessage(e.Built.Catalog, pgErr, msg)
			}
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
			var err error
			if b, err = r.appendData(b); err != nil {
				return nil, err
			}
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

// appendData appends Data as a JSON object, its keys in response order.
func (r *Result) appendData(b []byte) ([]byte, error) {
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
