package cli

// Per-instance data directory (PONT_DATA_DIR / --data-dir): proves the
// two guarantees the multi-instance deployment relies on —
//   - two data dirs hold two fully separate stores: a journal append in
//     instance A is invisible to instance B (isolation)
//   - with no flag and no env var, path resolution is byte-identical to
//     the classic behavior (default unchanged)
// All stores live in t.TempDir() — the operator's real session/message
// DBs are never touched.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sinjab/pont/internal/store"
)

// resolveWithEnv snapshots the resolution inputs so each test is
// hermetic regardless of the environment it runs under.
func resolveWithEnv(t *testing.T, storeFlag, dataFlag, env string) string {
	t.Helper()
	resetFlagGlobals()
	t.Setenv("PONT_DATA_DIR", env)
	t.Setenv("XDG_CONFIG_HOME", "")
	storeDir, dataDir = storeFlag, dataFlag
	initConfig()
	return GetConfigDir()
}

func TestDataDirDefaultUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := resolveWithEnv(t, "", "", "")

	want := filepath.Join(home, ".config", "whatsapp-cli")
	if got != want {
		t.Fatalf("default config dir = %q, want %q", got, want)
	}
	if p := GetSessionDBPath(); p != filepath.Join(want, "store", "session.db") {
		t.Errorf("session db path = %q", p)
	}
	if p := GetMessagesDBPath(); p != filepath.Join(want, "store", "messages.db") {
		t.Errorf("messages db path = %q", p)
	}
}

func TestDataDirEnvAndFlagPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	defaultDir := filepath.Join(home, ".config", "whatsapp-cli")

	cases := []struct {
		name      string
		storeFlag string
		dataFlag  string
		env       string
		want      string
	}{
		{"env overrides default", "", "", "/tmp/instance-a", "/tmp/instance-a"},
		{"data-dir flag beats env", "", "/tmp/flag", "/tmp/env", "/tmp/flag"},
		{"store flag beats data-dir flag", "/tmp/store", "/tmp/flag", "/tmp/env", "/tmp/store"},
		{"no flag no env keeps default", "", "", "", defaultDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveWithEnv(t, tc.storeFlag, tc.dataFlag, tc.env)
			if got != tc.want {
				t.Fatalf("GetConfigDir() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDataDirCreated0700(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "nested", "instance-a")
	resolveWithEnv(t, "", dir, "")

	if err := EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories: %v", err)
	}
	for _, d := range []string{dir, GetStoreDir(), GetMediaDir()} {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s perms = %o, want 700", d, fi.Mode().Perm())
		}
	}
}

func TestDataDirJournalIsolation(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")

	openJournal := func(dir string) (*store.DB, string) {
		t.Helper()
		resetFlagGlobals()
		SetStoreDir(dir)
		if err := EnsureDirectories(); err != nil {
			t.Fatalf("EnsureDirectories(%s): %v", dir, err)
		}
		dbPath := GetMessagesDBPath()
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatalf("store.Open under %s: %v", dir, err)
		}
		return db, dbPath
	}

	dbA, pathA := openJournal(dirA)
	defer dbA.CloseQuietly()
	dbB, pathB := openJournal(dirB)
	defer dbB.CloseQuietly()

	// Physically distinct DB files, each under its own data dir.
	if pathA == pathB {
		t.Fatalf("both instances resolved the same messages.db: %s", pathA)
	}
	if want := filepath.Join(dirA, "store", "messages.db"); pathA != want {
		t.Errorf("instance A messages.db = %q, want %q", pathA, want)
	}
	if want := filepath.Join(dirB, "store", "messages.db"); pathB != want {
		t.Errorf("instance B messages.db = %q, want %q", pathB, want)
	}

	// Instance A journals a message; instance B journals an ack.
	ts := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	if _, err := dbA.AppendWatchEvent("message", ts, `{"event":"message"}`); err != nil {
		t.Fatalf("A append: %v", err)
	}
	if _, err := dbB.AppendWatchEvent("message.ack", ts, `{"event":"message.ack"}`); err != nil {
		t.Fatalf("B append: %v", err)
	}

	rowsA, err := dbA.WatchEventsSince(0)
	if err != nil {
		t.Fatalf("A replay: %v", err)
	}
	rowsB, err := dbB.WatchEventsSince(0)
	if err != nil {
		t.Fatalf("B replay: %v", err)
	}
	if len(rowsA) != 1 || rowsA[0].Kind != "message" {
		t.Fatalf("instance A journal = %+v, want its own single message event", rowsA)
	}
	if len(rowsB) != 1 || rowsB[0].Kind != "message.ack" {
		t.Fatalf("instance B journal = %+v, want its own single message.ack event", rowsB)
	}
}
