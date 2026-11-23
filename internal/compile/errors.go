package compile

import (
	"fmt"

	"github.com/suprbdev/pdbq/internal/errcode"
)

// UserError is a compile error whose message is safe to return to API
// clients even with errors.detail "prod": it describes a problem with the
// request itself (pagination arguments, cursors, input shape) and contains
// no schema internals. Compile errors that are not UserErrors are sanitized
// to a generic message by the executor in prod mode.
type UserError struct {
	msg  string
	code errcode.Code
}

func (e *UserError) Error() string { return e.msg }

// Code returns the error's extensions.code: invalid_argument unless the
// error was built with another one.
func (e *UserError) Code() errcode.Code {
	if e.code == "" {
		return errcode.InvalidArgument
	}
	return e.code
}

// errUser builds a client-safe compile error (code invalid_argument). Keep
// schema internals — table, column and SQL type names, generated input type
// names — out of these messages; echoing back user-typed GraphQL field,
// argument or operator names is fine.
func errUser(format string, args ...any) error {
	return &UserError{msg: fmt.Sprintf("compile: "+format, args...)}
}

// errUserCode is errUser with another code than invalid_argument.
func errUserCode(code errcode.Code, format string, args ...any) error {
	return &UserError{msg: fmt.Sprintf("compile: "+format, args...), code: code}
}

// errCursor rejects a cursor argument (code invalid_cursor).
func errCursor(format string, args ...any) error {
	return errUserCode(errcode.InvalidCursor, format, args...)
}

// errDepth rejects a selection nested deeper than the depth limit (code
// depth_exceeded).
func errDepth(limit int) error {
	return errUserCode(errcode.DepthExceeded, "selection depth exceeds limit (%d)", limit)
}

// errPageSize rejects a first/last above the server page-size cap. Silently
// clamping instead would return fewer rows than the client asked for, which
// breaks paginators that infer "last page" from a short result and leaves
// response shapes mismatched against the requested size.
func errPageSize(arg string, got, max int) error {
	return errUserCode(errcode.PageSizeExceeded, "%s %d exceeds the maximum page size of %d", arg, got, max)
}
