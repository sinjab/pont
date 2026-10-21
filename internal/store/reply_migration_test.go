package store

import (
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrationV4ReplyColumns: the quoted-reply columns exist after
// migration, legacy rows keep NULL (never-guess rule), and fresh
// writes round-trip through the upsert path.
func TestMigrationV4ReplyColumns(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	for _, col := range []string{"reply_to_id", "reply_to_text"} {
		var name string
		if err := db.Messages.QueryRow(`SELECT name FROM pragma_table_info('messages') WHERE name=?`, col).Scan(&name); err != nil {
			t.Fatalf("%s column missing: %v", col, err)
		}
	}

	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('r@s.whatsapp.net','R')`); err != nil {
		t.Fatal(err)
	}
	// Legacy-shaped row: no reply columns supplied.
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('L1','r@s.whatsapp.net','r','legacy row','2026-10-01T09:00:00Z',0)`); err != nil {
		t.Fatal(err)
	}
	var rid, rtext interface{}
	if err := db.Messages.QueryRow(`SELECT reply_to_id, reply_to_text FROM messages WHERE id='L1'`).Scan(&rid, &rtext); err != nil {
		t.Fatal(err)
	}
	if rid != nil || rtext != nil {
		t.Fatalf("legacy row got fabricated reply fields: %v %v", rid, rtext)
	}

	// Fresh reply row round-trips.
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, reply_to_id, reply_to_text)
		VALUES ('R1','r@s.whatsapp.net','r','the reply text','2026-10-21T17:38:22Z',0,'QUOTED1','quoted body')`); err != nil {
		t.Fatal(err)
	}
	var gotID, gotText string
	if err := db.Messages.QueryRow(`SELECT reply_to_id, reply_to_text FROM messages WHERE id='R1'`).Scan(&gotID, &gotText); err != nil {
		t.Fatal(err)
	}
	if gotID != "QUOTED1" || gotText != "quoted body" {
		t.Fatalf("reply round-trip mismatch: %q %q", gotID, gotText)
	}
}
