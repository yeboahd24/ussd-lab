package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
	"github.com/yeboahd24/ussd-lab/internal/storage/sqlite"
	"github.com/yeboahd24/ussd-lab/internal/storage/storagetest"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := sqlite.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The identical suite that validates the memory store. It is deliberately
// unmodified: if SQLite needed the spec relaxed, that would be a finding about
// the abstraction, not a reason to edit the tests (ADR-003).
func TestSQLiteStore_Conformance(t *testing.T) {
	t.Parallel()

	storagetest.Run(t, "sqlite", func(t *testing.T) session.SessionStore {
		return newTestStore(t)
	})
}

// The reason SQLite exists at all: state must survive process restart, which is
// what makes `ussd logs` useful after the fact.
func TestSQLiteStore_SurvivesReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	want := &session.Session{
		ID:          "sess_persist",
		ProjectID:   "my-fintech",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
		Status:      session.StatusCompleted,
		Inputs:      []string{"1", "0241234567", "100"},
		CreatedAt:   now,
		UpdatedAt:   now.Add(12 * time.Second),
		ExpiresAt:   now.Add(120 * time.Second),
	}

	first, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := first.Create(ctx, want); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer second.Close()

	got, err := second.Get(ctx, "sess_persist")
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}

	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.Text() != "1*0241234567*100" {
		t.Errorf("Text() = %q", got.Text())
	}
	if !got.CreatedAt.Equal(want.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", got.CreatedAt, want.CreatedAt)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) {
		t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, want.ExpiresAt)
	}
}

// Reopening must be idempotent: migrations run again against a populated file.
func TestSQLiteStore_MigrationIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")

	for i := 0; i < 3; i++ {
		store, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatalf("Open() attempt %d error = %v", i, err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close() attempt %d error = %v", i, err)
		}
	}
}

// A zero ExpiresAt must round-trip as zero, not as year 1.
func TestSQLiteStore_ZeroTimeRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)

	s := &session.Session{
		ID:          "sess_zero",
		ProjectID:   "demo",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
		Status:      session.StatusActive,
		CreatedAt:   time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC),
	}
	if err := store.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.Get(ctx, "sess_zero")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !got.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero", got.ExpiresAt)
	}
	if got.IsExpired(time.Now()) {
		t.Error("a session with no expiry must never be considered expired")
	}
}

// Inputs are stored as JSON precisely so element boundaries survive; a
// delimited encoding would reintroduce the ADR-002 ambiguity in storage.
func TestSQLiteStore_InputsWithSeparatorSurvive(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)

	s := &session.Session{
		ID:          "sess_sep",
		ProjectID:   "demo",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
		Status:      session.StatusActive,
		Inputs:      []string{"1", "a*b", "c"},
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.Get(ctx, "sess_sep")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Inputs) != 3 || got.Inputs[1] != "a*b" {
		t.Errorf("Inputs = %v, want [1 a*b c] with boundaries preserved", got.Inputs)
	}
}

func TestSQLiteStore_OpenBadPath(t *testing.T) {
	t.Parallel()

	_, err := sqlite.Open(context.Background(), "/nonexistent-dir-xyz/sessions.db")
	if err == nil {
		t.Error("Open() error = nil, want error for an unwritable path")
	}
}

// Sanity check that the suite's sentinel expectations hold for this driver.
func TestSQLiteStore_Sentinels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTestStore(t)

	if _, err := store.Get(ctx, "nope"); !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Get() error = %v, want ErrSessionNotFound", err)
	}
}
