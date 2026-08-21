package protocol

import (
	"fmt"
	"strings"
	"unicode"
)

// ParseErrorCode classifies a malformed application response.
//
// Codes exist so that callers -- and the CLI's diagnostics -- can branch on the
// kind of failure rather than matching error strings.
type ParseErrorCode string

const (
	// CodeEmptyResponse: the application returned nothing at all.
	CodeEmptyResponse ParseErrorCode = "EMPTY_RESPONSE"

	// CodeUnknownType: the first token was neither CON nor END.
	CodeUnknownType ParseErrorCode = "UNKNOWN_RESPONSE_TYPE"

	// CodeEmptyText: a valid keyword with no message after it.
	CodeEmptyText ParseErrorCode = "EMPTY_RESPONSE_TEXT"

	// CodeTooLarge: the response exceeded MaxResponseBytes.
	CodeTooLarge ParseErrorCode = "RESPONSE_TOO_LARGE"

	// CodeInvalidEncoding: the response was not valid UTF-8.
	CodeInvalidEncoding ParseErrorCode = "INVALID_ENCODING"
)

// ParseError describes an unparseable application response.
type ParseError struct {
	Code ParseErrorCode

	// Message is the developer-facing explanation.
	Message string

	// Snippet is a short, sanitized excerpt of what the application actually
	// returned. It is sanitized because it is echoed into a terminal and a
	// browser, and an application could otherwise emit ANSI escapes or control
	// characters into the developer's session (see MVP design §22).
	Snippet string
}

func (e *ParseError) Error() string {
	if e.Snippet == "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s (got %q)", e.Code, e.Message, e.Snippet)
}

// newParseError builds a ParseError with a sanitized snippet of raw.
func newParseError(code ParseErrorCode, message, raw string) *ParseError {
	return &ParseError{
		Code:    code,
		Message: message,
		Snippet: sanitizeSnippet(raw),
	}
}

// snippetLimit bounds how much of a bad response is echoed back.
const snippetLimit = 80

// sanitizeSnippet makes untrusted application output safe to display.
//
// Control characters -- including ANSI escape introducers -- are replaced, and
// the result is truncated. An application is not a trusted source of terminal
// output.
func sanitizeSnippet(raw string) string {
	if raw == "" {
		return ""
	}

	var b strings.Builder
	count := 0

	for _, r := range raw {
		if count >= snippetLimit {
			b.WriteString("...")
			break
		}
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r) || !unicode.IsPrint(r):
			b.WriteByte('?')
		default:
			b.WriteRune(r)
		}
		count++
	}

	return strings.TrimSpace(b.String())
}
