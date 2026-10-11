package cli

// Contract golden tests — freeze the machine-readable output shapes that the
// orchestration layer (life-os, watcher hooks) depends on. Any intentional
// change to a shape MUST ship behind an explicit decision, not silently:
// regenerate goldens with `go test -tags sqlite_fts5 ./internal/cli/ -update`
// and review the diff like code (rewrite-consult P0, Oct 11 2026).

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eddmann/whatsapp-cli/internal/store"
)

var updateGoldens = flag.Bool("update", false, "update golden files instead of comparing")

const (
	fixtureChatNamed   = "21639253782829@s.whatsapp.net" // direct chat, stored name
	fixtureChatNoName  = "971501112233@s.whatsapp.net"   // direct chat, no name
	fixtureChatGroup   = "120363025801653866@g.us"       // group
	fixtureChatEmpty   = "9715550001111@s.whatsapp.net"  // chat row, zero messages
	fixtureLID         = "987654321098765@lid"
	fixtureLIDPhone    = "971501112233"
	fixtureLIDName     = "Kareem Fixture"
	fixtureSyncFreshAs = "2026-10-01T12:00:00Z" // metadata last_sync_time (blocks auto-sync)
)

// buildFixtureStore creates a deterministic messages.db and returns the store
// dir usable with SetStoreDir. The store is UNAUTHENTICATED (no session.db),
// so no command under test can touch the network.
func buildFixtureStore(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "store", "messages.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("fixture store.Open: %v", err)
	}
	defer db.CloseQuietly()

	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Messages.Exec(q, args...); err != nil {
			t.Fatalf("fixture exec %q: %v", q, err)
		}
	}

	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		fixtureChatNamed, "Yusra", "2026-10-01T09:04:00Z")
	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		fixtureChatNoName, nil, "2026-10-01T09:02:00Z")
	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		fixtureChatGroup, "Family Group", "2026-10-01T09:05:00Z")
	exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?,?,?)`,
		fixtureChatEmpty, "Empty Chat", nil)

	exec(`INSERT INTO lid_mappings (lid, phone, name, updated_at) VALUES (?,?,?,?)`,
		fixtureLID, fixtureLIDPhone, fixtureLIDName, "2026-10-01T08:00:00Z")

	type mrow struct {
		id, chat, sender, senderName, content, ts, mediaType, filename any
	}
	rows := []mrow{
		// plain inbound text
		{"3EB0FIX0001", fixtureChatNamed, fixtureChatNamed, nil, "Salam, how are you?", "2026-10-01T09:00:00Z", nil, nil},
		// outbound text (is_from_me = 1)
		{"3EB0FIX0002", fixtureChatNamed, fixtureChatNamed, nil, "All good, hamdulillah", "2026-10-01T09:01:00Z", nil, nil},
		// inbound image: no content, media metadata present
		{"3EB0FIX0003", fixtureChatNoName, fixtureChatNoName, nil, nil, "2026-10-01T09:02:00Z", "image", "cat.jpg"},
		// document with content caption
		{"3EB0FIX0004", fixtureChatNoName, fixtureChatNoName, nil, "the invoice", "2026-10-01T09:02:30Z", "document", "invoice.pdf"},
		// group message from a LID sender without a stored sender_name
		// (captures whatever sender resolution the query layer performs)
		{"3EB0FIX0005", fixtureChatGroup, fixtureLID, nil, "marhaba everyone", "2026-10-01T09:05:00Z", nil, nil},
		// group message with a stored sender_name
		{"3EB0FIX0006", fixtureChatGroup, fixtureChatNoName, "Sami Fixture", "ahlan Kareem", "2026-10-01T09:04:30Z", nil, nil},
	}
	for _, r := range rows {
		var isFromMe int
		if r.sender == r.chat && !strings.Contains(fmt.Sprint(r.chat), "@g.us") {
			// deterministic: messages 0002 is the outbound one
			isFromMe = map[string]int{"3EB0FIX0002": 1}[fmt.Sprint(r.id)]
		}
		exec(`INSERT INTO messages (id, chat_jid, sender, sender_name, content, timestamp, is_from_me, media_type, filename)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			r.id, r.chat, r.sender, r.senderName, r.content, r.ts, isFromMe, r.mediaType, r.filename)
	}

	exec(`INSERT INTO metadata (key, value) VALUES ('last_sync_time', ?)`, fixtureSyncFreshAs)
	return dir
}

// resetFlagGlobals mirrors process-per-command semantics: cobra binds flags
// to package globals once at init, so in-process Executes otherwise leak the
// previous invocation's flags (e.g. --fields) into later commands — a class
// production never sees (fresh process each run).
func resetFlagGlobals() {
	formatFlag, fieldsFlag = "", ""
	noHeaderFlag, verbose, noAutoSync = false, false, false
	storeDir = ""
	timeout = 0
	resolvedFormat = ""

	chatsQuery, chatsGroups, chatsLimit = "", false, 50
	messagesLimit, messagesBefore, messagesAfter = 50, "", ""
	messagesTimeframe, messagesType = "", ""
	searchChat, searchFrom, searchType = "", "", ""
	searchTimeframe, searchLimit = "", 50
}

// runFixtureCmd executes the root command in-process against the fixture
// store, returning captured stdout. stderr is swallowed (warnings only).
func runFixtureCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	resetFlagGlobals()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr

	SetStoreDir(dir)
	rootCmd.SetArgs(args)
	cmdErr := rootCmd.Execute()

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	_, _ = io.ReadAll(rErr)

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rOut)
	out := buf.String()
	if cmdErr != nil {
		t.Fatalf("command %v failed: %v", args, cmdErr)
	}
	return out
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	gp := filepath.Join("testdata", name+".golden")
	if *updateGoldens {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(gp, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(gp)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update to create): %v", gp, err)
	}
	if string(want) != got {
		t.Errorf("contract drift in %s:\n--- golden ---\n%s\n--- got ---\n%s", name, want, got)
	}
}

func TestContractChatsJSON(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "json")
	checkGolden(t, "chats-json", got)
}

func TestContractChatsJSONL(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "jsonl")
	checkGolden(t, "chats-jsonl", got)
}

func TestContractChatsFieldsSubset(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "json", "--fields", "jid,name")
	checkGolden(t, "chats-json-fields", got)
}

func TestContractChatsCSVNoHeader(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "csv", "--no-header")
	checkGolden(t, "chats-csv-noheader", got)
}

func TestContractMessagesJSON(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "messages", fixtureChatNamed, "--format", "json")
	checkGolden(t, "messages-json", got)
}

func TestContractMessagesMediaJSON(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "messages", fixtureChatNoName, "--format", "json")
	checkGolden(t, "messages-media-json", got)
}

func TestContractMessagesGroupLIDJSON(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "messages", fixtureChatGroup, "--format", "json")
	checkGolden(t, "messages-group-lid-json", got)
}

func TestContractMessagesEmptyChat(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "messages", fixtureChatEmpty, "--format", "json")
	checkGolden(t, "messages-empty-json", got)
}

func TestContractMessagesWindowFilter(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "messages", fixtureChatNamed,
		"--format", "json", "--after", "2026-10-01T09:00:30Z", "--before", "2026-10-01T09:01:30Z")
	checkGolden(t, "messages-window-json", got)
}

func TestContractSearchJSON(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "search", "invoice", "--format", "json")
	checkGolden(t, "search-json", got)
}

func TestContractAuthStatusUnauthenticated(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "auth", "status", "--format", "json")
	checkGolden(t, "auth-status-json", got)
}

// TestContractNoNetworkGuard proves the read path stays offline: the fixture
// is unauthenticated, so any network attempt would fail the command instead
// of silently connecting (auto-sync must skip on fresh last_sync_time).
func TestContractNoNetworkGuard(t *testing.T) {
	dir := buildFixtureStore(t)
	start := time.Now()
	_ = runFixtureCmd(t, dir, "chats", "--format", "json")
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("read command took %v — something tried the network", d)
	}
}
