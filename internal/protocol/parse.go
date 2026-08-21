package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxResponseBytes bounds an application response.
//
// A real USSD screen holds roughly 182 characters; this limit is generous
// enough for multi-page menus while preventing a runaway application from
// exhausting memory (MVP design §22).
const MaxResponseBytes = 8 << 10 // 8 KiB

// ParseResponse converts a raw application response into a USSDResponse.
//
// The expected form is a keyword, whitespace, then the message:
//
//	CON Enter recipient number:
//	END Transaction successful.
//
// Parsing is deliberately STRICT about the keyword: it must be uppercase CON
// or END. Real gateways are strict here, and a simulator that accepted "con"
// would let a developer ship an application that works locally and fails in
// production. Fidelity to the real protocol matters more than convenience
// (MVP design §30).
//
// Parsing is deliberately LENIENT about surrounding whitespace, because
// trailing newlines are an artefact of nearly every web framework and carry no
// meaning. Whitespace *inside* the message is preserved, since multi-line menus
// depend on it.
func ParseResponse(raw []byte) (USSDResponse, error) {
	if len(raw) > MaxResponseBytes {
		return USSDResponse{}, newParseError(
			CodeTooLarge,
			"response exceeds the maximum size",
			string(raw[:min(len(raw), snippetLimit)]),
		)
	}

	if !utf8.Valid(raw) {
		return USSDResponse{}, newParseError(
			CodeInvalidEncoding,
			"response is not valid UTF-8",
			string(raw),
		)
	}

	s := strings.TrimSpace(string(raw))
	if s == "" {
		return USSDResponse{}, newParseError(
			CodeEmptyResponse,
			`application returned an empty response; expected "CON <text>" or "END <text>"`,
			"",
		)
	}

	keyword, text := splitKeyword(s)

	rt := ResponseType(keyword)
	if !rt.Valid() {
		return USSDResponse{}, newParseError(
			CodeUnknownType,
			`response must begin with "CON " or "END "`,
			s,
		)
	}

	if text == "" {
		return USSDResponse{}, newParseError(
			CodeEmptyText,
			"response has a valid keyword but no message text",
			s,
		)
	}

	return USSDResponse{Type: rt, Text: text}, nil
}

// splitKeyword divides s at the first run of whitespace.
//
// It does not use strings.Fields, which would collapse the internal newlines
// that multi-line USSD menus rely on.
func splitKeyword(s string) (keyword, text string) {
	i := strings.IndexFunc(s, unicode.IsSpace)
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i:])
}
