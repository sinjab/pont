package store

// Table-driven unit tests for the query layer (rewrite-consult §7, the
// last unshipped recommendation, Oct 12 2026): the contract goldens
// freeze the CLI surface; these freeze the SQL beneath it — filters,
// windows, ordering, joins, subselects, FTS search. Fixture rows go in
// via raw SQL (FTS triggers keep the index synced on insert).
import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

const (
	tDirect = "216300000001@s.whatsapp.net"
	tGroup  = "120300000002@g.us"
	tLID    = "987600000003@lid"
	tEmpty  = "971500000004@s.whatsapp.net"
)

func fixtureDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "messages.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.CloseQuietly() })

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Messages.Exec(q, args...); err != nil {
			t.Fatalf("fixture exec: %v", err)
		}
	}

	// Timestamps bind as UTC time.Time — mirrors production writes and
	// the driver-format window bounds (string inserts would lexically
	// mismatch the parsed bounds; that gap hid a false-negative window
	// golden in the CLI contract fixture).
	mustTime := func(s string) time.Time {
		tv, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatalf("fixture time %q: %v", s, err)
		}
		return tv
	}

	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		tDirect, "Yusra Fixture", mustTime("2026-10-01T09:00:00Z"))
	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		tGroup, "Family Fixture", mustTime("2026-10-01T10:00:00Z"))
	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		tEmpty, "Empty Fixture", nil)
	exec(`INSERT INTO lid_mappings (lid, phone, name, updated_at) VALUES (?,?,?,?)`,
		tLID, "971500000009", "Kareem Fixture", mustTime("2026-10-01T08:00:00Z"))

	type row struct {
		id, chat, sender, senderName, content, ts, media any
	}
	rows := []row{
		{"M1", tDirect, tDirect, nil, "oldest direct text", "2026-09-30T08:00:00Z", nil},
		{"M2", tDirect, tDirect, nil, "newest direct text", "2026-10-01T09:00:00Z", nil},
		{"M3", tGroup, tLID, nil, "marhaba from lid sender", "2026-10-01T10:00:00Z", nil},
		{"M4", tGroup, tDirect, "Named Sender", "picture time", "2026-10-01T09:30:00Z", "image"},
		{"M5", tGroup, tDirect, "Named Sender", nil, "2026-10-01T09:45:00Z", "video"},
	}
	for _, r := range rows {
		var isFromMe int
		if r.id == "M2" {
			isFromMe = 1
		}
		exec(`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp, is_from_me, media_type)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.id, r.chat, r.sender, r.senderName, r.content, mustTime(r.ts.(string)), isFromMe, r.media)
	}
	return db
}

func TestListChatsOrderingAndSubselects(t *testing.T) {
	db := fixtureDB(t)
	chats, err := db.ListChats(ListChatsOptions{})
	if err != nil {
		t.Fatalf("ListChats: %v", err)
	}
	if len(chats) != 3 {
		t.Fatalf("want 3 chats, got %d", len(chats))
	}
	// group has the newest message; the EMPTY chat (nil last_message_time) sorts last
	if chats[0].JID != tGroup || chats[2].JID != tEmpty {
		t.Errorf("ordering: [%s, %s, %s], want group first, empty last", chats[0].JID, chats[1].JID, chats[2].JID)
	}
	top := chats[0]
	if top.LastMessage == nil || *top.LastMessage != "marhaba from lid sender" {
		t.Errorf("last_message subselect: %v", top.LastMessage)
	}
	if top.LastIsFromMe == nil || *top.LastIsFromMe {
		t.Errorf("last_is_from_me subselect: %v", top.LastIsFromMe)
	}
	if !top.IsGroup {
		t.Errorf("group flag not set for @g.us")
	}
}

func TestListChatsFilters(t *testing.T) {
	db := fixtureDB(t)

	byName, err := db.ListChats(ListChatsOptions{Query: "yusra"})
	if err != nil || len(byName) != 1 || byName[0].JID != tDirect {
		t.Errorf("query filter (case-insensitive name): %d chats, err=%v", len(byName), err)
	}

	groups, err := db.ListChats(ListChatsOptions{OnlyGroups: true})
	if err != nil || len(groups) != 1 || groups[0].JID != tGroup {
		t.Errorf("only-groups filter: %d chats, err=%v", len(groups), err)
	}

	limited, err := db.ListChats(ListChatsOptions{Limit: 1})
	if err != nil || len(limited) != 1 {
		t.Errorf("limit: %d chats, err=%v", len(limited), err)
	}
}

func TestOldestMessageForChat(t *testing.T) {
	db := fixtureDB(t)

	m, err := db.OldestMessageForChat(tDirect)
	if err != nil {
		t.Fatalf("OldestMessageForChat: %v", err)
	}
	if m.ID != "M1" {
		t.Errorf("oldest = %s, want M1", m.ID)
	}
	if m.ChatName == nil || *m.ChatName != "Yusra Fixture" {
		t.Errorf("chat_name join: %v", m.ChatName)
	}

	// group oldest is M4 (09:30Z, before M3's 10:00Z)
	g, err := db.OldestMessageForChat(tGroup)
	if err != nil {
		t.Fatalf("OldestMessageForChat(group): %v", err)
	}
	if g.ID != "M4" {
		t.Fatalf("group oldest = %s, want M4", g.ID)
	}
	if g.SenderName == nil || *g.SenderName != "Named Sender" {
		t.Errorf("stored sender_name: %v", g.SenderName)
	}

	// LID sender with no stored sender_name resolves through lid_mappings
	grp, err := db.ListMessages(ListMessagesOptions{ChatJID: tGroup})
	if err != nil {
		t.Fatalf("ListMessages(group): %v", err)
	}
	for _, m := range grp {
		if m.ID == "M3" {
			if m.SenderName == nil || *m.SenderName != "Kareem Fixture" {
				t.Errorf("LID join sender_name on M3: %v", m.SenderName)
			}
		}
	}

	if _, err := db.OldestMessageForChat(tEmpty); err != sql.ErrNoRows {
		t.Errorf("empty chat: want ErrNoRows, got %v", err)
	}
}

func TestListMessagesFiltersAndWindow(t *testing.T) {
	db := fixtureDB(t)

	// chat filter + DESC ordering
	inChat, err := db.ListMessages(ListMessagesOptions{ChatJID: tGroup})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(inChat) != 3 || inChat[0].ID != "M3" || inChat[2].ID != "M4" {
		t.Errorf("chat filter/order: ids %v", msgIDs(inChat))
	}

	// after/before window (RFC3339)
	window, err := db.ListMessages(ListMessagesOptions{
		After:  "2026-10-01T09:00:00Z",
		Before: "2026-10-01T09:40:00Z",
	})
	if err != nil {
		t.Fatalf("window: %v", err)
	}
	if len(window) != 2 { // M2 (09:00Z) + M4 (09:30Z)
		t.Errorf("window: ids %v, want [M4 M2]", msgIDs(window))
	}

	// type filters: text excludes media; image matches M4 only
	texts, _ := db.ListMessages(ListMessagesOptions{Type: "text", ChatJID: tGroup})
	if len(texts) != 1 || texts[0].ID != "M3" {
		t.Errorf("type=text: ids %v", msgIDs(texts))
	}
	images, _ := db.ListMessages(ListMessagesOptions{Type: "image", ChatJID: tGroup})
	if len(images) != 1 || images[0].ID != "M4" {
		t.Errorf("type=image: ids %v", msgIDs(images))
	}

	// is_from_me mapping
	for _, m := range inChat {
		if m.ID == "M3" && m.IsFromMe {
			t.Errorf("M3 flagged from_me")
		}
	}
	direct, _ := db.ListMessages(ListMessagesOptions{ChatJID: tDirect})
	if !direct[0].IsFromMe || direct[0].ID != "M2" {
		t.Errorf("M2 should be from_me")
	}
}

func TestSearchMessagesFTSAndFilters(t *testing.T) {
	db := fixtureDB(t)

	hits, err := db.SearchMessages(SearchMessagesOptions{Query: "marhaba"})
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != "M3" {
		t.Fatalf("fts match: ids %v", msgIDs(hits))
	}

	// from filter: same word exists only in M3, but filter by direct chat -> none
	none, err := db.SearchMessages(SearchMessagesOptions{Query: "marhaba", ChatJID: tDirect})
	if err != nil || len(none) != 0 {
		t.Errorf("chat-scoped search: %d hits, err=%v", len(none), err)
	}

	// from_jid filter
	fromLID, err := db.SearchMessages(SearchMessagesOptions{Query: "marhaba", FromJID: tLID})
	if err != nil || len(fromLID) != 1 {
		t.Errorf("from filter: %d hits, err=%v", len(fromLID), err)
	}

	// media-only search over a shared word
	_, _ = db.Messages.Exec(`UPDATE messages SET content='shared word' WHERE id='M3'`)
	mediaHits, err := db.SearchMessages(SearchMessagesOptions{Query: "shared", Type: "text"})
	if err != nil || len(mediaHits) != 1 || mediaHits[0].ID != "M3" {
		t.Errorf("type filter on search: %d hits, err=%v", len(mediaHits), err)
	}
}

func TestGetChatName(t *testing.T) {
	db := fixtureDB(t)
	if got := db.GetChatName(tDirect); got != "Yusra Fixture" {
		t.Errorf("GetChatName = %q", got)
	}
	if got := db.GetChatName("unknown@s.whatsapp.net"); got != "" {
		t.Errorf("unknown chat name = %q, want empty", got)
	}
}

func msgIDs(ms []Message) []string {
	ids := make([]string, len(ms))
	for i, m := range ms {
		ids[i] = m.ID
	}
	return ids
}
