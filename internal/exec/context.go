package exec

import "context"

type opCtxKey struct{}

// WithOperation attaches the in-flight operation to the context. The executor
// does this before running RequestHooks and compiling, so CompileHook plugins
// (whose compile.Func only receives a context) can reach the verified claims,
// role, and operation metadata.
func WithOperation(ctx context.Context, op *Operation) context.Context {
	return context.WithValue(ctx, opCtxKey{}, op)
}

// OperationFromContext returns the in-flight operation attached by the
// executor, or nil when called outside a request (e.g. schema build).
func OperationFromContext(ctx context.Context) *Operation {
	op, _ := ctx.Value(opCtxKey{}).(*Operation)
	return op
}

// ClaimsFromContext returns the verified request claims, or nil outside a
// request. Shorthand for OperationFromContext(ctx).Claims.
func ClaimsFromContext(ctx context.Context) map[string]any {
	if op := OperationFromContext(ctx); op != nil {
		return op.Claims
	}
	return nil
}
