package whatsapp

import (
	"strings"
	"testing"

	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
)

// TestExtractContactVCard covers the contact-card extractor: a contact
// card yields its raw vCard body; a plain text message yields empty; a
// card with an empty vCard yields empty; nil yields empty.
func TestExtractContactVCard(t *testing.T) {
	body := "BEGIN:VCARD\nVERSION:3.0\nFN:Ahmad Khayri Omari\nTEL;type=CELL:+9647701234567\nEND:VCARD"

	// 1. Contact card: full body extracted verbatim.
	card := &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: protoString("Ahmad Khayri Omari"),
			Vcard:       protoString(body),
		},
	}
	if got := extractContactVCard(card); got != body {
		t.Fatalf("vcard body not extracted verbatim: %q", got)
	}

	// 2. Plain text message: empty.
	plain := &waE2E.Message{Conversation: protoString("just a message")}
	if got := extractContactVCard(plain); got != "" {
		t.Fatalf("plain message yielded vcard: %q", got)
	}

	// 3. Card with empty vCard: empty, not a display-name guess.
	empty := &waE2E.Message{
		ContactMessage: &waE2E.ContactMessage{
			DisplayName: protoString("Someone"),
		},
	}
	if got := extractContactVCard(empty); got != "" {
		t.Fatalf("empty vcard fabricated: %q", got)
	}

	// 4. Nil message: empty.
	if got := extractContactVCard(nil); got != "" {
		t.Fatalf("nil message yielded vcard: %q", got)
	}

	// 5. The display line and the body stay separate concerns:
	// extractTextContent still renders the 👤 line, not the body.
	if line := extractTextContent(card); !strings.HasPrefix(line, "👤 Ahmad Khayri Omari") || strings.Contains(line, "BEGIN:VCARD") {
		t.Fatalf("display line drifted: %q", line)
	}
}
