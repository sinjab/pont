package whatsapp

import (
	"strings"
	"testing"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
)

// TestExtractReplyInfo covers the quoted-reply extractor: an
// extended-text reply carries StanzaId + quoted snapshot; a plain
// message returns empty; a media reply extracts too; the snapshot is
// capped at 480 runes.
func TestExtractReplyInfo(t *testing.T) {
	quoted := &waE2E.Message{
		Conversation: protoString("الهاتف تطبيق الايميل"),
	}

	// 1. Extended-text reply.
	reply := &waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: protoString("ارسلت له هذا الرد على فكرة بعد مراجعتك"),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      protoString("3EB0QUOTED123"),
				QuotedMessage: quoted,
			},
		},
	}
	id, text := extractReplyInfo(reply)
	if id != "3EB0QUOTED123" {
		t.Fatalf("reply id not extracted: %q", id)
	}
	if !strings.Contains(text, "الايميل") {
		t.Fatalf("quoted snapshot not extracted: %q", text)
	}

	// 2. Plain message: no reply.
	plain := &waE2E.Message{Conversation: protoString("just a message")}
	id, text = extractReplyInfo(plain)
	if id != "" || text != "" {
		t.Fatalf("plain message misread as reply: %q %q", id, text)
	}

	// 3. Media reply (image with ContextInfo).
	imgReply := &waE2E.Message{
		ImageMessage: &waE2E.ImageMessage{
			Caption: protoString("look at this"),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      protoString("IMGQ1"),
				QuotedMessage: &waE2E.Message{Conversation: protoString("earlier")},
			},
		},
	}
	id, text = extractReplyInfo(imgReply)
	if id != "IMGQ1" || !strings.Contains(text, "earlier") {
		t.Fatalf("media reply not extracted: %q %q", id, text)
	}

	// 4. Snapshot cap: 600-rune quoted body truncates to 480 + ellipsis.
	long := strings.Repeat("ا", 600)
	id, text = extractReplyInfo(&waE2E.Message{
		ExtendedTextMessage: &waE2E.ExtendedTextMessage{
			Text: protoString("reply"),
			ContextInfo: &waE2E.ContextInfo{
				StanzaID:      protoString("LONG1"),
				QuotedMessage: &waE2E.Message{Conversation: protoString(long)},
			},
		},
	})
	if id != "LONG1" {
		t.Fatalf("long id: %q", id)
	}
	r := []rune(text)
	if len(r) != 481 || r[len(r)-1] != '…' {
		t.Fatalf("cap not applied: %d runes, tail %q", len(r), string(r[len(r)-1]))
	}
}
