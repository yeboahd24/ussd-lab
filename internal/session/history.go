package session

import (
	"context"
	"time"
)

// SessionSummary describes one past conversation, reconstructed from its
// events.
//
// History is derived from the event log rather than stored separately. Events
// are immutable and already record everything that happened (provider design
// §30), so a second write path for session snapshots would add a way for the
// two to disagree without adding any information.
type SessionSummary struct {
	SessionID   string
	ServiceCode string
	PhoneNumber string
	Network     string

	// Status is derived from the terminating event, or ACTIVE if none was
	// recorded -- which in practice means the process stopped mid-session.
	Status Status

	StartedAt  time.Time
	EndedAt    time.Time
	EventCount int
	InputCount int
}

// Duration is how long the conversation lasted.
func (s SessionSummary) Duration() time.Duration {
	if s.EndedAt.IsZero() || s.EndedAt.Before(s.StartedAt) {
		return 0
	}
	return s.EndedAt.Sub(s.StartedAt)
}

// HistoryStore records events durably and reads them back.
//
// It is deliberately separate from SessionStore. The two hold data with
// different lifetimes: live session state is worthless two minutes after it is
// written, while history is the whole point of `ussd logs` (ADR-003). Keeping
// them apart means the live store stays a four-method interface that Redis can
// implement later, while history stays queryable.
type HistoryStore interface {
	EventSink

	// ListSessions returns recent conversations, newest first.
	ListSessions(ctx context.Context, limit int) ([]SessionSummary, error)

	// ListEvents returns one conversation's events in order.
	ListEvents(ctx context.Context, sessionID string) ([]Event, error)
}

// TerminalStatusFor maps a terminating event type to the status it implies.
//
// The mapping lives here, next to the event types, so a new terminal event
// cannot be added without a decision about which status it represents.
func TerminalStatusFor(t EventType) (Status, bool) {
	switch t {
	case EventSessionCompleted:
		return StatusCompleted, true
	case EventSessionCancelled:
		return StatusCancelled, true
	case EventSessionTimeout:
		return StatusTimeout, true
	case EventApplicationError:
		return StatusError, true
	default:
		return "", false
	}
}
