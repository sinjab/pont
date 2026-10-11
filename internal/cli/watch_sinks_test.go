package cli

// Sink contract tests (P2 tail): the socket broadcast bus must deliver
// the same JSONL bytes as stdout, survive subscriber death without
// touching the watcher, and the --on-message hook must fire on message
// events only, receiving the full line on stdin.
import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sinjab/pont/internal/whatsapp"
)

// awaitSubscriber blocks until the sink reports the given subscriber
// count (dial-success precedes accept-registration by a window).
func awaitSubscriber(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		s := watchSock
		if s != nil && s.subscriberCount() == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("subscriber count never reached %d", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSocketSinkBroadcastAndSurvival(t *testing.T) {
	// macOS caps unix-socket paths at ~104 bytes (sun_path); the
	// default t.TempDir() under /var/folders overflows it. Use a short
	// /tmp path — real deployments use ~/.config/<name>/watch.sock.
	dir, err := os.MkdirTemp("/tmp", "ws")
	if err != nil {
		t.Fatalf("tmpdir: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sockPath := filepath.Join(dir, "watch.sock")
	if err := startSocketSink(sockPath); err != nil {
		t.Fatalf("startSocketSink: %v", err)
	}
	defer closeSocketSink()

	c1, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c1.Close() }()
	awaitSubscriber(t, 1)

	emitWatchEvent(watchEvent{Event: "connected"})

	line, err := bufio.NewReader(c1).ReadString('\n')
	if err != nil {
		t.Fatalf("subscriber read: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("socket line not JSON: %q: %v", line, err)
	}
	if m["event"] != "connected" || m["schema_version"] != "1.0" {
		t.Errorf("socket payload wrong: %s", line)
	}

	// Subscriber death must not break the watcher: kill c1, emit again,
	// a fresh subscriber must still receive the next live line.
	_ = c1.Close()
	emitWatchEvent(watchEvent{Event: "message"})

	c2, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("re-dial: %v", err)
	}
	defer func() { _ = c2.Close() }()
	awaitSubscriber(t, 1) // c1 dropped by the broadcast above, c2 registered
	emitWatchEvent(watchEvent{Event: "disconnected", Reason: "test"})
	line2, err := bufio.NewReader(c2).ReadString('\n')
	if err != nil {
		t.Fatalf("second subscriber read: %v", err)
	}
	var m2 map[string]any
	if err := json.Unmarshal([]byte(line2), &m2); err != nil {
		t.Fatalf("second line not JSON: %q", line2)
	}
	if m2["event"] != "disconnected" {
		t.Errorf("second subscriber got wrong event: %s", line2)
	}
}

func TestMessageHookFiresOnMessagesOnly(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "hook-out")
	watchOnMessage = "cat > " + out
	defer func() { watchOnMessage = "" }()

	emitWatchEvent(watchEvent{Event: "connected"}) // not a message: no hook
	emitWatchEvent(watchEvent{Event: "message", Message: &whatsapp.MessageDescription{
		ID:      "3EB0SINKTEST01",
		ChatJID: "971500000000@s.whatsapp.net",
		Content: "hook me",
	}})

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(out); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hook output never appeared at %s", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read hook output: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("hook stdin was not the JSON line: %q", got)
	}
	if m["event"] != "message" {
		t.Errorf("hook payload event = %v, want message", m["event"])
	}
}
