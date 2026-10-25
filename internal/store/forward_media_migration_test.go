package store

import (
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrationV5MediaReferenceColumns: the true media-reference columns
// exist after migration, legacy rows keep NULL (never-guess rule), and
// fresh writes round-trip. Forwards read direct_path to re-send the
// protocol's OWN signed reference — synthesizing it from the URL strips
// the ?oh= auth token and produces an undownloadable reference for
// directPath-first clients (proven Oct 25 2026, the Basem «ما فتح» case).
func TestMigrationV5MediaReferenceColumns(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	for _, col := range []string{"direct_path", "media_key_timestamp"} {
		var name string
		if err := db.Messages.QueryRow(`SELECT name FROM pragma_table_info('messages') WHERE name=?`, col).Scan(&name); err != nil {
			t.Fatalf("%s column missing: %v", col, err)
		}
	}

	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('b@lid','B')`); err != nil {
		t.Fatal(err)
	}
	// Legacy-shaped row: no media-reference columns supplied.
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('L1','b@lid','b','legacy row','2026-10-01T09:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	var dp interface{}
	var mkt interface{}
	if err := db.Messages.QueryRow(`SELECT direct_path, media_key_timestamp FROM messages WHERE id='L1'`).Scan(&dp, &mkt); err != nil {
		t.Fatal(err)
	}
	if dp != nil || mkt != nil {
		t.Fatalf("legacy row got fabricated media reference fields: %v %v", dp, mkt)
	}

	// Fresh media row round-trips both columns.
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, media_type, url, direct_path, media_key_timestamp, file_length)
		VALUES ('M1','b@lid','b','','2026-10-25T13:18:42Z',0,'audio','https://mmg.whatsapp.net/v/t62.7117-24/x?oh=tok','/v/t62.7117-24/x',1761386322000,71009)`); err != nil {
		t.Fatal(err)
	}
	var gotDP string
	var gotMKT int64
	if err := db.Messages.QueryRow(`SELECT direct_path, media_key_timestamp FROM messages WHERE id='M1'`).Scan(&gotDP, &gotMKT); err != nil {
		t.Fatal(err)
	}
	if gotDP != "/v/t62.7117-24/x" || gotMKT != 1761386322000 {
		t.Fatalf("media reference round-trip mismatch: %q %d", gotDP, gotMKT)
	}
}
