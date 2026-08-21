package session

import (
	"context"
	"errors"
)

// Store errors. They are sentinels so callers can branch with errors.Is
// regardless of which implementation produced them -- a store that invented
// its own error values would be a silent behavioural difference.
var (
	// ErrSessionNotFound: no session with that ID exists.
	ErrSessionNotFound = errors.New("session not found")

	// ErrSessionExists: Create was called with an ID already in use.
	ErrSessionExists = errors.New("session already exists")
)

// SessionStore persists sessions (MVP design §26).
//
// The interface is deliberately four methods wide. Everything the MVP needs is
// keyed lookup, so adding List or query methods now would be speculative -- and
// every method added here must be implemented, and proved correct, by every
// future store including Redis.
//
// Implementations MUST:
//   - store and return deep copies, so a caller mutating a returned session
//     cannot alter stored state;
//   - return ErrSessionNotFound / ErrSessionExists rather than custom errors;
//   - be safe for concurrent use.
//
// These requirements are executable: see internal/storage/storagetest.
type SessionStore interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	Update(ctx context.Context, s *Session) error
	Delete(ctx context.Context, id string) error
}
