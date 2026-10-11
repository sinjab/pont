package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/eddmann/whatsapp-cli/internal/store"
	"github.com/eddmann/whatsapp-cli/internal/whatsapp"

	"go.mau.fi/whatsmeow/types/events"
)

// Terminal exit codes. A supervisor watching this process should stop
// (or long-backoff) retrying on these: retrying cannot fix them.
const (
	watchExitLoggedOut      = 3 // session invalidated; needs QR re-pair
	watchExitTemporaryBan   = 4 // server-side temporary ban
	watchExitClientOutdated = 5 // protocol too old; rebuild with whatsmeow bump
	watchExitStreamReplaced = 6 // another connection took over this device session
)

var watchJIDs []string
var watchSinceSeq int64

// watchJournalRetention bounds the durable event journal (pruned at startup).
const watchJournalRetention = 10000

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Stream live events as JSON lines on stdout",
	Long: `Connect to WhatsApp and stream real-time events as JSON lines (JSONL) on stdout.

Designed to run as a long-lived companion process against its own --store
directory (a separate linked device): one JSON object per line for message,
connected, disconnected, keepalive_timeout, offline_sync_completed and
terminal events. It never sends messages and never mutates any other store.

Every event is journaled durably (watch_events table in the same store)
BEFORE it is emitted, so a restarted consumer loses nothing:

    whatsapp watch --since-seq N

first replays all journaled events after seq N (each replayed line carries
its "seq"), then continues live (live lines carry "seq" too in this mode).
Without --since-seq the output is byte-identical to the classic stream.

Auto-reconnects on unclean disconnects. Terminal states exit non-zero:
3 = logged out, 4 = temporary ban, 5 = client outdated, 6 = stream replaced.`,
	RunE: runWatch,
}

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.Flags().StringArrayVar(&watchJIDs, "jid", nil, "Only emit messages for these chat JIDs (repeatable; default: all)")
	watchCmd.Flags().Int64Var(&watchSinceSeq, "since-seq", -1, "Replay journaled events after this seq, then continue live (adds \"seq\" to every line)")
}

type watchEvent struct {
	Event   string                       `json:"event"`
	TS      time.Time                    `json:"ts"`
	Message *whatsapp.MessageDescription `json:"message,omitempty"`
	Reason  string                       `json:"reason,omitempty"`
	Count   int                          `json:"count,omitempty"`
}

// journalDB and seqMode wire the durable event journal into the emit path.
// journalDB is set by runWatch once the store is open; seqMode is true when
// --since-seq was passed (replay + live lines carry their journal seq).
var (
	journalDB *store.DB
	seqMode   bool
)

// withSeqPrefix splices a seq field in front of a marshaled JSON object:
// {"event":...} -> {"seq":N,"event":...}. String surgery on the encoder
// output keeps the emitted field order deterministic and the classic shape
// (without seq) untouched.
func withSeqPrefix(seq int64, body []byte) []byte {
	if len(body) == 0 || body[0] != '{' {
		return body
	}
	out := make([]byte, 0, len(body)+16)
	out = append(out, `{"seq":`...)
	out = strconv.AppendInt(out, seq, 10)
	out = append(out, ',')
	out = append(out, body[1:]...)
	return out
}

// emitWatchEvent journals the event durably FIRST, then writes it to stdout.
// Write errors (e.g. closed pipe) are fatal for a stream process — but the
// journal already holds the event, so a restarted consumer replays it via
// --since-seq instead of losing it.
func emitWatchEvent(ev watchEvent) {
	if ev.TS.IsZero() {
		ev.TS = time.Now()
	}
	body, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watch: marshal event failed: %v\n", err)
		return
	}
	var line = body
	if journalDB != nil {
		seq, jerr := journalDB.AppendWatchEvent(ev.Event, ev.TS, string(body))
		if jerr != nil {
			// Journal is durability, not the live path: warn and stream on.
			fmt.Fprintf(os.Stderr, "watch: journal append failed: %v\n", jerr)
		} else if seqMode {
			line = withSeqPrefix(seq, body)
		}
	}
	if _, err := os.Stdout.Write(append(line, '\n')); err != nil {
		fmt.Fprintf(os.Stderr, "watch: stdout write failed: %v\n", err)
		os.Exit(1)
	}
}

func runWatch(cmd *cobra.Command, args []string) error {
	if err := EnsureDirectories(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	db, err := store.Open(GetMessagesDBPath())
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.CloseQuietly()

	// Wire the durable journal; prune to bound it.
	journalDB = db
	if err := db.PruneWatchEvents(watchJournalRetention); err != nil {
		fmt.Fprintf(os.Stderr, "watch: journal prune failed: %v\n", err)
	}

	// Replay mode: emit everything the journal holds after the cursor
	// BEFORE connecting, so the consumer sees a single ordered stream.
	if watchSinceSeq >= 0 {
		seqMode = true
		rows, rerr := db.WatchEventsSince(watchSinceSeq)
		if rerr != nil {
			return fmt.Errorf("journal replay failed: %w", rerr)
		}
		for _, r := range rows {
			line := withSeqPrefix(r.Seq, []byte(r.Payload))
			if _, err := os.Stdout.Write(append(line, '\n')); err != nil {
				return fmt.Errorf("replay write failed: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "watch: replayed %d journaled events after seq %d\n", len(rows), watchSinceSeq)
	}

	client, err := whatsapp.New(db, GetStoreDir(), IsVerbose(), nil)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if !client.IsAuthenticated() {
		return fmt.Errorf("not authenticated. Run 'whatsapp auth login' first (with the same --store)")
	}

	client.WA.EnableAutoReconnect = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		signal.Stop(sigChan)
		fmt.Fprintln(os.Stderr, "\nInterrupted, disconnecting...")
		cancel()
	}()

	terminal := make(chan int, 1)

	jidSet := make(map[string]bool, len(watchJIDs))
	for _, j := range watchJIDs {
		jidSet[j] = true
	}

	client.WA.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			if len(jidSet) > 0 && !jidSet[v.Info.Chat.String()] {
				return
			}
			desc := client.DescribeMessage(v)
			emitWatchEvent(watchEvent{Event: "message", TS: time.Now(), Message: &desc})
		case *events.Connected:
			emitWatchEvent(watchEvent{Event: "connected"})
		case *events.Disconnected:
			emitWatchEvent(watchEvent{Event: "disconnected", Reason: "socket closed; auto-reconnect pending"})
		case *events.KeepAliveTimeout:
			emitWatchEvent(watchEvent{Event: "keepalive_timeout", Reason: fmt.Sprintf("error count %d", v.ErrorCount)})
		case *events.OfflineSyncCompleted:
			emitWatchEvent(watchEvent{Event: "offline_sync_completed", Count: v.Count})
		case *events.StreamReplaced:
			emitWatchEvent(watchEvent{Event: "stream_replaced", Reason: "another connection took over this device session"})
			select {
			case terminal <- watchExitStreamReplaced:
			default:
			}
		case *events.LoggedOut:
			emitWatchEvent(watchEvent{Event: "logged_out", Reason: fmt.Sprintf("on_connect=%v reason=%v", v.OnConnect, v.Reason)})
			select {
			case terminal <- watchExitLoggedOut:
			default:
			}
		case *events.TemporaryBan:
			emitWatchEvent(watchEvent{Event: "temporary_ban", Reason: v.String()})
			select {
			case terminal <- watchExitTemporaryBan:
			default:
			}
		case *events.ClientOutdated:
			emitWatchEvent(watchEvent{Event: "client_outdated", Reason: "rebuild with a newer whatsmeow"})
			select {
			case terminal <- watchExitClientOutdated:
			default:
			}
		}
	})

	if err := client.Connect(); err != nil {
		return fmt.Errorf("connection failed: %w", err)
	}
	emitWatchEvent(watchEvent{Event: "started"})

	select {
	case <-ctx.Done():
	case code := <-terminal:
		client.Disconnect()
		fmt.Fprintf(os.Stderr, "watch: terminal state, exiting %d\n", code)
		os.Exit(code)
	}

	client.Disconnect()
	return nil
}
