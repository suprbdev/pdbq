// Package errcode is pdbq's closed vocabulary of machine-readable error
// codes: the extensions.code of every GraphQL error pdbq produces, over
// HTTP and WebSocket alike. docs/errors.md documents every code; codes
// shared with pdbr use pdbr's spelling.
package errcode

import (
	"errors"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Code is the value of a GraphQL error's extensions.code.
type Code string

// The vocabulary; docs/errors.md gives the HTTP status and the conditions
// of each. Database errors use PG instead.
const (
	GraphQLParseFailed         Code = "graphql_parse_failed"          // syntax error in the document
	GraphQLValidationFailed    Code = "graphql_validation_failed"     // document or variable values fail validation
	InvalidRequest             Code = "invalid_request"               // malformed GraphQL-over-HTTP request
	DocumentLimitExceeded      Code = "document_limit_exceeded"       // a structural document limit
	DepthExceeded              Code = "depth_exceeded"                // selection or nesting depth limit
	CostExceeded               Code = "cost_exceeded"                 // estimated cost limit
	PageSizeExceeded           Code = "page_size_exceeded"            // first/last above limits.max_page_size
	IntrospectionDisabled      Code = "introspection_disabled"        // __schema/__type while introspection is off
	IntrospectionLimitExceeded Code = "introspection_limit_exceeded"  // introspection response above its byte limit
	ReadOnly                   Code = "read_only"                     // mutation in read-only mode
	PartialMutationsDisabled   Code = "partial_mutations_disabled"    // partialMutations requested but not enabled
	OperationNotSupported      Code = "operation_not_supported"       // operation this transport or schema does not serve
	OperationRejected          Code = "operation_rejected"            // a RequestHook refused the operation
	InvalidArgument            Code = "invalid_argument"              // an argument value the compiler rejects
	InvalidCursor              Code = "invalid_cursor"                // undecodable cursor, or one for another type
	NotFound                   Code = "not_found"                     // keyed mutation matched no row; unknown path
	AuthRequired               Code = "auth_required"                 // no credentials and no anonymous role
	InvalidToken               Code = "invalid_token"                 // credentials present but invalid
	RoleNotAllowed             Code = "role_not_allowed"              // valid credentials, refused role
	MethodNotAllowed           Code = "method_not_allowed"            // HTTP method not served for the request
	UnsupportedMediaType       Code = "unsupported_media_type"        // POST body is not application/json
	NotAcceptable              Code = "not_acceptable"                // Accept cannot be satisfied
	BodyTooLarge               Code = "body_too_large"                // body above limits.max_body_bytes
	PersistedQueryNotFound     Code = "persisted_query_not_found"     // hash unknown: resend with the query
	PersistedQueryNotSupported Code = "persisted_query_not_supported" // persisted queries or registration off
	PersistedQueryRequired     Code = "persisted_query_required"      // persisted_queries.only without a hash
	SubscriptionLimitExceeded  Code = "subscription_limit_exceeded"   // per-connection operation or buffer limit
	Unavailable                Code = "unavailable"                   // database or subscriptions unavailable, or at capacity
	Timeout                    Code = "timeout"                       // server.request_timeout elapsed
	Internal                   Code = "internal"                      // anything else
)

// All lists every code of the vocabulary except the PG family.
var All = []Code{
	GraphQLParseFailed, GraphQLValidationFailed, InvalidRequest,
	DocumentLimitExceeded, DepthExceeded, CostExceeded, PageSizeExceeded,
	IntrospectionDisabled, IntrospectionLimitExceeded, ReadOnly,
	PartialMutationsDisabled, OperationNotSupported, OperationRejected,
	InvalidArgument, InvalidCursor, NotFound, AuthRequired, InvalidToken,
	RoleNotAllowed, MethodNotAllowed, UnsupportedMediaType, NotAcceptable,
	BodyTooLarge, PersistedQueryNotFound, PersistedQueryNotSupported,
	PersistedQueryRequired, SubscriptionLimitExceeded, Unavailable, Timeout,
	Internal,
}

// pgPrefix starts the code of a database error.
const pgPrefix = "pg:"

// PG returns the code of a database error with SQLSTATE sqlstate:
// "pg:" followed by the five-character state, e.g. pg:23505.
func PG(sqlstate string) Code { return Code(pgPrefix + sqlstate) }

// IsPG reports whether c is a database error code.
func (c Code) IsPG() bool { return len(c) > len(pgPrefix) && c[:len(pgPrefix)] == pgPrefix }

// Errorf returns a GraphQL error with the formatted message and code.
func Errorf(code Code, format string, args ...any) *gqlerror.Error {
	err := gqlerror.Errorf(format, args...)
	err.Extensions = map[string]any{"code": string(code)}
	return err
}

// Of returns the code err carries: its extensions.code when err is a
// *gqlerror.Error, the Code method's result when err has one, else "".
func Of(err error) Code {
	var gerr *gqlerror.Error
	if errors.As(err, &gerr) {
		if c, ok := gerr.Extensions["code"].(string); ok {
			return Code(c)
		}
		return ""
	}
	var coded interface{ Code() Code }
	if errors.As(err, &coded) {
		return coded.Code()
	}
	return ""
}

// With sets code on gerr unless it already carries one, and returns gerr.
// It replaces Extensions with a copy, so a map shared with other errors is
// never written to.
func With(gerr *gqlerror.Error, code Code) *gqlerror.Error {
	setCode(gerr, code)
	return gerr
}

// WithAll sets code, as With does, on err when it is a *gqlerror.Error and
// on every error of err when it is a gqlerror.List, and returns err.
func WithAll(err error, code Code) error {
	var list gqlerror.List
	if errors.As(err, &list) {
		for _, gerr := range list {
			setCode(gerr, code)
		}
		return err
	}
	var gerr *gqlerror.Error
	if errors.As(err, &gerr) {
		setCode(gerr, code)
	}
	return err
}

func setCode(gerr *gqlerror.Error, code Code) {
	if gerr == nil {
		return
	}
	if _, ok := gerr.Extensions["code"]; ok {
		return
	}
	ext := make(map[string]any, len(gerr.Extensions)+1)
	for k, v := range gerr.Extensions {
		ext[k] = v
	}
	ext["code"] = string(code)
	gerr.Extensions = ext
}
