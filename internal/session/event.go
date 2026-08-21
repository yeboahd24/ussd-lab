package session

import (
	"context"
	"sync"
	"time"
)

// EventType enumerates observable session events (MVP design §14).
type EventType string

const (
	EventSessionStarted      EventType = "SESSION_STARTED"
	EventInputReceived       EventType = "INPUT_RECEIVED"
	EventApplicationRequest  EventType = "APPLICATION_REQUEST"
	EventApplicationResponse EventType = "APPLICATION_RESPONSE"
	EventSessionCompleted    EventType = "SESSION_COMPLETED"
	EventSessionCancelled    EventType = "SESSION_CANCELLED"
	EventSessionTimeout      EventType = "SESSION_TIMEOUT"
	EventApplicationError    EventType = "APPLICATION_ERROR"
)

// Event is an immutable record of something that happened in a session.
//
// Events are the substrate for the CLI session debugger (MVP design §15),
// `ussd logs`, and the test runner's assertions. Because all three read the
// same events, they cannot disagree about what occurred.
type Event struct {
	EventID   string         `json:"event_id"`
	SessionID string         `json:"session_id"`
	Type      EventType      `json:"type"`
	Payload   map[string]any `json:"payload,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// EventSink receives session events.
//
// Emit returns no error on purpose. A sink failure must never fail a USSD
// session: losing a debug record is annoying, dropping a user's transaction
// because a log write failed is not acceptable. Sinks own their failure
// handling.
type EventSink interface {
	Emit(ctx context.Context, e Event)
}

// NopSink discards events.
type NopSink struct{}

func (NopSink) Emit(context.Context, Event) {}

// MemorySink retains events in order. It backs tests today and the CLI's live
// session view later.
type MemorySink struct {
	mu     sync.RWMutex
	events []Event
}

func NewMemorySink() *MemorySink { return &MemorySink{} }

func (s *MemorySink) Emit(_ context.Context, e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, e)
}

// Events returns a copy of everything recorded.
func (s *MemorySink) Events() []Event {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Event, len(s.events))
	copy(out, s.events)
	return out
}

// Types returns the recorded event types in order, which is what most
// assertions actually care about.
func (s *MemorySink) Types() []EventType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]EventType, len(s.events))
	for i, e := range s.events {
		out[i] = e.Type
	}
	return out
}

// MultiSink fans events out to several sinks.
type MultiSink []EventSink

func (m MultiSink) Emit(ctx context.Context, e Event) {
	for _, s := range m {
		s.Emit(ctx, e)
	}
}

// RedactingSink removes user input before events reach a durable sink.
//
// The event log records what the user typed, because that is what makes the
// session debugger useful. But a developer's application may ask for a PIN, and
// USSD Lab cannot tell a PIN from a menu choice -- both are just digits. So the
// live terminal view keeps full input, while a sink that writes to disk can be
// wrapped in this, keeping secrets out of a file that outlives the session
// (MVP design §22, provider design §38).
type RedactingSink struct {
	Inner EventSink
}

// RedactedText replaces user input in a redacted event.
const RedactedText = "[REDACTED]"

// NewRedactingSink wraps inner so that recorded input is replaced.
func NewRedactingSink(inner EventSink) *RedactingSink {
	return &RedactingSink{Inner: inner}
}

func (s *RedactingSink) Emit(ctx context.Context, e Event) {
	if e.Type == EventInputReceived && e.Payload != nil {
		// Copy the payload: events are shared with other sinks, and mutating
		// one in place would redact the live terminal view too.
		payload := make(map[string]any, len(e.Payload))
		for k, v := range e.Payload {
			payload[k] = v
		}
		if _, ok := payload["text"]; ok {
			payload["text"] = RedactedText
		}
		e.Payload = payload
	}
	s.Inner.Emit(ctx, e)
}
