package session

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"
)

// Identifier prefixes. Prefixed IDs make it obvious in a log line which kind
// of object an identifier refers to.
const (
	sessionIDPrefix = "sess_"
	requestIDPrefix = "req_"
	eventIDPrefix   = "evt_"
)

// idEntropyBytes yields 16 base32 characters, ~80 bits of entropy.
//
// These identifiers are not merely labels: a session ID is presented to the
// browser, so guessing one must be infeasible. They are therefore generated
// with crypto/rand, never math/rand.
const idEntropyBytes = 10

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewSessionID mints a platform session identifier.
//
// USSD Lab always mints its own session IDs. A provider's session identifier
// will be mapped to one of these, never adopted as the primary key
// (provider design §28).
func NewSessionID() string { return newID(sessionIDPrefix) }

// NewRequestID mints an identifier for a single call to the application.
func NewRequestID() string { return newID(requestIDPrefix) }

// NewEventID mints an identifier for a session event.
func NewEventID() string { return newID(eventIDPrefix) }

func newID(prefix string) string {
	b := make([]byte, idEntropyBytes)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the system entropy source is broken.
		// Continuing would emit guessable identifiers, so fail loudly.
		panic(fmt.Sprintf("session: cannot read random bytes: %v", err))
	}
	return prefix + strings.ToLower(idEncoding.EncodeToString(b))
}
