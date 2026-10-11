package cli

// P2: the seq-prefix splice must not disturb the classic (seq-less) watch
// line shape — the golden contract stays byte-identical without --since-seq.

import (
	"encoding/json"
	"testing"
)

func TestWithSeqPrefix(t *testing.T) {
	body := []byte(`{"event":"message","ts":"2026-10-11T22:00:00Z"}`)
	got := string(withSeqPrefix(42, body))
	if got != `{"seq":42,"event":"message","ts":"2026-10-11T22:00:00Z"}` {
		t.Fatalf("splice wrong: %s", got)
	}
	// The spliced line must still be valid JSON with the original fields.
	var m map[string]any
	if err := json.Unmarshal([]byte(got), &m); err != nil {
		t.Fatalf("spliced line not valid JSON: %v", err)
	}
	if m["event"] != "message" || m["seq"].(float64) != 42 {
		t.Fatalf("spliced fields wrong: %+v", m)
	}
	// Non-object bodies pass through untouched.
	if string(withSeqPrefix(1, []byte(`null`))) != `null` {
		t.Fatal("non-object body mutated")
	}
}
