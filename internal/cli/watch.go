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

	"github.com/sinjab/pont/internal/store"
	"github.com/sinjab/pont/internal/whatsapp"

	"go.mau.fi/whatsmeow/types"
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
message.ack (delivery/read/played receipts), connected, disconnected,
keepalive_timeout, offline_sync_completed and terminal events. It never
sends messages and never mutates any other store.

Every event is journaled durably (watch_events table in the same store)
BEFORE it is emitted, so a restarted consumer loses nothing:

    whatsapp watch --since-seq N

first replays all journaled events after seq N (each replayed line carries
its "seq"), then continues live (live lines carry "seq" too in this mode).
Without --since-seq the output is byte-identical to the classic stream.

Optional fan-out sinks (live events only, journal-first, never fatal to
the watcher): --socket <path> broadcasts every live line to Unix-socket
subscribers (they may connect and disconnect at any time); --on-message
<cmd> runs a command (via sh -c) per message and message.ack event with
the JSON line on stdin. Dropped subscribers, failed or saturated hooks
are warnings — the journal still holds everything.

Auto-reconnects on unclean disconnects. Terminal states exit non-zero:
3 = logged out, 4 = temporary ban, 5 = client outdated, 6 = stream replaced.`,
	RunE: runWatch,
}

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.Flags().StringArrayVar(&watchJIDs, "jid", nil, "Only emit messages for these chat JIDs (repeatable; default: all)")
	watchCmd.Flags().Int64Var(&watchSinceSeq, "since-seq", -1, "Replay journaled events after this seq, then continue live (adds \"seq\" to every line)")
	watchCmd.Flags().StringVar(&watchSocket, "socket", "", "Unix socket path to broadcast every live event line to (subscribers may connect/disconnect freely)")
	watchCmd.Flags().StringVar(&watchOnMessage, "on-message", "", "Command (run via sh -c) invoked per message and message.ack event with the JSON line on stdin")
}

// watchSchemaVersion stamps every LIVE-emitted event line (contract
// normalization, Oct 11 2026 consult; bumped to 1.1 with the
// message.ack event kind, Oct 18 2026). Journaled lines replayed via
// --since-seq are re-emitted verbatim from the journal, so events
// journaled before a given version existed carry their original stamp
// (or none, pre-1.0) — consumers must treat absence as legacy shape.
const watchSchemaVersion = "1.1"

type watchEvent struct {
	Event         string                       `json:"event"`
	TS            time.Time                    `json:"ts"`
	SchemaVersion string                       `json:"schema_version"`
	Message       *whatsapp.MessageDescription `json:"message,omitempty"`
	Ack           *watchAckEvent               `json:"ack,omitempty"`
	Reason        string                       `json:"reason,omitempty"`
	Count         int                          `json:"count,omitempty"`
}

// watchAckEvent is the payload of a message.ack event: whatsmeow's
// events.Receipt (delivery/read/played confirmations, verified against
// the pinned whatsmeow v0.0.0-20261007111105 — this version has no
// events.MessageAck; receipts arrive solely as events.Receipt).
type watchAckEvent struct {
	ChatJID     string    `json:"chat_jid"`         // chat the receipted messages live in
	Sender      string    `json:"sender,omitempty"` // who issued the receipt (group reads: the reader; empty in 1:1)
	MessageIDs  []string  `json:"message_ids"`      // affected message IDs (order as sent by the server — Android and iOS differ)
	ReceiptType string    `json:"receipt_type"`     // delivered | read | read-self | played | played-self | sender | retry | ...
	Timestamp   time.Time `json:"timestamp"`        // source timestamp from WhatsApp (not emit time)
	IsFromMe    bool      `json:"is_from_me"`       // the receipted messages were sent by this account (canonical field name, matches message events)
}

// receiptTypeName renders a whatsmeow receipt type for the wire. The
// delivered type is the protocol's zero value (empty string) — surface
// it explicitly so consumers never see receipt_type:"".
func receiptTypeName(t types.ReceiptType) string {
	if t == "" {
		return "delivered"
	}
	return string(t)
}

// ackEventFromReceipt shapes a whatsmeow receipt into the journaled
// message.ack watch event. Envelope TS stays wall-clock emit time like
// every other event; the receipt's own timestamp rides in ack.timestamp
// (falling back to emit time when the server sent none, so consumers
// never parse year-1 zeroes).
func ackEventFromReceipt(v *events.Receipt) watchEvent {
	now := time.Now()
	ts := v.Timestamp
	if ts.IsZero() {
		ts = now
	}
	return watchEvent{
		Event: "message.ack",
		TS:    now,
		Ack: &watchAckEvent{
			ChatJID:     v.Chat.String(),
			Sender:      v.Sender.String(),
			MessageIDs:  append([]string{}, v.MessageIDs...),
			ReceiptType: receiptTypeName(v.Type),
			Timestamp:   ts,
			IsFromMe:    v.IsFromMe,
		},
	}
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
	ev.SchemaVersion = watchSchemaVersion
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
	// Fan-out sinks (opt-in, never load-bearing): socket subscribers
	// get every live line; the hook fires on message and message.ack
	// events. Both are strictly AFTER journal + stdout — the durability
	// and classic-stream contracts come first.
	if s := watchSock; s != nil {
		s.broadcast(append(append([]byte{}, line...), '\n'))
	}
	if (ev.Event == "message" || ev.Event == "message.ack") && watchOnMessage != "" {
		fireMessageHook([]byte(watchOnMessage), append(append([]byte{}, line...), '\n'))
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

	// Socket sink: live events only (the replay above is consumer
	// catch-up and stays stdout-only). Started after replay, before
	// connection, so early subscribers see the "started" event first.
	if err := startSocketSink(watchSocket); err != nil {
		return err
	}
	defer closeSocketSink()

	client, err := whatsapp.New(db, GetStoreDir(), IsVerbose(), nil)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	if !client.IsAuthenticated() {
		return fmt.Errorf("not authenticated. Run 'pont auth login' first (with the same --store)")
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
		case *events.Receipt:
			// Delivery/read/played receipts for outgoing messages (and
			// other-device reads of incoming ones). Same --jid filter as
			// message events: an instance scoped to one chat sees only
			// that chat's receipts.
			if len(jidSet) > 0 && !jidSet[v.Chat.String()] {
				return
			}
			emitWatchEvent(ackEventFromReceipt(v))
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
