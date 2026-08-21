// Package sqlite provides a durable SessionStore backed by SQLite.
//
// It exists alongside the in-memory store not because live session state needs
// durability -- it does not -- but because an interface with one implementation
// is an unvalidated guess. Both stores pass the identical conformance suite in
// internal/storage/storagetest, which is what makes SessionStore a verified
// abstraction and what will make the eventual Redis implementation a matter of
// passing the same tests (ADR-003).
//
// The driver is modernc.org/sqlite, a pure-Go implementation. A cgo driver
// would be faster but would break effortless cross-compilation, contradicting
// the single-binary distribution goal that motivated choosing Go at all.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"github.com/yeboahd24/ussd-lab/internal/session"
)

// driverName is the name modernc.org/sqlite registers with database/sql.
const driverName = "sqlite"

// timeLayout is the on-disk timestamp format: RFC3339 with nanoseconds, always
// UTC. A text format keeps the database inspectable with ordinary tools, which
// matters for a debugging aid.
const timeLayout = time.RFC3339Nano

// Store is a SQLite-backed SessionStore.
type Store struct {
	db *sql.DB
}

var _ session.SessionStore = (*Store)(nil)

// Open opens (creating if necessary) the database at path and migrates it.
//
// Use ":memory:" for an ephemeral database.
func Open(ctx context.Context, path string) (*Store, error) {
	// busy_timeout makes concurrent writers wait rather than fail immediately;
	// WAL allows readers to proceed during a write.
	dsn := fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)",
		path)

	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite database %s: %w", path, err)
	}

	// A single connection serialises all access. SQLite permits only one
	// writer anyway, and at laptop scale -- a handful of concurrent USSD
	// sessions -- removing lock contention entirely is worth more than
	// parallel reads.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect to sqlite database %s: %w", path, err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}

	if err := restrictPermissions(path); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// dbFileMode keeps databases readable only by their owner.
//
// These files hold phone numbers and raw user input, which may include a PIN if
// the developer's application asks for one. SQLite creates them 0644 by
// default, which on a shared machine means every other user can read them.
const dbFileMode = 0o600

// restrictPermissions tightens the database file and its WAL sidecars.
func restrictPermissions(path string) error {
	if path == "" || path == ":memory:" {
		return nil
	}

	// The -wal and -shm files carry the same data and are created lazily, so
	// a missing one is expected rather than an error.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, dbFileMode); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("restrict permissions on %s: %w", p, err)
		}
	}
	return nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const insertSQL = `
INSERT INTO sessions (
	id, project_id, service_code, phone_number, network,
	status, inputs, created_at, updated_at, expires_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING`

func (s *Store) Create(ctx context.Context, sess *session.Session) error {
	inputs, err := encodeInputs(sess.Inputs)
	if err != nil {
		return err
	}

	// ON CONFLICT DO NOTHING plus a RowsAffected check detects a duplicate
	// atomically, without matching on driver-specific error strings.
	res, err := s.db.ExecContext(ctx, insertSQL,
		sess.ID, sess.ProjectID, sess.ServiceCode, sess.PhoneNumber, sess.Network,
		string(sess.Status), inputs,
		encodeTime(sess.CreatedAt), encodeTime(sess.UpdatedAt), encodeTime(sess.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("insert session %s: %w", sess.ID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("insert session %s: %w", sess.ID, err)
	}
	if n == 0 {
		return session.ErrSessionExists
	}
	return nil
}

const selectSQL = `
SELECT id, project_id, service_code, phone_number, network,
       status, inputs, created_at, updated_at, expires_at
FROM sessions WHERE id = ?`

func (s *Store) Get(ctx context.Context, id string) (*session.Session, error) {
	var (
		sess                            session.Session
		status, inputs                  string
		createdAt, updatedAt, expiresAt string
	)

	err := s.db.QueryRowContext(ctx, selectSQL, id).Scan(
		&sess.ID, &sess.ProjectID, &sess.ServiceCode, &sess.PhoneNumber, &sess.Network,
		&status, &inputs, &createdAt, &updatedAt, &expiresAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, session.ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("select session %s: %w", id, err)
	}

	sess.Status = session.Status(status)

	if sess.Inputs, err = decodeInputs(inputs); err != nil {
		return nil, fmt.Errorf("session %s: %w", id, err)
	}
	if sess.CreatedAt, err = decodeTime(createdAt); err != nil {
		return nil, fmt.Errorf("session %s created_at: %w", id, err)
	}
	if sess.UpdatedAt, err = decodeTime(updatedAt); err != nil {
		return nil, fmt.Errorf("session %s updated_at: %w", id, err)
	}
	if sess.ExpiresAt, err = decodeTime(expiresAt); err != nil {
		return nil, fmt.Errorf("session %s expires_at: %w", id, err)
	}

	return &sess, nil
}

const updateSQL = `
UPDATE sessions SET
	project_id = ?, service_code = ?, phone_number = ?, network = ?,
	status = ?, inputs = ?, created_at = ?, updated_at = ?, expires_at = ?
WHERE id = ?`

func (s *Store) Update(ctx context.Context, sess *session.Session) error {
	inputs, err := encodeInputs(sess.Inputs)
	if err != nil {
		return err
	}

	res, err := s.db.ExecContext(ctx, updateSQL,
		sess.ProjectID, sess.ServiceCode, sess.PhoneNumber, sess.Network,
		string(sess.Status), inputs,
		encodeTime(sess.CreatedAt), encodeTime(sess.UpdatedAt), encodeTime(sess.ExpiresAt),
		sess.ID,
	)
	if err != nil {
		return fmt.Errorf("update session %s: %w", sess.ID, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update session %s: %w", sess.ID, err)
	}
	if n == 0 {
		return session.ErrSessionNotFound
	}
	return nil
}

func (s *Store) Delete(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}

	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	if n == 0 {
		return session.ErrSessionNotFound
	}
	return nil
}

// encodeInputs stores the input history as a JSON array.
//
// A delimited string would reintroduce exactly the "*" ambiguity that keeping
// Inputs as a list avoids (ADR-002), so the encoding must preserve element
// boundaries.
func encodeInputs(inputs []string) (string, error) {
	if inputs == nil {
		return "[]", nil
	}
	b, err := json.Marshal(inputs)
	if err != nil {
		return "", fmt.Errorf("encode inputs: %w", err)
	}
	return string(b), nil
}

func decodeInputs(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode inputs: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// encodeTime renders t as UTC text. A zero time becomes the empty string so it
// survives the round trip as a zero time rather than as year 1.
func encodeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(timeLayout)
}

func decodeTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(timeLayout, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse time %q: %w", raw, err)
	}
	return t.UTC(), nil
}
