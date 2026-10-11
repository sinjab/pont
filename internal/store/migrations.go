package store

import (
	"database/sql"
	"fmt"
)

// migrations are numbered, run exactly once, and tracked via PRAGMA
// user_version (P1, rewrite-consult Oct 11 2026). Migration 1 adopts any
// pre-versioning schema, creates the chat-timestamp index, and performs the
// one-time FTS rebuild that Open() used to run on EVERY open — a write lock
// per read against 87k messages.
type migration struct {
	version int64
	name    string
	fn      func(execer) error
}

// execer is satisfied by both *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

var migrations = []migration{
	{
		version: 1,
		name:    "adopt baseline + chat-timestamp index + one-time FTS rebuild",
		fn: func(db execer) error {
			if err := baselineSchema(db); err != nil {
				return err
			}
			if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, timestamp DESC)`); err != nil {
				return fmt.Errorf("create idx_messages_chat_ts: %w", err)
			}
			// One-time rebuild: rows written before the FTS triggers existed,
			// or desynced by historical INSERT OR REPLACE churn, get reindexed.
			if _, err := db.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('rebuild')`); err != nil {
				return fmt.Errorf("fts rebuild: %w", err)
			}
			return nil
		},
	},
	{
		version: 2,
		name:    "watch event journal (durable replay for watch consumers)",
		fn: func(db execer) error {
			_, err := db.Exec(`CREATE TABLE IF NOT EXISTS watch_events (
				seq INTEGER PRIMARY KEY AUTOINCREMENT,
				ts TIMESTAMP NOT NULL,
				kind TEXT NOT NULL,
				payload TEXT NOT NULL
			)`)
			if err != nil {
				return fmt.Errorf("create watch_events: %w", err)
			}
			return nil
		},
	},
}

// migrateDB applies pending migrations, each in its own transaction with
// user_version advanced inside the same transaction as the schema change.
func migrateDB(db *sql.DB) error {
	var current int64
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&current); err != nil {
		return fmt.Errorf("read user_version: %w", err)
	}
	for _, m := range migrations {
		if m.version <= current {
			continue
		}
		tx, err := db.Begin()
		if err != nil {
			return fmt.Errorf("migration %d begin: %w", m.version, err)
		}
		if err := m.fn(tx); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d (%s): %w", m.version, m.name, err)
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, m.version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set user_version=%d: %w", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration %d commit: %w", m.version, err)
		}
	}
	return nil
}
