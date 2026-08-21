package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// schemaVersion is the migration level this build expects.
//
// It is tracked in SQLite's built-in user_version pragma rather than a
// migrations table: with a single local file and a handful of migrations, a
// table would be more machinery than the problem needs.
const schemaVersion = 2

// migrations are applied in order, each moving the schema up by one version.
// Never edit a migration that has shipped -- append a new one instead.
var migrations = []string{
	// v1: sessions
	`
	CREATE TABLE IF NOT EXISTS sessions (
		id           TEXT PRIMARY KEY,
		project_id   TEXT NOT NULL,
		service_code TEXT NOT NULL,
		phone_number TEXT NOT NULL,
		network      TEXT NOT NULL,
		status       TEXT NOT NULL,
		inputs       TEXT NOT NULL,
		created_at   TEXT NOT NULL,
		updated_at   TEXT NOT NULL,
		expires_at   TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_sessions_status
		ON sessions (status);

	CREATE INDEX IF NOT EXISTS idx_sessions_project_created
		ON sessions (project_id, created_at);
	`,

	// v2: session events, the durable record behind `ussd logs`.
	`
	CREATE TABLE IF NOT EXISTS events (
		event_id   TEXT PRIMARY KEY,
		session_id TEXT NOT NULL,
		type       TEXT NOT NULL,
		payload    TEXT NOT NULL,
		timestamp  TEXT NOT NULL
	);

	-- Every query either walks one session in order, or scans recent activity.
	CREATE INDEX IF NOT EXISTS idx_events_session_time
		ON events (session_id, timestamp);

	CREATE INDEX IF NOT EXISTS idx_events_time
		ON events (timestamp);
	`,
}

// migrate brings the database up to schemaVersion.
func migrate(ctx context.Context, db *sql.DB) error {
	var current int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	if current > schemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than this build supports (%d)",
			current, schemaVersion)
	}

	for v := current; v < schemaVersion; v++ {
		if _, err := db.ExecContext(ctx, migrations[v]); err != nil {
			return fmt.Errorf("apply migration %d: %w", v+1, err)
		}
		// PRAGMA does not accept a bind parameter for the value.
		if _, err := db.ExecContext(ctx,
			fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return fmt.Errorf("record schema version %d: %w", v+1, err)
		}
	}

	return nil
}
