package cli

// Freezes the LIVE watch-event JSON envelope (the watch stream has no
// fixture-store goldens — this is its contract freeze):
//   - every live line carries schema_version "1.0"
//   - message events carry the canonical is_from_me / media_type fields
//     (store-model alignment, Oct 11 2026 consult) ALONGSIDE the legacy
//     from_me / type aliases, which stay load-bearing for downstream
//     consumers (autoreply.py reads them) until their v3.0 removal
//   - media-less messages omit media_type/type entirely (omitempty)
// Journaled replay (--since-seq) re-emits stored bodies verbatim, so
// pre-1.0 journal lines legitimately lack schema_version.
import (
	"bufio"
	"encoding/json"
	"os"
	"testing"

	"github.com/sinjab/pont/internal/whatsapp"
)

func captureEmit(t *testing.T, emit func()) []string {
	t.Helper()
	resetFlagGlobals()
	oldOut := os.Stdout
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = wOut
	emit()
	_ = wOut.Close()
	os.Stdout = oldOut

	var lines []string
	sc := bufio.NewScanner(rOut)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	return lines
}

func TestWatchEventEnvelopeContract(t *testing.T) {
	lines := captureEmit(t, func() {
		emitWatchEvent(watchEvent{Event: "message", Message: &whatsapp.MessageDescription{
			ID:        "3EB0ENVELOPE01",
			ChatJID:   "971500000000@s.whatsapp.net",
			Sender:    "971500000000@s.whatsapp.net",
			IsFromMe:  true,
			MediaType: "image",
			Content:   "contract freeze",
		}})
		emitWatchEvent(watchEvent{Event: "message", Message: &whatsapp.MessageDescription{
			ID:       "3EB0ENVELOPE02",
			ChatJID:  "971500000000@s.whatsapp.net",
			Sender:   "971500000000@s.whatsapp.net",
			IsFromMe: false,
			Content:  "no media",
		}})
		emitWatchEvent(watchEvent{Event: "connected"})
	})

	if len(lines) != 3 {
		t.Fatalf("expected 3 emitted lines, got %d", len(lines))
	}

	var withMedia, withoutMedia, connected map[string]any
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not JSON: %q: %v", l, err)
		}
		switch m["event"] {
		case "connected":
			connected = m
		default:
			msg, _ := m["message"].(map[string]any)
			if msg == nil {
				t.Fatalf("message event without message object: %q", l)
			}
			if _, has := msg["media_type"]; has {
				withMedia = m
			} else {
				withoutMedia = m
			}
		}
	}

	// schema_version on every live line
	for name, m := range map[string]map[string]any{"with_media": withMedia, "without_media": withoutMedia, "connected": connected} {
		if m == nil {
			t.Fatalf("missing %s event in emit output", name)
		}
		if got := m["schema_version"]; got != "1.0" {
			t.Errorf("%s: schema_version = %v, want \"1.0\"", name, got)
		}
	}

	// canonical fields present and agreeing with legacy aliases
	mm := withMedia["message"].(map[string]any)
	if mm["is_from_me"] != true || mm["from_me"] != true {
		t.Errorf("with_media: is_from_me=%v from_me=%v, want both true", mm["is_from_me"], mm["from_me"])
	}
	if mm["media_type"] != "image" || mm["type"] != "image" {
		t.Errorf("with_media: media_type=%v type=%v, want both \"image\"", mm["media_type"], mm["type"])
	}

	nm := withoutMedia["message"].(map[string]any)
	if nm["is_from_me"] != false || nm["from_me"] != false {
		t.Errorf("without_media: is_from_me=%v from_me=%v, want both false", nm["is_from_me"], nm["from_me"])
	}
	if _, has := nm["media_type"]; has {
		t.Errorf("without_media: media_type should be omitted when empty, got %v", nm["media_type"])
	}
	if _, has := nm["type"]; has {
		t.Errorf("without_media: type should be omitted when empty, got %v", nm["type"])
	}

	// non-message events carry the envelope but no message object
	if _, has := connected["message"]; has {
		t.Errorf("connected event should not carry a message object")
	}
}
