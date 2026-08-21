package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// EventStore is the durable record behind `ussd logs`.
type EventStore struct {
	db  *sql.DB
	log *slog.Logger

	// dropped counts events that could not be written. Emit cannot return an
	// error -- a logging failure must never fail a USSD session -- so the
	// count is exposed instead, and `ussd dev` reports it at shutdown. Silently
	// losing history would be worse than either.
	dropped atomic.Int64

	// path is retained so WAL sidecars, which SQLite creates lazily on first
	// write, can have their permissions tightened too.
	path      string
	tightened atomic.Bool
}

var _ session.HistoryStore = (*EventStore)(nil)

// OpenHistory opens (creating if necessary) the history database at path.
func OpenHistory(ctx context.Context, path string, logger *slog.Logger) (*EventStore, error) {
	store, err := Open(ctx, path)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &EventStore{db: store.db, log: logger, path: path}, nil
}

// Close releases the database handle.
func (s *EventStore) Close() error { return s.db.Close() }

// Dropped reports how many events could not be recorded.
func (s *EventStore) Dropped() int { return int(s.dropped.Load()) }

const insertEventSQL = `
INSERT INTO events (event_id, session_id, type, payload, timestamp)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (event_id) DO NOTHING`

// Emit records an event.
//
// It never returns an error and never blocks a session: losing a debug record
// is annoying, dropping a user's transaction because a log write failed is not
// acceptable.
func (s *EventStore) Emit(ctx context.Context, e session.Event) {
	payload := "{}"
	if e.Payload != nil {
		if b, err := json.Marshal(e.Payload); err == nil {
			payload = string(b)
		} else {
			s.fail("encode event payload", e, err)
			return
		}
	}

	if _, err := s.db.ExecContext(ctx, insertEventSQL,
		e.EventID, e.SessionID, string(e.Type), payload, encodeTime(e.Timestamp),
	); err != nil {
		s.fail("write event", e, err)
		return
	}

	// The -wal file appears on the first write, after Open already ran, so it
	// is tightened here -- once.
	if s.tightened.CompareAndSwap(false, true) {
		if err := restrictPermissions(s.path); err != nil {
			s.log.Warn("could not restrict history file permissions",
				slog.String("error", err.Error()))
		}
	}
}

func (s *EventStore) fail(what string, e session.Event, err error) {
	s.dropped.Add(1)
	s.log.Warn("could not record session event",
		slog.String("operation", what),
		slog.String("session_id", e.SessionID),
		slog.String("type", string(e.Type)),
		slog.String("error", err.Error()))
}

const listEventsSQL = `
SELECT event_id, session_id, type, payload, timestamp
FROM events
WHERE session_id = ?
ORDER BY timestamp ASC, rowid ASC`

// ListEvents returns one conversation's events in order.
//
// Ordering falls back to rowid because several events in a fast session can
// share a timestamp; insertion order is then the only correct tiebreak.
func (s *EventStore) ListEvents(ctx context.Context, sessionID string) ([]session.Event, error) {
	rows, err := s.db.QueryContext(ctx, listEventsSQL, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list events for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []session.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

const listSessionsSQL = `
SELECT session_id,
       MIN(timestamp) AS started_at,
       MAX(timestamp) AS ended_at,
       COUNT(*)       AS event_count
FROM events
GROUP BY session_id
ORDER BY started_at DESC
LIMIT ?`

// DefaultHistoryLimit bounds a listing.
const DefaultHistoryLimit = 20

// ListSessions returns recent conversations, newest first.
//
// Summaries are reconstructed from the event log rather than read from a
// sessions table: events are immutable and already record everything, so a
// second source would only add a way for the two to disagree.
func (s *EventStore) ListSessions(ctx context.Context, limit int) ([]session.SessionSummary, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}

	rows, err := s.db.QueryContext(ctx, listSessionsSQL, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	var ids []string
	summaries := make(map[string]*session.SessionSummary)

	for rows.Next() {
		var (
			id                 string
			startedAt, endedAt string
			count              int
		)
		if err := rows.Scan(&id, &startedAt, &endedAt, &count); err != nil {
			return nil, fmt.Errorf("scan session summary: %w", err)
		}

		started, err := decodeTime(startedAt)
		if err != nil {
			return nil, err
		}
		ended, err := decodeTime(endedAt)
		if err != nil {
			return nil, err
		}

		ids = append(ids, id)
		summaries[id] = &session.SessionSummary{
			SessionID:  id,
			Status:     session.StatusActive,
			StartedAt:  started,
			EndedAt:    ended,
			EventCount: count,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Fill in detail from each session's own events. The listing is bounded by
	// limit, so this is a small number of small queries rather than a join
	// that would have to unpack JSON in SQL.
	for _, id := range ids {
		if err := s.enrich(ctx, summaries[id]); err != nil {
			return nil, err
		}
	}

	out := make([]session.SessionSummary, 0, len(ids))
	for _, id := range ids {
		out = append(out, *summaries[id])
	}
	return out, nil
}

// enrich reads a session's events to fill in the fields that only they carry.
func (s *EventStore) enrich(ctx context.Context, sum *session.SessionSummary) error {
	events, err := s.ListEvents(ctx, sum.SessionID)
	if err != nil {
		return err
	}

	for _, e := range events {
		switch e.Type {
		case session.EventSessionStarted:
			sum.ServiceCode = payloadString(e.Payload, "service_code")
			sum.PhoneNumber = payloadString(e.Payload, "phone_number")
			sum.Network = payloadString(e.Payload, "network")

		case session.EventInputReceived:
			sum.InputCount++
		}

		if status, ok := session.TerminalStatusFor(e.Type); ok {
			sum.Status = status
		}
	}
	return nil
}

func payloadString(p map[string]any, key string) string {
	if p == nil {
		return ""
	}
	if v, ok := p[key].(string); ok {
		return v
	}
	return ""
}

// rowScanner covers both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanEvent(r rowScanner) (session.Event, error) {
	var (
		e         session.Event
		typ       string
		payload   string
		timestamp string
	)

	if err := r.Scan(&e.EventID, &e.SessionID, &typ, &payload, &timestamp); err != nil {
		return session.Event{}, fmt.Errorf("scan event: %w", err)
	}

	e.Type = session.EventType(typ)

	if payload != "" && payload != "{}" {
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return session.Event{}, fmt.Errorf("decode event payload: %w", err)
		}
	}

	ts, err := decodeTime(timestamp)
	if err != nil {
		return session.Event{}, err
	}
	e.Timestamp = ts

	return e, nil
}

// Prune deletes events older than cutoff, so a long-lived project's history
// file does not grow without bound.
func (s *EventStore) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM events WHERE timestamp < ?", encodeTime(cutoff))
	if err != nil {
		return 0, fmt.Errorf("prune events: %w", err)
	}
	return res.RowsAffected()
}
