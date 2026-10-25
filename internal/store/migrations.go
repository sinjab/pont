package store

import (
	"database/sql"
	"fmt"
	"strings"
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
	{
		version: 3,
		name:    "sender full-JID column (new records only; legacy rows stay NULL)",
		fn: func(db execer) error {
			// sender stores only the user component - a LID user and a phone
			// user with the same digits are ambiguous. sender_jid captures the
			// full JID for NEW records; legacy rows stay NULL rather than
			// guessed (never pretend historical identifiers can be
			// reconstructed reliably).
			if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN sender_jid TEXT`); err != nil {
				if !strings.Contains(err.Error(), "duplicate column name") {
					return fmt.Errorf("add sender_jid: %w", err)
				}
			}
			return nil
		},
	},
	{
		version: 4,
		name:    "quoted-reply columns (reply_to_id + reply_to_text snapshot; new records only, legacy rows NULL)",
		fn: func(db execer) error {
			// A quoted reply carries its target in the protocol's
			// ContextInfo; persisting reply_to_id (+ a text snapshot of
			// the quoted message) makes every stored reply self-describing
			// even when the quoted original was phone-sent, lives in
			// another chat, or predates the store. New records only -
			// legacy rows stay NULL (same never-guess rule as sender_jid).
			for _, col := range []string{`reply_to_id TEXT`, `reply_to_text TEXT`} {
				if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN ` + col); err != nil {
					if !strings.Contains(err.Error(), "duplicate column name") {
						return fmt.Errorf("add %s: %w", col, err)
					}
				}
			}
			return nil
		},
	},
	{
		version: 5,
		name:    "true media reference columns (direct_path + media_key_timestamp; new records only, legacy rows NULL)",
		fn: func(db execer) error {
			// Forwarding media re-sends the row's media reference.
			// direct_path is the protocol's OWN signed reference from the
			// uploader; without it, forwards had to SYNTHESIZE a path by
			// stripping the URL's ?oh= auth token — an invalid reference
			// that directPath-first clients (Android) fail to download
			// permanently ("Couldn't download / ask that it be resent",
			// proven Oct 25 2026). media_key_timestamp rides the same
			// capture. New records only — legacy rows stay NULL and
			// forward URL-only (DirectPath empty, never synthesized).
			for _, col := range []string{`direct_path TEXT`, `media_key_timestamp INTEGER`} {
				if _, err := db.Exec(`ALTER TABLE messages ADD COLUMN ` + col); err != nil {
					if !strings.Contains(err.Error(), "duplicate column name") {
						return fmt.Errorf("add %s: %w", col, err)
					}
				}
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
