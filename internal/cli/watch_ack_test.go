package cli

// message.ack contract freeze (schema 1.1, Oct 18 2026): whatsmeow
// receipts (events.Receipt — the pinned version has no events.MessageAck)
// surface as journaled message.ack events. This file pins the mapping:
//   - envelope: event=message.ack, schema_version=1.1, no message object
//   - ack payload: chat_jid, sender (omitted when empty), message_ids
//     (server order preserved), receipt_type, source timestamp, is_from_me
//   - whatsmeow's "delivered" receipt type is the protocol zero value
//     (empty string) and must surface explicitly as "delivered"
import (
	"encoding/json"
	"testing"
	"time"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestAckEventFromReceiptMapping(t *testing.T) {
	chat := types.NewJID("971500000000", types.DefaultUserServer)
	reader := types.NewJID("971555000111", types.DefaultUserServer)
	ts := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)

	// Delivered: zero-value ReceiptType, empty sender (1:1 chat).
	delivered := &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, IsFromMe: true},
		MessageIDs:    []types.MessageID{"3EB0ACK0001", "3EB0ACK0002"},
		Timestamp:     ts,
	}
	ev := ackEventFromReceipt(delivered)
	if ev.Event != "message.ack" || ev.Ack == nil {
		t.Fatalf("event = %+v, want message.ack with ack payload", ev)
	}
	if ev.Ack.ReceiptType != "delivered" {
		t.Errorf("zero ReceiptType = %q, want explicit \"delivered\"", ev.Ack.ReceiptType)
	}
	if ev.Ack.ChatJID != chat.String() {
		t.Errorf("chat_jid = %q, want %q", ev.Ack.ChatJID, chat.String())
	}
	if ev.Ack.Sender != "" {
		t.Errorf("empty sender should be omitted, got %q", ev.Ack.Sender)
	}
	if len(ev.Ack.MessageIDs) != 2 || ev.Ack.MessageIDs[0] != "3EB0ACK0001" || ev.Ack.MessageIDs[1] != "3EB0ACK0002" {
		t.Errorf("message_ids = %v, want server order preserved", ev.Ack.MessageIDs)
	}
	if !ev.Ack.Timestamp.Equal(ts) {
		t.Errorf("ack timestamp = %v, want source timestamp %v", ev.Ack.Timestamp, ts)
	}
	if !ev.Ack.IsFromMe {
		t.Errorf("is_from_me = false, want true for outgoing-message receipts")
	}

	// Group read: sender is the reader, explicit type passes through.
	read := &events.Receipt{
		MessageSource: types.MessageSource{Chat: chat, Sender: reader, IsGroup: true},
		MessageIDs:    []types.MessageID{"3EB0ACK0003"},
		Timestamp:     ts,
		Type:          types.ReceiptTypeRead,
	}
	ev = ackEventFromReceipt(read)
	if ev.Ack.ReceiptType != "read" {
		t.Errorf("receipt_type = %q, want read", ev.Ack.ReceiptType)
	}
	if ev.Ack.Sender != reader.String() {
		t.Errorf("sender = %q, want %q", ev.Ack.Sender, reader.String())
	}
}

func TestAckEnvelopeContract(t *testing.T) {
	ts := time.Date(2026, 10, 18, 9, 31, 0, 0, time.UTC)
	lines := captureEmit(t, func() {
		emitWatchEvent(watchEvent{
			Event: "message.ack",
			Ack: &watchAckEvent{
				ChatJID:     "971500000000@s.whatsapp.net",
				MessageIDs:  []string{"3EB0ACK0009"},
				ReceiptType: "read",
				Timestamp:   ts,
				IsFromMe:    true,
			},
		})
	})
	if len(lines) != 1 {
		t.Fatalf("expected 1 emitted line, got %d", len(lines))
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("ack line not JSON: %q: %v", lines[0], err)
	}
	if m["event"] != "message.ack" {
		t.Errorf("event = %v", m["event"])
	}
	if m["schema_version"] != "1.1" {
		t.Errorf("schema_version = %v, want 1.1", m["schema_version"])
	}
	if _, has := m["message"]; has {
		t.Errorf("message.ack must not carry a message object: %q", lines[0])
	}
	ack, _ := m["ack"].(map[string]any)
	if ack == nil {
		t.Fatalf("message.ack without ack object: %q", lines[0])
	}
	for _, k := range []string{"chat_jid", "message_ids", "receipt_type", "timestamp", "is_from_me"} {
		if _, has := ack[k]; !has {
			t.Errorf("ack payload missing %s: %q", k, lines[0])
		}
	}
	if ack["receipt_type"] != "read" || ack["is_from_me"] != true {
		t.Errorf("ack payload wrong: %q", lines[0])
	}
}

func TestReceiptTypeName(t *testing.T) {
	cases := map[types.ReceiptType]string{
		"":                           "delivered",
		types.ReceiptTypeRead:        "read",
		types.ReceiptTypeReadSelf:    "read-self",
		types.ReceiptTypePlayed:      "played",
		types.ReceiptTypePlayedSelf:  "played-self",
		types.ReceiptTypeSender:      "sender",
		types.ReceiptTypeRetry:       "retry",
		types.ReceiptTypeServerError: "server-error",
		types.ReceiptTypeHistorySync: "hist_sync",
	}
	for in, want := range cases {
		if got := receiptTypeName(in); got != want {
			t.Errorf("receiptTypeName(%q) = %q, want %q", in, got, want)
		}
	}
}
