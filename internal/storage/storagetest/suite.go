// Package storagetest is an executable specification for session.SessionStore.
//
// Every store implementation runs this identical suite. An interface with one
// implementation is a guess, shaped by that implementation's accidents; an
// interface whose implementations all satisfy one suite is a verified
// abstraction. When Redis arrives it passes this suite or it is not correct
// (ADR-003, and the same technique provider design §44 prescribes for adapter
// contract tests).
package storagetest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/yeboahd24/ussd-lab/internal/protocol"
	"github.com/yeboahd24/ussd-lab/internal/session"
)

// Factory builds a fresh, empty store for one test.
type Factory func(t *testing.T) session.SessionStore

// Run executes the full conformance suite against the store built by newStore.
func Run(t *testing.T, name string, newStore Factory) {
	t.Helper()

	t.Run(name, func(t *testing.T) {
		t.Run("CreateThenGet", func(t *testing.T) { testCreateThenGet(t, newStore) })
		t.Run("GetMissing", func(t *testing.T) { testGetMissing(t, newStore) })
		t.Run("CreateDuplicate", func(t *testing.T) { testCreateDuplicate(t, newStore) })
		t.Run("Update", func(t *testing.T) { testUpdate(t, newStore) })
		t.Run("UpdateMissing", func(t *testing.T) { testUpdateMissing(t, newStore) })
		t.Run("Delete", func(t *testing.T) { testDelete(t, newStore) })
		t.Run("DeleteMissing", func(t *testing.T) { testDeleteMissing(t, newStore) })
		t.Run("GetReturnsIndependentCopy", func(t *testing.T) { testGetIsolation(t, newStore) })
		t.Run("CreateStoresIndependentCopy", func(t *testing.T) { testCreateIsolation(t, newStore) })
		t.Run("RoundTripsAllFields", func(t *testing.T) { testRoundTrip(t, newStore) })
		t.Run("EmptyAndNilInputs", func(t *testing.T) { testInputEdgeCases(t, newStore) })
		t.Run("ConcurrentAccess", func(t *testing.T) { testConcurrency(t, newStore) })
	})
}

// fixture builds a fully populated session.
//
// Times are truncated to milliseconds so that a store persisting through a
// serialisation format with limited precision is still expected to round-trip
// exactly -- the assertion stays meaningful rather than being loosened later.
func fixture(id string) *session.Session {
	now := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC).Truncate(time.Millisecond)
	return &session.Session{
		ID:          id,
		ProjectID:   "my-fintech",
		ServiceCode: "*124#",
		PhoneNumber: "233240000001",
		Network:     protocol.NetworkSimulator,
		Status:      session.StatusActive,
		Inputs:      []string{"1", "0241234567"},
		CreatedAt:   now,
		UpdatedAt:   now,
		ExpiresAt:   now.Add(120 * time.Second),
	}
}

func assertEqual(t *testing.T, got, want *session.Session) {
	t.Helper()

	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.ProjectID != want.ProjectID {
		t.Errorf("ProjectID = %q, want %q", got.ProjectID, want.ProjectID)
	}
	if got.ServiceCode != want.ServiceCode {
		t.Errorf("ServiceCode = %q, want %q", got.ServiceCode, want.ServiceCode)
	}
	if got.PhoneNumber != want.PhoneNumber {
		t.Errorf("PhoneNumber = %q, want %q", got.PhoneNumber, want.PhoneNumber)
	}
	if got.Network != want.Network {
		t.Errorf("Network = %q, want %q", got.Network, want.Network)
	}
	if got.Status != want.Status {
		t.Errorf("Status = %q, want %q", got.Status, want.Status)
	}
	if got.Text() != want.Text() {
		t.Errorf("Text() = %q, want %q", got.Text(), want.Text())
	}
	if len(got.Inputs) != len(want.Inputs) {
		t.Errorf("Inputs = %v, want %v", got.Inputs, want.Inputs)
	}
	assertTimeEqual(t, "CreatedAt", got.CreatedAt, want.CreatedAt)
	assertTimeEqual(t, "UpdatedAt", got.UpdatedAt, want.UpdatedAt)
	assertTimeEqual(t, "ExpiresAt", got.ExpiresAt, want.ExpiresAt)
}

func assertTimeEqual(t *testing.T, field string, got, want time.Time) {
	t.Helper()
	if !got.Equal(want) {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}

func testCreateThenGet(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)
	want := fixture("sess_1")

	if err := store.Create(ctx, want); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.Get(ctx, "sess_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	assertEqual(t, got, want)
}

func testGetMissing(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	_, err := store.Get(ctx, "sess_nope")
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Get() error = %v, want ErrSessionNotFound", err)
	}
}

func testCreateDuplicate(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.Create(ctx, fixture("sess_1")); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}

	err := store.Create(ctx, fixture("sess_1"))
	if !errors.Is(err, session.ErrSessionExists) {
		t.Errorf("duplicate Create() error = %v, want ErrSessionExists", err)
	}
}

func testUpdate(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	s := fixture("sess_1")
	if err := store.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	s.Status = session.StatusCompleted
	s.Inputs = append(s.Inputs, "100")
	if err := store.Update(ctx, s); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	got, err := store.Get(ctx, "sess_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != session.StatusCompleted {
		t.Errorf("Status = %q, want COMPLETED", got.Status)
	}
	if got.Text() != "1*0241234567*100" {
		t.Errorf("Text() = %q, want %q", got.Text(), "1*0241234567*100")
	}
}

func testUpdateMissing(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	err := store.Update(ctx, fixture("sess_ghost"))
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Update() error = %v, want ErrSessionNotFound", err)
	}
}

func testDelete(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.Create(ctx, fixture("sess_1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := store.Delete(ctx, "sess_1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err := store.Get(ctx, "sess_1")
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Get() after Delete error = %v, want ErrSessionNotFound", err)
	}
}

func testDeleteMissing(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	err := store.Delete(ctx, "sess_ghost")
	if !errors.Is(err, session.ErrSessionNotFound) {
		t.Errorf("Delete() error = %v, want ErrSessionNotFound", err)
	}
}

// A store must hand back a copy. If it returns a pointer into its own state, a
// caller can mutate "persisted" data without calling Update -- something no
// database-backed store could reproduce, so the abstraction would leak.
func testGetIsolation(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	if err := store.Create(ctx, fixture("sess_1")); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	got, err := store.Get(ctx, "sess_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	got.Status = session.StatusCancelled
	got.PhoneNumber = "tampered"
	if len(got.Inputs) > 0 {
		got.Inputs[0] = "tampered"
	}

	again, err := store.Get(ctx, "sess_1")
	if err != nil {
		t.Fatalf("second Get() error = %v", err)
	}

	if again.Status != session.StatusActive {
		t.Errorf("Status = %q, want ACTIVE: mutating a returned session altered the store",
			again.Status)
	}
	if again.PhoneNumber != "233240000001" {
		t.Errorf("PhoneNumber = %q: store returned a live reference", again.PhoneNumber)
	}
	if len(again.Inputs) > 0 && again.Inputs[0] != "1" {
		t.Errorf("Inputs[0] = %q: store shares the input slice with callers", again.Inputs[0])
	}
}

// The mirror of the above: mutating the session you passed to Create must not
// alter what was stored.
func testCreateIsolation(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	s := fixture("sess_1")
	if err := store.Create(ctx, s); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	s.Status = session.StatusCancelled
	s.Inputs[0] = "tampered"

	got, err := store.Get(ctx, "sess_1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != session.StatusActive {
		t.Errorf("Status = %q, want ACTIVE: store retained the caller's pointer", got.Status)
	}
	if got.Inputs[0] != "1" {
		t.Errorf("Inputs[0] = %q, want %q", got.Inputs[0], "1")
	}
}

func testRoundTrip(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	for _, status := range []session.Status{
		session.StatusActive,
		session.StatusCompleted,
		session.StatusCancelled,
		session.StatusTimeout,
		session.StatusError,
	} {
		t.Run(string(status), func(t *testing.T) {
			id := "sess_" + string(status)
			want := fixture(id)
			want.Status = status

			if err := store.Create(ctx, want); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			got, err := store.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			assertEqual(t, got, want)
		})
	}
}

func testInputEdgeCases(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	cases := []struct {
		name   string
		inputs []string
		want   string
	}{
		{"nil inputs", nil, ""},
		{"empty slice", []string{}, ""},
		{"single input", []string{"1"}, "1"},
		{"input containing separator", []string{"1", "a*b"}, "1*a*b"},
		{"unicode input", []string{"Ω"}, "Ω"},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := fmt.Sprintf("sess_edge_%d", i)
			s := fixture(id)
			s.Inputs = tc.inputs

			if err := store.Create(ctx, s); err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			got, err := store.Get(ctx, id)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.Text() != tc.want {
				t.Errorf("Text() = %q, want %q", got.Text(), tc.want)
			}
		})
	}
}

// Stores must be safe for concurrent use. Run with -race for this to bite.
func testConcurrency(t *testing.T, newStore Factory) {
	ctx := context.Background()
	store := newStore(t)

	const workers = 16
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			id := fmt.Sprintf("sess_conc_%d", i)
			s := fixture(id)

			if err := store.Create(ctx, s); err != nil {
				t.Errorf("Create(%s) error = %v", id, err)
				return
			}
			s.Status = session.StatusCompleted
			if err := store.Update(ctx, s); err != nil {
				t.Errorf("Update(%s) error = %v", id, err)
				return
			}
			if _, err := store.Get(ctx, id); err != nil {
				t.Errorf("Get(%s) error = %v", id, err)
			}
		}(i)
	}

	wg.Wait()
}
