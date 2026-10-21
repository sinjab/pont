package whatsapp

import (
	"encoding/json"
	"time"

	"go.mau.fi/whatsmeow/types/events"
)

// MessageDescription is a serializable summary of a live message event,
// for consumers that watch the event stream without touching the store.
type MessageDescription struct {
	ID         string `json:"id"`
	ChatJID    string `json:"chat_jid"`
	Sender     string `json:"sender"`
	SenderAlt  string `json:"sender_alt,omitempty"`
	SenderName string `json:"sender_name,omitempty"`
	PushName   string `json:"push_name,omitempty"`
	// IsFromMe / MediaType are the canonical field names, aligned with
	// the store models (contract normalization, Oct 11 2026 consult).
	IsFromMe  bool      `json:"is_from_me"`
	Timestamp time.Time `json:"timestamp"`
	MediaType string    `json:"media_type,omitempty"`
	Content   string    `json:"content,omitempty"`
	// Quoted-reply target + snapshot, when the message is a reply
	// (mirror of the store fields; migration v4).
	ReplyToID   string `json:"reply_to_id,omitempty"`
	ReplyToText string `json:"reply_to_text,omitempty"`
	// DeviceSent is true when this is a copy of a message sent from
	// another one of the user's own linked devices (phone, desktop,
	// another CLI instance). On a dedicated watcher device these
	// copies are the reliable outbound oracle.
	DeviceSent bool `json:"device_sent,omitempty"`
}

// MarshalJSON derives the legacy from_me / type aliases from the
// canonical IsFromMe / MediaType fields, so alias and canonical can
// never disagree wherever the struct is built. The aliases remain
// load-bearing for downstream consumers (autoreply.py reads them)
// until v3.0 removes them — delete this method then.
func (d MessageDescription) MarshalJSON() ([]byte, error) {
	type canonical MessageDescription
	return json.Marshal(struct {
		canonical
		FromMe bool   `json:"from_me"`
		Type   string `json:"type,omitempty"`
	}{canonical(d), d.IsFromMe, d.MediaType})
}

// DescribeMessage summarizes a live events.Message without persisting anything.
func (c *Client) DescribeMessage(msg *events.Message) MessageDescription {
	content := extractTextContent(msg.Message)
	mediaType, _, _, _, _, _, _ := extractMediaInfo(msg.Message)
	replyToID, replyToText := extractReplyInfo(msg.Message)

	d := MessageDescription{
		ID:          string(msg.Info.ID),
		ChatJID:     msg.Info.Chat.String(),
		Sender:      msg.Info.Sender.String(),
		PushName:    msg.Info.PushName,
		IsFromMe:    msg.Info.IsFromMe,
		Timestamp:   msg.Info.Timestamp,
		MediaType:   mediaType,
		Content:     content,
		ReplyToID:   replyToID,
		ReplyToText: replyToText,
	}

	if !msg.Info.SenderAlt.IsEmpty() {
		d.SenderAlt = msg.Info.SenderAlt.String()
	}
	if msg.Info.DeviceSentMeta != nil {
		d.DeviceSent = true
	}
	if name := c.resolveSenderName(msg.Info.Sender.User, msg.Info.Sender, msg.Info.PushName); name != "" {
		d.SenderName = name
	}

	return d
}
