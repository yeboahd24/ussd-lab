// Package session owns USSD session lifecycle.
//
// The engine is the only component permitted to change a session's status or
// accumulate user input. Transport layers (the simulator, and later provider
// adapters) hand it normalized input and render what it returns; they contain
// no session logic of their own.
//
// This package deliberately has no HTTP and no SQL dependency. Persistence is
// reached through SessionStore and the developer's application through
// protocol.ApplicationClient, so a complete session can be driven in a unit
// test with no server, no port and no browser (MVP design §19).
package session

import (
	"strings"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
)

// Status is the lifecycle state of a session (MVP design §12).
type Status string

const (
	// StatusActive: the session is running and awaiting input.
	StatusActive Status = "ACTIVE"

	// StatusCompleted: the application returned END.
	StatusCompleted Status = "COMPLETED"

	// StatusCancelled: the user abandoned the session.
	StatusCancelled Status = "CANCELLED"

	// StatusTimeout: the session expired before the next input.
	StatusTimeout Status = "TIMEOUT"

	// StatusError: the application could not be reached or misbehaved.
	StatusError Status = "ERROR"
)

// IsTerminal reports whether no further input is accepted.
func (s Status) IsTerminal() bool {
	return s != StatusActive
}

func (s Status) String() string { return string(s) }

// Session is a single USSD conversation.
//
// It is a plain value type with no behaviour that reaches outside itself, so
// it can be copied freely between the engine and a store without either side
// holding a reference to the other's state.
type Session struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id"`
	ServiceCode string `json:"service_code"`
	PhoneNumber string `json:"phone_number"`
	Network     string `json:"network"`
	Status      Status `json:"status"`

	// Inputs is the authoritative, ordered history of what the user entered.
	//
	// The wire protocol transmits these joined by protocol.InputSeparator,
	// which is lossy when an input itself contains "*" (ADR-002). Keeping the
	// list here means the session record never suffers that ambiguity, even
	// though the request sent to the application does.
	Inputs []string `json:"inputs"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Text renders the accumulated input in wire form: "1*0241234567*100".
//
// This is the value placed in USSDRequest.Text. It corresponds to the
// current_input field in MVP design §12.
func (s *Session) Text() string {
	return strings.Join(s.Inputs, protocol.InputSeparator)
}

// IsExpired reports whether the session has passed its expiry instant.
//
// Expiry is evaluated against an injected clock rather than time.Now so that
// timeout behaviour is testable without sleeping.
func (s *Session) IsExpired(now time.Time) bool {
	return !s.ExpiresAt.IsZero() && !now.Before(s.ExpiresAt)
}

// Clone returns a deep copy.
//
// Stores and the engine exchange clones so that a caller mutating a returned
// session cannot corrupt stored state. An in-memory store that skipped this
// would appear to work while behaving differently from a SQLite store, which
// is exactly the divergence the storagetest conformance suite exists to catch.
func (s *Session) Clone() *Session {
	if s == nil {
		return nil
	}
	out := *s
	if s.Inputs != nil {
		out.Inputs = make([]string, len(s.Inputs))
		copy(out.Inputs, s.Inputs)
	}
	return &out
}
