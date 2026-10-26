package store

import (
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestMigrationV6VCardColumn: the vcard column exists after migration,
// legacy rows keep NULL (never-guess rule), fresh writes round-trip,
// and the upsert's non-empty guard heals a legacy row on re-ingest
// (the backfill recovery path for cards stored pre-v6).
func TestMigrationV6VCardColumn(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	var name string
	if err := db.Messages.QueryRow(`SELECT name FROM pragma_table_info('messages') WHERE name='vcard'`).Scan(&name); err != nil {
		t.Fatalf("vcard column missing: %v", err)
	}

	if _, err := db.Messages.Exec(`INSERT INTO chats (jid, name) VALUES ('h@lid','H')`); err != nil {
		t.Fatal(err)
	}
	// Legacy-shaped row: no vcard supplied (a pre-v6 contact card).
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me)
		VALUES ('C1','h@lid','h','👤 Legacy Card','2026-10-26T15:43:05Z',0)`); err != nil {
		t.Fatal(err)
	}
	var v interface{}
	if err := db.Messages.QueryRow(`SELECT vcard FROM messages WHERE id='C1'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != nil {
		t.Fatalf("legacy row got fabricated vcard: %v", v)
	}

	// Re-ingest of the same message id with a vcard heals the row
	// (ON CONFLICT non-empty guard) — the backfill recovery lane.
	if _, err := db.Messages.Exec(`INSERT INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, vcard)
		VALUES ('C1','h@lid','h','👤 Legacy Card','2026-10-26T15:43:05Z',0,'BEGIN:VCARD')
		ON CONFLICT(id, chat_jid) DO UPDATE SET
			vcard = CASE WHEN excluded.vcard != '' THEN excluded.vcard ELSE messages.vcard END`); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := db.Messages.QueryRow(`SELECT vcard FROM messages WHERE id='C1'`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != "BEGIN:VCARD" {
		t.Fatalf("heal-on-reingest failed: %q", got)
	}

	// Fresh card row round-trips through ListMessages with VCard set.
	if _, err := db.Messages.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, vcard)
		VALUES ('C2','h@lid','h','👤 Fresh Card','2026-10-26T16:00:00Z',0,'BEGIN:VCARD\nTEL:+123')`); err != nil {
		t.Fatal(err)
	}
	msgs, err := db.ListMessages(ListMessagesOptions{ChatJID: "h@lid", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, m := range msgs {
		if m.ID == "C2" {
			seen = true
			if m.VCard == nil || !strings.Contains(*m.VCard, "TEL:+123") {
				t.Fatalf("ListMessages did not surface vcard: %+v", m.VCard)
			}
		}
	}
	if !seen {
		t.Fatal("C2 not returned by ListMessages")
	}
}
