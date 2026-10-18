package whatsapp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestUnknownDeliveryErrorText(t *testing.T) {
	cause := &whatsmeow.DisconnectedError{Action: "message send"}
	err := &UnknownDeliveryError{ID: "3EB0TEST", ChatJID: "1234@s.whatsapp.net", Err: cause}

	text := err.Error()
	for _, want := range []string{
		"send state unknown",
		"id=3EB0TEST",
		"1234@s.whatsapp.net",
		"do NOT resend",
		"pont delete 3EB0TEST --chat 1234@s.whatsapp.net",
		"websocket disconnected before message send returned response",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("error text missing %q: %s", want, text)
		}
	}

	if !errors.Is(err, cause) {
		t.Error("UnknownDeliveryError must unwrap to its cause")
	}
	var out *UnknownDeliveryError
	if !errors.As(fmt.Errorf("send failed: %w", err), &out) || out.ID != "3EB0TEST" {
		t.Error("UnknownDeliveryError must survive wrapping for errors.As at the CLI layer")
	}
}

func TestValidateSendResponse(t *testing.T) {
	if _, err := validateSendResponse(whatsmeow.SendResponse{}); err == nil {
		t.Fatal("missing message_id must fail, including for audio caption follow-ups")
	}
	response := whatsmeow.SendResponse{ID: "SENT123"}
	got, err := validateSendResponse(response)
	if err != nil || got.ID != response.ID {
		t.Fatalf("valid response rejected: %+v, %v", got, err)
	}
}
