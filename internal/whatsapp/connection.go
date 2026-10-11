package whatsapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types/events"
)

const syncCompletionSettleDelay = 5 * time.Second

// registerHandlers registers event handlers for WhatsApp events.
func (c *Client) registerHandlers() {
	c.WA.AddEventHandler(func(evt interface{}) {
		switch v := evt.(type) {
		case *events.Message:
			c.handleMessage(v)
		case *events.HistorySync:
			result := c.handleHistorySync(v)
			if v.Data != nil && v.Data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND {
				c.completeBackfillRequest(result)
			}
			// Check if sync is complete (progress == 100)
			if v.Data != nil && v.Data.Progress != nil && *v.Data.Progress >= 100 {
				c.Logger.Info("history sync complete")
				c.backfillChatNames()
				c.signalSyncCompleteAfterSettleDelay()
			}
		case *events.OfflineSyncCompleted:
			c.backfillChatNames()
			count, _ := c.Store.CountMessages()
			if count > 0 {
				c.signalSyncCompleteAfterSettleDelay()
			}
		case *events.Connected:
			c.Logger.Info("connected to WhatsApp")
			select {
			case c.postPairConnected <- struct{}{}:
			default:
			}
		case *events.PairSuccess:
			c.paired.Store(true)
			c.Logger.Info("pairing confirmed", "user", v.ID.User, "device", v.ID.Device)
			c.notifyPairResult(nil)
		case *events.PairError:
			c.Logger.Error("pairing failed", "error", v.Error)
			c.notifyPairResult(fmt.Errorf("pairing failed locally: %w", v.Error))
		case *events.Disconnected:
			// Fatal only while a pairing is still pending — the code-entry
			// window is exactly the socket-EOF failure mode of issue #1.
			// Post-pair reconnects also emit Disconnected and must not
			// poison the wait; nobody listening drops the signal silently.
			if !c.paired.Load() {
				c.notifyPairResult(errors.New("connection closed by server before pairing completed"))
			}
		case *events.LoggedOut:
			c.Logger.Warn("logged out of WhatsApp")
		}
	})
}

// notifyPairResult delivers a pairing outcome without ever blocking the
// event handler; a second outcome (e.g. Disconnected after PairError) is
// dropped — the first result wins.
func (c *Client) notifyPairResult(err error) {
	select {
	case c.pairResult <- err:
	default:
	}
}

// WaitForPairCompletion blocks until a pairing attempt (login --phone) is
// actually confirmed by the server: PairSuccess followed by the post-pair
// websocket reconnection (events.Connected). Without this gate the CLI would
// claim success the moment the pairing code is displayed (fork issue #1).
func (c *Client) WaitForPairCompletion(ctx context.Context) error {
	select {
	case err := <-c.pairResult:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return fmt.Errorf("interrupted before pairing completed: %w", ctx.Err())
	}

	// whatsmeow: PairSuccess is generally followed by a websocket
	// reconnection — wait for the post-pair Connected before declaring
	// success. The signal is sticky, so an already-reconnected client
	// passes straight through.
	select {
	case <-c.postPairConnected:
	case <-ctx.Done():
		return fmt.Errorf("interrupted after pairing, during reconnect: %w", ctx.Err())
	}
	return nil
}

func (c *Client) signalSyncCompleteAfterSettleDelay() {
	c.syncCompleteMu.Lock()
	defer c.syncCompleteMu.Unlock()

	if c.syncCompleteTimer != nil {
		c.syncCompleteTimer.Stop()
	}

	c.syncCompleteTimer = time.AfterFunc(syncCompletionSettleDelay, func() {
		if !c.WA.IsConnected() {
			return
		}

		select {
		case c.SyncComplete <- struct{}{}:
		default:
		}
	})
}

// ConnectWithQR connects to WhatsApp, displaying a QR code if needed.
func (c *Client) ConnectWithQR(ctx context.Context) error {
	if c.WA.Store.ID == nil {
		qrChan, err := c.WA.GetQRChannel(ctx)
		if err != nil {
			return fmt.Errorf("failed to get QR channel: %w", err)
		}
		if err := c.WA.Connect(); err != nil {
			return err
		}

		for evt := range qrChan {
			if evt.Event == "code" {
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stderr)
			} else if evt.Event == "success" {
				break
			}
		}

		return nil
	}

	return c.WA.Connect()
}

// ConnectWithPairCode links a new device using a phone-number pairing code
// instead of a QR scan. The phone must enter the returned code under
// Settings -> Linked Devices -> Link with phone number instead.
func (c *Client) ConnectWithPairCode(ctx context.Context, phone string) (string, error) {
	if c.WA.Store.ID != nil {
		return "", fmt.Errorf("already authenticated")
	}
	if err := c.WA.Connect(); err != nil {
		return "", err
	}
	// whatsmeow docs: wait for the first QR event (or ~1s) before PairPhone.
	time.Sleep(1500 * time.Millisecond)
	return c.WA.PairPhone(ctx, phone, true, whatsmeow.PairClientMacOS, "Chrome (macOS)")
}

// Connect connects to WhatsApp without QR (requires existing session).
func (c *Client) Connect() error {
	return c.WA.Connect()
}
