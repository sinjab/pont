package whatsapp

import (
	"errors"
	"strings"
	"testing"

	"go.mau.fi/whatsmeow"
)

func TestMediaCaptionCompletion(t *testing.T) {
	cause := errors.New("caption send aborted: websocket disconnected")
	for _, tc := range []struct {
		name string
		resp whatsmeow.SendResponse
		err  error
	}{
		{"sent", whatsmeow.SendResponse{ID: "CAPTION123"}, nil},
		{"failed", whatsmeow.SendResponse{}, cause},
		{"missing id", whatsmeow.SendResponse{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			media := &SendMessageResult{Success: true, MessageID: "MEDIA123"}
			result, err := completeMediaCaption(media, "1234@s.whatsapp.net", tc.resp, tc.err)
			if tc.name == "sent" {
				if err != nil || !result.Success || result.CaptionMessageID != "CAPTION123" {
					t.Fatalf("caption success: %+v, %v", result, err)
				}
				return
			}
			if err == nil || result.Success || result.CaptionMessageID != "" {
				t.Fatalf("caption failure reported success: %+v, %v", result, err)
			}
			if result.MessageID != "MEDIA123" || !strings.Contains(err.Error(), "MEDIA123") {
				t.Errorf("already-sent media ID lost: %+v, %v", result, err)
			}
			if tc.err != nil && !errors.Is(err, cause) {
				t.Errorf("caption error lost: %v", err)
			}
		})
	}
}
