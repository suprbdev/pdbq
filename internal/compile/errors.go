package compile

import "fmt"

// UserError is a compile error whose message is safe to return to API
// clients even with errors.detail "prod": it describes a problem with the
// request itself (pagination arguments, cursors, input shape) and contains
// no schema internals. Compile errors that are not UserErrors are sanitized
// to a generic message by the executor in prod mode.
type UserError struct{ msg string }

func (e *UserError) Error() string { return e.msg }

// errUser builds a client-safe compile error. Keep schema internals —
// table, column and SQL type names, generated input type names — out of
// these messages; echoing back user-typed GraphQL field, argument or
// operator names is fine.
func errUser(format string, args ...any) error {
	return &UserError{msg: fmt.Sprintf("compile: "+format, args...)}
}
