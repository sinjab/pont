package cli

// --jq contract tests: gh-style expression filtering over the JSON
// output paths (P4, Oct 12 2026). Fixture-store driven like the
// contract goldens, with explicit expected strings.
import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestJQOverJSONArray(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "json", "--jq", ".[].name")
	// chats sort by last_message_time DESC: Family Group (09:05),
	// Yusra (09:04), the nameless chat (09:02), Empty Chat (nil).
	// omitempty drops the key at marshal time, so .name on the nameless
	// chat is jq null — faithful jq semantics; users filter with
	// select(. != null).
	want := "Family Group\nYusra\nnull\nEmpty Chat\n"
	if got != want {
		t.Errorf("jq over json array:\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

func TestJQOverJSONLElements(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "jsonl", "--jq", ".jid")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("jq over jsonl: want 4 lines, got %d: %q", len(lines), got)
	}
	// Same recency order: group first, empty chat last.
	if lines[0] != "120363025801653866@g.us" {
		t.Errorf("jsonl per-element jq wrong first line: %q", lines[0])
	}
	if lines[3] != "9715550001111@s.whatsapp.net" {
		t.Errorf("jsonl per-element jq wrong last line: %q", lines[3])
	}
}

func TestJQObjectResultsCompact(t *testing.T) {
	dir := buildFixtureStore(t)
	got := runFixtureCmd(t, dir, "chats", "--format", "json", "--jq", ".[0] | {jid, name}")
	want := `{"jid":"120363025801653866@g.us","name":"Family Group"}` + "\n"
	if got != want {
		t.Errorf("jq object result:\n--- want ---\n%q\n--- got ---\n%q", want, got)
	}
}

func TestJQWithFieldsPreFilter(t *testing.T) {
	dir := buildFixtureStore(t)
	// --fields applies BEFORE --jq: the query sees only selected keys.
	got := runFixtureCmd(t, dir, "messages", fixtureChatNamed, "--format", "jsonl", "--fields", "id,content", "--jq", ".content")
	if !strings.Contains(got, "Salam, how are you?") {
		t.Errorf("fields+jq pipeline wrong: %q", got)
	}
	if strings.Contains(got, "\"chat_jid\"") {
		t.Errorf("fields filter should have removed chat_jid before jq: %q", got)
	}
}

func TestJQInvalidExpressionFails(t *testing.T) {
	dir := buildFixtureStore(t)
	got, err := runFixtureCmdErr(t, dir, "chats", "--format", "json", "--jq", ".[")
	_ = got
	if err == nil {
		t.Fatalf("invalid jq expression must fail, got output %q", got)
	}
}

func TestJQRejectsNonJSONFormats(t *testing.T) {
	dir := buildFixtureStore(t)
	_, err := runFixtureCmdErr(t, dir, "chats", "--format", "csv", "--jq", ".[].name")
	if err == nil {
		t.Fatal("--jq with csv must fail loudly, not no-op")
	}
}

// runFixtureCmdErr is runFixtureCmd but returning the command error
// instead of failing the test on it (for expected-failure cases).
func runFixtureCmdErr(t *testing.T, dir string, args ...string) (string, error) {
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

	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	_, _ = io.ReadAll(rErr)

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rOut)
	return buf.String(), cmdErr
}
