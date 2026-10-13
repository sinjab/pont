package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// idempotentReconnectWindow is how long a one-shot send waits for the
// socket to come back after a mid-send drop before declaring the delivery
// state unknown. whatsmeow's internal retryFrame only waits 5s, which a
// flapping socket easily exceeds (proven Oct 13 2026: a send reported
// "websocket disconnected before message send returned response" while the
// frame had already been delivered — the caller's blind retry duplicated
// the message).
const idempotentReconnectWindow = 30 * time.Second

// UnknownDeliveryError means a send frame was handed to the socket but no
// server acknowledgement ever arrived (websocket dropped mid-send). The
// message may or may not have been delivered. Retrying with a NEW id can
// duplicate the message on the recipient's side; the id is included so a
// delivered message can be revoked with `pont delete <id> --chat <jid>`.
type UnknownDeliveryError struct {
	ID      string
	ChatJID string
	Err     error
}

func (e *UnknownDeliveryError) Error() string {
	return fmt.Sprintf(
		"send state unknown (id=%s, chat=%s): the message may have been delivered — do NOT resend with new content blindly; verify the chat first, and revoke instead with: pont delete %s --chat %s (cause: %v)",
		e.ID, e.ChatJID, e.ID, e.ChatJID, e.Err,
	)
}

func (e *UnknownDeliveryError) Unwrap() error { return e.Err }

// sendMessageIdempotent sends msg to jid under a caller-generated message
// ID and makes the delivery semantics safe across websocket drops:
//
//  1. First attempt uses the generated id. If the socket drops while
//     waiting for the server ack (whatsmeow DisconnectedError — the frame
//     may already be on the wire), the one-shot CLI has no auto-reconnect,
//     so we bring the socket back ourselves and wait up to
//     idempotentReconnectWindow.
//  2. The single retry re-sends with the SAME id. WhatsApp's retry
//     semantics dedupe by (sender, id): a recipient that already has the
//     id ignores the repeat, so this cannot create a visible duplicate,
//     while a first attempt that truly never left recovers normally.
//  3. If the same-id retry also fails, delivery is genuinely unknown and
//     the caller gets *UnknownDeliveryError carrying the id.
func (c *Client) sendMessageIdempotent(ctx context.Context, jid types.JID, msg *waE2E.Message) (whatsmeow.SendResponse, error) {
	id := c.WA.GenerateMessageID()

	resp, err := c.WA.SendMessage(ctx, jid, msg, whatsmeow.SendRequestExtra{ID: id})
	if err == nil {
		return resp, nil
	}

	var discErr *whatsmeow.DisconnectedError
	if !errors.As(err, &discErr) {
		// Not a mid-send drop: the failure is real (never reached the wire
		// or the server rejected it) — plain error, safe to retry normally.
		return resp, err
	}

	c.Logger.Warn(
		"send interrupted by websocket drop; reconnecting for same-id retry",
		"message_id", id, "chat", jid.String(), "error", err.Error(),
	)

	// One-shot commands run with EnableAutoReconnect=false, so the socket
	// does not come back on its own.
	if !c.WA.IsConnected() {
		if connErr := c.WA.Connect(); connErr != nil {
			return resp, &UnknownDeliveryError{ID: id, ChatJID: jid.String(), Err: connErr}
		}
	}
	if !c.WA.WaitForConnection(idempotentReconnectWindow) {
		return resp, &UnknownDeliveryError{ID: id, ChatJID: jid.String(), Err: err}
	}

	resp2, err2 := c.WA.SendMessage(ctx, jid, msg, whatsmeow.SendRequestExtra{ID: id})
	if err2 == nil {
		return resp2, nil
	}
	return resp2, &UnknownDeliveryError{ID: id, ChatJID: jid.String(), Err: err2}
}
