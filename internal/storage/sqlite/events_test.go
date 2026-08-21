package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
)

func newHistory(t *testing.T) *sqlite.EventStore {
	t.Helper()

	store, err := sqlite.OpenHistory(context.Background(),
		filepath.Join(t.TempDir(), "history.db"), nil)
	if err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// conversation emits a full session's worth of events.
func conversation(ctx context.Context, s session.HistoryStore, id string, base time.Time, final session.EventType) {
	at := func(n int) time.Time { return base.Add(time.Duration(n) * time.Second) }

	s.Emit(ctx, session.Event{
		EventID: id + "-1", SessionID: id, Type: session.EventSessionStarted,
		Timestamp: at(0),
		Payload: map[string]any{
			"service_code": "*124#",
			"phone_number": "233240000001",
			"network":      protocol.NetworkSimulator,
		},
	})
	s.Emit(ctx, session.Event{
		EventID: id + "-2", SessionID: id, Type: session.EventApplicationResponse,
		Timestamp: at(0), Payload: map[string]any{"type": "CON", "text": "Welcome"},
	})
	s.Emit(ctx, session.Event{
		EventID: id + "-3", SessionID: id, Type: session.EventInputReceived,
		Timestamp: at(4), Payload: map[string]any{"text": "1"},
	})
	s.Emit(ctx, session.Event{
		EventID: id + "-4", SessionID: id, Type: final, Timestamp: at(6),
	})
}

func TestEventStore_RoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	conversation(ctx, store, "sess_1", base, session.EventSessionCompleted)

	events, err := store.ListEvents(ctx, "sess_1")
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4", len(events))
	}

	if events[0].Type != session.EventSessionStarted {
		t.Errorf("first event = %s, want SESSION_STARTED", events[0].Type)
	}
	if got := events[0].Payload["service_code"]; got != "*124#" {
		t.Errorf("service_code = %v", got)
	}
	if !events[0].Timestamp.Equal(base) {
		t.Errorf("timestamp = %v, want %v", events[0].Timestamp, base)
	}
	if store.Dropped() != 0 {
		t.Errorf("dropped = %d, want 0", store.Dropped())
	}
}

// Summaries are reconstructed from events alone, so every field must be
// derivable from what the engine emits.
func TestEventStore_ListSessions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	conversation(ctx, store, "sess_old", base, session.EventSessionCompleted)
	conversation(ctx, store, "sess_new", base.Add(time.Hour), session.EventSessionCancelled)

	sessions, err := store.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}

	// Newest first.
	if sessions[0].SessionID != "sess_new" {
		t.Errorf("first = %s, want sess_new", sessions[0].SessionID)
	}

	got := sessions[1]
	if got.ServiceCode != "*124#" {
		t.Errorf("ServiceCode = %q", got.ServiceCode)
	}
	if got.PhoneNumber != "233240000001" {
		t.Errorf("PhoneNumber = %q", got.PhoneNumber)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %s, want COMPLETED", got.Status)
	}
	if got.InputCount != 1 {
		t.Errorf("InputCount = %d, want 1", got.InputCount)
	}
	if got.EventCount != 4 {
		t.Errorf("EventCount = %d, want 4", got.EventCount)
	}
	if got.Duration() != 6*time.Second {
		t.Errorf("Duration = %v, want 6s", got.Duration())
	}

	if sessions[0].Status != session.StatusCancelled {
		t.Errorf("Status = %s, want CANCELLED", sessions[0].Status)
	}
}

func TestEventStore_StatusDerivation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		event session.EventType
		want  session.Status
	}{
		{session.EventSessionCompleted, session.StatusCompleted},
		{session.EventSessionCancelled, session.StatusCancelled},
		{session.EventSessionTimeout, session.StatusTimeout},
		{session.EventApplicationError, session.StatusError},
	}

	for _, tt := range tests {
		t.Run(string(tt.event), func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			store := newHistory(t)
			conversation(ctx, store, "sess_1",
				time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC), tt.event)

			sessions, err := store.ListSessions(ctx, 10)
			if err != nil {
				t.Fatalf("ListSessions() error = %v", err)
			}
			if sessions[0].Status != tt.want {
				t.Errorf("Status = %s, want %s", sessions[0].Status, tt.want)
			}
		})
	}
}

// A session with no terminal event means the process stopped mid-conversation.
func TestEventStore_UnfinishedSessionStaysActive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)

	store.Emit(ctx, session.Event{
		EventID: "e1", SessionID: "sess_1", Type: session.EventSessionStarted,
		Timestamp: time.Now().UTC(),
		Payload:   map[string]any{"service_code": "*124#"},
	})

	sessions, err := store.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if sessions[0].Status != session.StatusActive {
		t.Errorf("Status = %s, want ACTIVE", sessions[0].Status)
	}
}

func TestEventStore_LimitAndOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	for i := 0; i < 5; i++ {
		conversation(ctx, store,
			string(rune('a'+i))+"_sess",
			base.Add(time.Duration(i)*time.Hour),
			session.EventSessionCompleted)
	}

	sessions, err := store.ListSessions(ctx, 2)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("got %d sessions, want 2", len(sessions))
	}
	if !sessions[0].StartedAt.After(sessions[1].StartedAt) {
		t.Error("sessions are not newest-first")
	}
}

// Several events in a fast session share a timestamp; insertion order is then
// the only correct tiebreak.
func TestEventStore_StableOrderWithinTimestamp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	want := []session.EventType{
		session.EventSessionStarted,
		session.EventApplicationRequest,
		session.EventApplicationResponse,
		session.EventSessionCompleted,
	}
	for i, typ := range want {
		store.Emit(ctx, session.Event{
			EventID:   string(rune('a' + i)),
			SessionID: "sess_1", Type: typ, Timestamp: now,
		})
	}

	events, err := store.ListEvents(ctx, "sess_1")
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	for i := range want {
		if events[i].Type != want[i] {
			t.Errorf("event %d = %s, want %s", i, events[i].Type, want[i])
		}
	}
}

// Emitting the same event twice must not duplicate it: at-least-once delivery
// is the assumption everywhere else in the system.
func TestEventStore_IdempotentEmit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	e := session.Event{
		EventID: "evt_1", SessionID: "sess_1",
		Type: session.EventSessionStarted, Timestamp: time.Now().UTC(),
	}

	store.Emit(ctx, e)
	store.Emit(ctx, e)

	events, err := store.ListEvents(ctx, "sess_1")
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 1 {
		t.Errorf("got %d events, want 1", len(events))
	}
}

func TestEventStore_SurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	first, err := sqlite.OpenHistory(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	conversation(ctx, first, "sess_1", base, session.EventSessionCompleted)
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := sqlite.OpenHistory(ctx, path, nil)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer second.Close()

	sessions, err := second.ListSessions(ctx, 10)
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Status != session.StatusCompleted {
		t.Errorf("history did not survive reopen: %+v", sessions)
	}
}

func TestEventStore_Prune(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newHistory(t)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	conversation(ctx, store, "sess_old", base, session.EventSessionCompleted)
	conversation(ctx, store, "sess_new", base.Add(48*time.Hour), session.EventSessionCompleted)

	n, err := store.Prune(ctx, base.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if n != 4 {
		t.Errorf("pruned %d events, want 4", n)
	}

	sessions, _ := store.ListSessions(ctx, 10)
	if len(sessions) != 1 || sessions[0].SessionID != "sess_new" {
		t.Errorf("wrong session pruned: %+v", sessions)
	}
}

func TestEventStore_UnknownSession(t *testing.T) {
	t.Parallel()

	events, err := newHistory(t).ListEvents(context.Background(), "sess_nope")
	if err != nil {
		t.Fatalf("ListEvents() error = %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events, want 0", len(events))
	}
}

// The schema must migrate from v1 to v2 without losing existing sessions.
func TestEventStore_MigratesExistingDatabase(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "existing.db")

	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Create(ctx, &session.Session{
		ID: "sess_1", ProjectID: "p", ServiceCode: "*124#",
		PhoneNumber: "233240000001", Network: protocol.NetworkSimulator,
		Status: session.StatusActive, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	history, err := sqlite.OpenHistory(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenHistory() on existing database error = %v", err)
	}
	defer history.Close()

	history.Emit(ctx, session.Event{
		EventID: "e1", SessionID: "sess_1",
		Type: session.EventSessionStarted, Timestamp: time.Now().UTC(),
	})
	if history.Dropped() != 0 {
		t.Errorf("dropped = %d after migration", history.Dropped())
	}
}

// History files hold phone numbers and raw user input, so they must not be
// readable by other users on a shared machine.
func TestEventStore_FilePermissions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.db")

	store, err := sqlite.OpenHistory(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenHistory() error = %v", err)
	}
	defer store.Close()

	store.Emit(ctx, session.Event{
		EventID: "e1", SessionID: "sess_1",
		Type: session.EventSessionStarted, Timestamp: time.Now().UTC(),
	})

	// The main file and the WAL sidecar both carry the data.
	for _, p := range []string{path, path + "-wal"} {
		info, err := os.Stat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("Stat(%s): %v", p, err)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			t.Errorf("%s has mode %o; group/other must have no access", p, mode)
		}
	}
}

func TestStore_FilePermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("%s has mode %o; group/other must have no access", path, mode)
	}
}
