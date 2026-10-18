package cli

import (
	"fmt"
	"strings"

	"github.com/sinjab/pont/internal/store"
	"github.com/sinjab/pont/internal/whatsapp"
)

// messageSender keeps receipt-producing commands testable without a network
// connection or a real session store.
type messageSender interface {
	SendText(string, string, string) (*whatsapp.SendMessageResult, error)
	SendMedia(string, string, string, string) (*whatsapp.SendMessageResult, error)
	ForwardMessage(string, string, string) (*whatsapp.SendMessageResult, error)
	SendReaction(string, string, string, bool) (*whatsapp.SendMessageResult, error)
	RevokeMessage(string, string) (*whatsapp.SendMessageResult, error)
}

var withMessageSender = func(fn func(messageSender) error) error {
	return WithConnection(func(_ *store.DB, client *whatsapp.Client) error {
		return fn(client)
	})
}

// validateSendResult must run before printing any success receipt. A nil error
// alone does not establish that the requested action completed.
func validateSendResult(result *whatsapp.SendMessageResult, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("no send receipt returned")
	}
	if !result.Success {
		if result.Message != "" {
			return fmt.Errorf("%s", result.Message)
		}
		return fmt.Errorf("action did not complete")
	}
	if strings.TrimSpace(result.MessageID) == "" {
		return fmt.Errorf("no message_id returned")
	}
	return nil
}
