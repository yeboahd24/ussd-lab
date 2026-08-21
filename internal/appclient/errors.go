// Package appclient talks to the developer's USSD application over HTTP.
//
// It is an outer layer: it depends on internal/protocol, never the reverse.
// Its main responsibility beyond transport is ERROR CLASSIFICATION -- turning
// the many ways an HTTP call can fail into the small set of distinct causes a
// developer can actually act on (MVP design §21, brief §20).
package appclient

import (
	"errors"
	"fmt"
)

// ErrorCode classifies a failure to obtain a usable application response.
type ErrorCode string

const (
	// CodeUnavailable: the application could not be reached at all --
	// connection refused, DNS failure, no route.
	CodeUnavailable ErrorCode = "APPLICATION_UNAVAILABLE"

	// CodeTimeout: the application accepted the connection but did not answer
	// in time.
	CodeTimeout ErrorCode = "APPLICATION_TIMEOUT"

	// CodeHTTPStatus: the application answered with a non-2xx status.
	CodeHTTPStatus ErrorCode = "APPLICATION_HTTP_ERROR"

	// CodeMalformed: the application answered, but not with CON/END.
	CodeMalformed ErrorCode = "MALFORMED_RESPONSE"

	// CodeTooLarge: the response body exceeded protocol.MaxResponseBytes.
	CodeTooLarge ErrorCode = "RESPONSE_TOO_LARGE"

	// CodeRedirectBlocked: the application tried to redirect the request.
	// Following it could send traffic to a host the developer never
	// configured, so it is refused (MVP design §23).
	CodeRedirectBlocked ErrorCode = "REDIRECT_BLOCKED"

	// CodeInvalidRequest: the request was rejected before being sent.
	CodeInvalidRequest ErrorCode = "INVALID_REQUEST"
)

// Error is a classified application-call failure.
//
// Hint carries the actionable next step. Diagnostics that only say what broke
// leave the developer to guess; the hint says what to do about it.
type Error struct {
	Code    ErrorCode
	Message string
	Hint    string
	Cause   error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// CodeOf reports the ErrorCode carried by err, if any.
func CodeOf(err error) (ErrorCode, bool) {
	var ae *Error
	if errors.As(err, &ae) {
		return ae.Code, true
	}
	return "", false
}
