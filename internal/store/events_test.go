package store

// P2: durable watch event journal — append/since/latest/prune roundtrip.

import (
	"path/filepath"
	"testing"
	"time"
)

func TestWatchEventJournalRoundtrip(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	if latest, _ := db.LatestWatchEventSeq(); latest != 0 {
		t.Fatalf("latest on empty journal = %d, want 0", latest)
	}

	base := time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC)
	var last int64
	for i := 0; i < 3; i++ {
		seq, err := db.AppendWatchEvent("message", base.Add(time.Duration(i)*time.Second), `{"event":"message","n":1}`)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if seq <= last {
			t.Fatalf("seq not monotonic: %d after %d", seq, last)
		}
		last = seq
	}
	if latest, _ := db.LatestWatchEventSeq(); latest != last {
		t.Fatalf("latest = %d, want %d", latest, last)
	}

	rows, err := db.WatchEventsSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("since 0 -> %d rows, want 3", len(rows))
	}
	if rows[0].Kind != "message" || rows[2].Payload != `{"event":"message","n":1}` {
		t.Fatalf("roundtrip mismatch: %+v", rows[2])
	}

	rows, err = db.WatchEventsSince(last - 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("since %d -> %d rows, want 1 (exclusive cursor)", last-1, len(rows))
	}
}

func TestWatchEventJournalPrune(t *testing.T) {
	dir := t.TempDir()
	db := openT(t, filepath.Join(dir, "m.db"))

	base := time.Date(2026, 10, 11, 22, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		if _, err := db.AppendWatchEvent("message", base, "{}"); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.PruneWatchEvents(10); err != nil {
		t.Fatal(err)
	}
	rows, err := db.WatchEventsSince(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 10 {
		t.Fatalf("prune kept %d rows, want 10", len(rows))
	}
	if rows[0].Seq != 16 {
		t.Fatalf("prune kept wrong window: first seq %d, want 16", rows[0].Seq)
	}
}
