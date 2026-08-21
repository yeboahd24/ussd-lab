package protocol

import (
	"context"
	"fmt"
)

// ResponseType is the USSD continuation keyword returned by an application.
type ResponseType string

const (
	// TypeCON continues the session: the phone shows the text and waits for
	// further input.
	TypeCON ResponseType = "CON"

	// TypeEND terminates the session: the phone shows the text and closes.
	TypeEND ResponseType = "END"
)

// Valid reports whether t is a supported response type.
func (t ResponseType) Valid() bool {
	return t == TypeCON || t == TypeEND
}

func (t ResponseType) String() string { return string(t) }

// USSDResponse is the normalized reply from a developer's application.
type USSDResponse struct {
	Type ResponseType `json:"type"`
	Text string       `json:"text"`
}

// Continue builds a session-continuing response.
func Continue(text string) USSDResponse {
	return USSDResponse{Type: TypeCON, Text: text}
}

// End builds a session-terminating response.
func End(text string) USSDResponse {
	return USSDResponse{Type: TypeEND, Text: text}
}

// IsFinal reports whether this response ends the session.
func (r USSDResponse) IsFinal() bool { return r.Type == TypeEND }

// Wire renders the response in the on-the-wire form a developer application
// would return: "CON Enter amount:".
//
// The simulator uses this to echo responses; a future provider adapter would
// instead format into that provider's required encoding.
func (r USSDResponse) Wire() string {
	return fmt.Sprintf("%s %s", r.Type, r.Text)
}

func (r USSDResponse) String() string { return r.Wire() }

// ApplicationClient sends a normalized request to the developer's application
// and returns the normalized response.
//
// The interface lives here, alongside the types it exchanges, because both the
// session engine and the test runner consume it. Implementations live in outer
// layers (internal/appclient), so inner packages never depend on net/http.
type ApplicationClient interface {
	Send(ctx context.Context, req *USSDRequest) (*USSDResponse, error)
}
