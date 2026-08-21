// Package memory provides an in-process SessionStore.
//
// It is the default store for `ussd dev` and the store used by engine unit
// tests. Live USSD session state lives for roughly two minutes and is worthless
// after the process exits, so durability buys nothing here (ADR-003).
package memory

import (
	"context"
	"sync"

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// Store keeps sessions in a map guarded by a mutex.
type Store struct {
	mu       sync.RWMutex
	sessions map[string]*session.Session
}

var _ session.SessionStore = (*Store)(nil)

// New returns an empty Store.
func New() *Store {
	return &Store{sessions: make(map[string]*session.Session)}
}

func (s *Store) Create(_ context.Context, sess *session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.sessions[sess.ID]; exists {
		return session.ErrSessionExists
	}

	// Store a clone. Without it the caller retains a pointer into the store and
	// could mutate "persisted" state without calling Update -- behaviour no
	// database-backed store could ever reproduce.
	s.sessions[sess.ID] = sess.Clone()
	return nil
}

func (s *Store) Get(_ context.Context, id string) (*session.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sess, ok := s.sessions[id]
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	return sess.Clone(), nil
}

func (s *Store) Update(_ context.Context, sess *session.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[sess.ID]; !ok {
		return session.ErrSessionNotFound
	}
	s.sessions[sess.ID] = sess.Clone()
	return nil
}

func (s *Store) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return session.ErrSessionNotFound
	}
	delete(s.sessions, id)
	return nil
}

// Len reports how many sessions are held. Test and diagnostic use only.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}
