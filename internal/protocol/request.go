// Package protocol defines the normalized, provider-independent USSD contract
// between USSD Lab and a developer's application.
//
// This package is the innermost layer of the system. It deliberately imports
// nothing transport-specific and nothing provider-specific: no net/http, no
// database driver, no simulator type. That restriction is what guarantees a
// future provider adapter cannot leak provider concerns into the session
// engine -- the compiler enforces it.
//
// See docs/adr/001-modular-monolith.md and
// docs/adr/002-normalized-ussd-protocol.md.
package protocol

import (
	"fmt"
	"strings"
	"time"
)

// NetworkSimulator is the Network value used by the local simulator. Real
// providers will supply their own values ("MTN", "TELECEL", ...).
const NetworkSimulator = "SIMULATOR"

// InputSeparator joins accumulated user inputs, matching the convention used
// by real USSD aggregators: "1*0241234567*100".
const InputSeparator = "*"

// USSDRequest is the normalized request delivered to the developer's
// application. Its JSON encoding is a public compatibility contract: fields
// may be added, but never renamed or removed without a version bump.
//
//	{
//	  "request_id":   "req_01HXYZ",
//	  "session_id":   "sess_01HXYZ",
//	  "service_code": "*124#",
//	  "phone_number": "233240000001",
//	  "network":      "SIMULATOR",
//	  "text":         "1*0241234567"
//	}
type USSDRequest struct {
	// RequestID is unique per HTTP call. It exists so that at-least-once
	// delivery and replay detection are possible later without a schema
	// change (provider design §16).
	RequestID string `json:"request_id"`

	// SessionID is stable for the whole conversation and is always minted by
	// USSD Lab, never adopted from a provider (provider design §28).
	SessionID string `json:"session_id"`

	ServiceCode string `json:"service_code"`
	PhoneNumber string `json:"phone_number"`
	Network     string `json:"network"`

	// Text is the ACCUMULATED user input, joined by InputSeparator. It is
	// empty on the first request of a session. The session engine owns this
	// accumulation so that developer applications stay stateless.
	Text string `json:"text"`

	Timestamp time.Time `json:"timestamp"`

	// Metadata carries provider-specific values. It is the designated escape
	// hatch that keeps provider concerns out of the top-level protocol.
	// Business logic should avoid depending on it (provider design §41).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Inputs splits the accumulated Text back into individual user inputs.
//
// This is lossy when a user's input legitimately contains InputSeparator --
// a limitation shared with real USSD systems and recorded in ADR-002. The
// session engine retains the authoritative input history separately.
func (r *USSDRequest) Inputs() []string {
	if r.Text == "" {
		return nil
	}
	return strings.Split(r.Text, InputSeparator)
}

// IsFirstRequest reports whether this is the initial dial of a session.
func (r *USSDRequest) IsFirstRequest() bool {
	return r.Text == ""
}

// Validate reports whether the request is structurally sound.
//
// Callers validate before dispatch so that a malformed request fails with a
// clear message instead of being sent to the developer's application and
// producing a confusing downstream error.
func (r *USSDRequest) Validate() error {
	if r == nil {
		return fmt.Errorf("request is nil")
	}
	switch {
	case r.RequestID == "":
		return fmt.Errorf("request_id is required")
	case r.SessionID == "":
		return fmt.Errorf("session_id is required")
	case r.ServiceCode == "":
		return fmt.Errorf("service_code is required")
	case r.PhoneNumber == "":
		return fmt.Errorf("phone_number is required")
	case r.Network == "":
		return fmt.Errorf("network is required")
	}
	return nil
}
