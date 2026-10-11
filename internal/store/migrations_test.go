package store

// P1 hardening tests: numbered migrations + user_version, WAL, one-time FTS
// rebuild, and upsert semantics (rowid stability, no column nulling).

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func openT(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.CloseQuietly() })
	return db
}

func TestOpenEnablesWALAndMigratesToV1(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	var mode string
	if err := db.Messages.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}

	var v int64
	if err := db.Messages.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	var want int64 = 1
	for _, m := range migrations {
		want = m.version
	}
	if v != want {
		t.Fatalf("user_version = %d, want %d", v, want)
	}

	var idx string
	if err := db.Messages.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_messages_chat_ts'`).Scan(&idx); err != nil {
		t.Fatalf("idx_messages_chat_ts missing: %v", err)
	}
}

// TestLegacyFTSHeal simulates a pre-versioning database whose rows missed the
// FTS index (written before triggers existed): resetting user_version to 0
// re-runs migration 1, whose one-time rebuild restores searchability.
func TestLegacyFTSHeal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.db")
	db := openT(t, path)

	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('a@s.whatsapp.net', 'A')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('M1', 'a@s.whatsapp.net', 'a@s.whatsapp.net', 'needle in haystack', '2026-10-01T09:00:00Z', 0)`); err != nil {
		t.Fatal(err)
	}
	// Simulate the legacy desync: wipe FTS content and forget the version.
	if _, err := db.Messages.Exec(`INSERT INTO messages_fts(messages_fts) VALUES('delete-all')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Messages.Exec(`PRAGMA user_version=0`); err != nil {
		t.Fatal(err)
	}
	db.CloseQuietly()

	healed := openT(t, path)
	var n int
	if err := healed.Messages.QueryRow(`SELECT COUNT(*) FROM messages_fts WHERE messages_fts MATCH 'needle'`).Scan(&n); err != nil {
		t.Fatalf("match: %v", err)
	}
	if n != 1 {
		t.Fatalf("FTS heal failed: %d matches, want 1", n)
	}
}

// TestUpsertPreservesRowidAndColumns pins the P1 INSERT .. ON CONFLICT
// semantics that INSERT OR REPLACE violated: re-seen messages keep their
// rowid (no FTS churn) and non-empty columns are never nulled.
func TestUpsertPreservesRowidAndColumns(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('a@s.whatsapp.net', 'A')`); err != nil {
		t.Fatal(err)
	}
	const upsertMsg = `INSERT INTO messages
		(id, chat_jid, sender, sender_jid, sender_name, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, chat_jid) DO UPDATE SET
			sender = excluded.sender,
			sender_jid = CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE messages.sender_jid END,
			sender_name = CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE messages.sender_name END,
			content = CASE WHEN excluded.content != '' THEN excluded.content ELSE messages.content END,
			timestamp = excluded.timestamp,
			is_from_me = excluded.is_from_me,
			media_type = CASE WHEN excluded.media_type IS NOT NULL AND excluded.media_type != '' THEN excluded.media_type ELSE messages.media_type END,
			filename = CASE WHEN excluded.filename IS NOT NULL AND excluded.filename != '' THEN excluded.filename ELSE messages.filename END,
			url = CASE WHEN excluded.url != '' THEN excluded.url ELSE messages.url END,
			media_key = CASE WHEN excluded.media_key IS NOT NULL THEN excluded.media_key ELSE messages.media_key END,
			file_sha256 = CASE WHEN excluded.file_sha256 IS NOT NULL THEN excluded.file_sha256 ELSE messages.file_sha256 END,
			file_enc_sha256 = CASE WHEN excluded.file_enc_sha256 IS NOT NULL THEN excluded.file_enc_sha256 ELSE messages.file_enc_sha256 END,
			file_length = CASE WHEN excluded.file_length != 0 THEN excluded.file_length ELSE messages.file_length END`

	if _, err := db.Messages.Exec(upsertMsg,
		"M1", "a@s.whatsapp.net", "a@s.whatsapp.net", "a@s.whatsapp.net", "Original Name", "first content",
		"2026-10-01T09:00:00Z", false, "image", "orig.jpg", "http://u", []byte{1}, []byte{2}, []byte{3}, 123); err != nil {
		t.Fatal(err)
	}
	var rowid1 int64
	if err := db.Messages.QueryRow(`SELECT rowid FROM messages WHERE id='M1'`).Scan(&rowid1); err != nil {
		t.Fatal(err)
	}

	// Re-see the same message with emptier payloads (echo re-delivery).
	if _, err := db.Messages.Exec(upsertMsg,
		"M1", "a@s.whatsapp.net", "a@s.whatsapp.net", "", "", "",
		"2026-10-01T09:00:01Z", false, nil, nil, "", nil, nil, nil, 0); err != nil {
		t.Fatal(err)
	}

	var rowid2 int64
	var senderName, content, mediaType, filename, url sql.NullString
	var fileLen sql.NullInt64
	if err := db.Messages.QueryRow(`SELECT rowid, sender_name, content, media_type, filename, url, file_length
		FROM messages WHERE id='M1'`).Scan(&rowid2, &senderName, &content, &mediaType, &filename, &url, &fileLen); err != nil {
		t.Fatal(err)
	}

	if rowid1 != rowid2 {
		t.Fatalf("rowid changed on upsert: %d -> %d (FTS churn + FK churn)", rowid1, rowid2)
	}
	if !senderName.Valid || senderName.String != "Original Name" {
		t.Fatalf("sender_name nulled by upsert: %+v", senderName)
	}
	if !content.Valid || content.String != "first content" {
		t.Fatalf("content nulled by upsert: %+v", content)
	}
	if !mediaType.Valid || mediaType.String != "image" {
		t.Fatalf("media_type nulled by upsert: %+v", mediaType)
	}
	if !filename.Valid || filename.String != "orig.jpg" {
		t.Fatalf("filename nulled by upsert: %+v", filename)
	}
	if !url.Valid || url.String != "http://u" {
		t.Fatalf("url nulled by upsert: %+v", url)
	}
	if !fileLen.Valid || fileLen.Int64 != 123 {
		t.Fatalf("file_length zeroed by upsert: %+v", fileLen)
	}

	// Newer non-empty payload DOES overwrite (freshness).
	if _, err := db.Messages.Exec(upsertMsg,
		"M1", "a@s.whatsapp.net", "a@s.whatsapp.net", "a@s.whatsapp.net", "New Name", "updated content",
		"2026-10-01T09:00:02Z", false, "video", "v.mp4", "http://u2", []byte{9}, []byte{9}, []byte{9}, 456); err != nil {
		t.Fatal(err)
	}
	if err := db.Messages.QueryRow(`SELECT sender_name, content, media_type FROM messages WHERE id='M1'`).
		Scan(&senderName, &content, &mediaType); err != nil {
		t.Fatal(err)
	}
	if senderName.String != "New Name" || content.String != "updated content" || mediaType.String != "video" {
		t.Fatalf("fresh non-empty payload did not overwrite: %+v %+v %+v", senderName, content, mediaType)
	}
}

func TestMigrationV3SenderJIDColumn(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))
	var col string
	if err := db.Messages.QueryRow(`SELECT name FROM pragma_table_info('messages') WHERE name='sender_jid'`).Scan(&col); err != nil {
		t.Fatalf("sender_jid column missing: %v", err)
	}
	// Legacy-honesty: no backfill — existing rows keep NULL sender_jid.
	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('b@s.whatsapp.net','B')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('L1','b@s.whatsapp.net','b','legacy row','2026-10-01T09:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	var jid sql.NullString
	if err := db.Messages.QueryRow(`SELECT sender_jid FROM messages WHERE id='L1'`).Scan(&jid); err != nil {
		t.Fatal(err)
	}
	if jid.Valid {
		t.Fatalf("legacy row got a fabricated sender_jid: %q", jid.String)
	}
}
