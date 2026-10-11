package whatsapp

// Golden test for the watch event DTO — the watcher's stdout JSONL is the
// autoreply engine's input contract. Freeze the marshal shape (field names,
// omitempty behavior) so no refactor changes it silently (P0, Oct 11 2026).

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var updateGoldens = flag.Bool("update", false, "update golden files instead of comparing")

func TestContractWatchEventShape(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)

	full := MessageDescription{
		ID:         "3EB0FIX0001",
		ChatJID:    "21639253782829@s.whatsapp.net",
		Sender:     "21639253782829@s.whatsapp.net",
		SenderAlt:  "21639253782829@lid",
		SenderName: "Yusra",
		PushName:   "Umm Karim",
		FromMe:     false,
		Timestamp:  ts,
		Type:       "image",
		DeviceSent: false,
	}
	minimal := MessageDescription{
		ID:        "3EB0FIX0002",
		ChatJID:   "21639253782829@s.whatsapp.net",
		Sender:    "21639253782829@s.whatsapp.net",
		FromMe:    true,
		Timestamp: ts,
	}
	deviceSent := minimal
	deviceSent.ID = "3EB0FIX0003"
	deviceSent.DeviceSent = true

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, d := range []MessageDescription{full, minimal, deviceSent} {
		if err := enc.Encode(d); err != nil {
			t.Fatalf("marshal: %v", err)
		}
	}

	gp := filepath.Join("testdata", "watch-event.golden")
	got := buf.String()
	if *updateGoldens {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(gp, []byte(got), 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(gp)
	if err != nil {
		t.Fatalf("missing golden (run -update): %v", err)
	}
	if string(want) != got {
		t.Errorf("watch event contract drift:\n--- golden ---\n%s--- got ---\n%s", want, got)
	}
}
