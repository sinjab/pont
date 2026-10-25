package whatsapp

import (
	"database/sql"
	"strings"
	"time"

	waHistorySync "go.mau.fi/whatsmeow/proto/waHistorySync"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// handleMessage processes real-time incoming messages and persists them.
func (c *Client) handleMessage(msg *events.Message) {
	chatJID := msg.Info.Chat.String()
	sender := msg.Info.Sender.User
	senderJID := msg.Info.Sender.String() // full JID (user@server): LID vs phone disambiguation (migration v3)
	content := extractTextContent(msg.Message)
	mediaType, filename, url, directPath, mediaKeyTimestamp, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message) // + true media reference (migration v5)
	replyToID, replyToText := extractReplyInfo(msg.Message)                                                                                   // quoted-reply target (migration v4)

	if content == "" && mediaType == "" {
		return
	}

	// Resolve sender name
	senderName := c.resolveSenderName(sender, msg.Info.Sender, msg.Info.PushName)

	// Ensure we have a per-sender chat entry
	if sender != "" {
		indiv := types.JID{User: sender, Server: "s.whatsapp.net"}
		var existing sql.NullString
		_ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", indiv.String()).Scan(&existing)
		if !existing.Valid {
			resolved := c.resolvePreferredName(indiv.String())
			_, _ = c.Store.Messages.Exec("INSERT INTO chats (jid, name) VALUES (?, ?)", indiv.String(), resolved)
		} else if existing.String == "" {
			resolved := c.resolvePreferredName(indiv.String())
			if resolved != "" {
				_, _ = c.Store.Messages.Exec("UPDATE chats SET name = ? WHERE jid = ?", resolved, indiv.String())
			}
		}
	}

	name := c.getChatName(msg.Info.Chat.String(), chatJID, nil, sender)
	if _, err := c.Store.Messages.Exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
			last_message_time = excluded.last_message_time`, chatJID, name, msg.Info.Timestamp); err != nil {
		c.Logger.Warn("failed to upsert chat", "jid", chatJID, "err", err)
	}

	if _, err := c.Store.Messages.Exec(`INSERT INTO messages
		(id, chat_jid, sender, sender_jid, sender_name, content, timestamp, is_from_me, media_type, filename, url, direct_path, media_key_timestamp, media_key, file_sha256, file_enc_sha256, file_length, reply_to_id, reply_to_text)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id, chat_jid) DO UPDATE SET
			sender = excluded.sender,
			sender_jid = CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE messages.sender_jid END,
			sender_name = CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE messages.sender_name END,
			content = CASE WHEN excluded.content != '' THEN excluded.content ELSE messages.content END,
			timestamp = excluded.timestamp,
			is_from_me = excluded.is_from_me,
			media_type = CASE WHEN excluded.media_type IS NOT NULL AND excluded.media_type != '' THEN excluded.media_type ELSE messages.media_type END,
			filename = CASE WHEN excluded.filename IS NOT NULL AND excluded.filename != '' THEN excluded.filename ELSE messages.filename END,
			url = CASE WHEN excluded.url != '' THEN excluded.url ELSE messages.url END,
			direct_path = CASE WHEN excluded.direct_path != '' THEN excluded.direct_path ELSE messages.direct_path END,
			media_key_timestamp = CASE WHEN excluded.media_key_timestamp != 0 THEN excluded.media_key_timestamp ELSE messages.media_key_timestamp END,
			media_key = CASE WHEN excluded.media_key IS NOT NULL THEN excluded.media_key ELSE messages.media_key END,
			file_sha256 = CASE WHEN excluded.file_sha256 IS NOT NULL THEN excluded.file_sha256 ELSE messages.file_sha256 END,
			file_enc_sha256 = CASE WHEN excluded.file_enc_sha256 IS NOT NULL THEN excluded.file_enc_sha256 ELSE messages.file_enc_sha256 END,
			file_length = CASE WHEN excluded.file_length != 0 THEN excluded.file_length ELSE messages.file_length END,
			reply_to_id = CASE WHEN excluded.reply_to_id != '' THEN excluded.reply_to_id ELSE messages.reply_to_id END,
			reply_to_text = CASE WHEN excluded.reply_to_text != '' THEN excluded.reply_to_text ELSE messages.reply_to_text END`,
		msg.Info.ID, chatJID, sender, senderJID, senderName, content, msg.Info.Timestamp, msg.Info.IsFromMe, mediaType, filename, url, directPath, mediaKeyTimestamp, mediaKey, fileSHA256, fileEncSHA256, fileLength, replyToID, replyToText,
	); err != nil {
		c.Logger.Warn("failed to store message", "id", msg.Info.ID, "chat_jid", chatJID, "err", err)
	}
}

// handleHistorySync persists conversations and messages received during a history sync.
func (c *Client) handleHistorySync(hs *events.HistorySync) HistorySyncResult {
	if hs == nil || hs.Data == nil || hs.Data.Conversations == nil {
		return HistorySyncResult{}
	}

	synced := 0
	moreAvailable := false
	isOnDemand := hs.Data.GetSyncType() == waHistorySync.HistorySync_ON_DEMAND
	conversationCount := len(hs.Data.Conversations)

	for _, conv := range hs.Data.Conversations {
		if conv == nil || conv.ID == nil {
			continue
		}

		responseChatJID := *conv.ID
		chatJID := c.backfillStorageChatJID(responseChatJID, isOnDemand, conversationCount)
		jid, err := types.ParseJID(chatJID)
		if err != nil {
			c.Logger.Warn("history sync: bad JID", "jid", chatJID, "response_jid", responseChatJID, "err", err)
			continue
		}

		name := c.getChatName(jid.String(), chatJID, conv, "")
		endType := conv.GetEndOfHistoryTransferType()
		if endType == waHistorySync.Conversation_COMPLETE_BUT_MORE_MESSAGES_REMAIN_ON_PRIMARY ||
			endType == waHistorySync.Conversation_COMPLETE_ON_DEMAND_SYNC_BUT_MORE_MSG_REMAIN_ON_PRIMARY {
			moreAvailable = true
		}

		if len(conv.Messages) > 0 && conv.Messages[0] != nil && conv.Messages[0].Message != nil {
			ts := conv.Messages[0].Message.GetMessageTimestamp()
			if ts != 0 {
				t := time.Unix(int64(ts), 0)
				if _, err := c.Store.Messages.Exec(`INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)
					ON CONFLICT(jid) DO UPDATE SET
						name = CASE WHEN excluded.name != '' THEN excluded.name ELSE chats.name END,
						last_message_time = excluded.last_message_time`, chatJID, name, t); err != nil {
					c.Logger.Warn("history sync: failed to upsert chat", "jid", chatJID, "err", err)
				}
			}
		}

		for _, m := range conv.Messages {
			if m == nil || m.Message == nil {
				continue
			}

			var text string
			if m.Message.Message != nil {
				text = extractTextContent(m.Message.Message)
			}

			mt, fn, u, dp, mkt, mk, sha, enc, fl := "", "", "", "", int64(0), ([]byte)(nil), ([]byte)(nil), ([]byte)(nil), uint64(0)
			if m.Message.Message != nil {
				mt, fn, u, dp, mkt, mk, sha, enc, fl = extractMediaInfo(m.Message.Message)
			}

			rID, rText := "", "" // quoted-reply target (migration v4)
			if m.Message.Message != nil {
				rID, rText = extractReplyInfo(m.Message.Message)
			}

			if text == "" && mt == "" {
				continue
			}

			fromMe := false
			snd := jid.User
			sndFull := jid.String() // full JID before normalization (migration v3)
			if m.Message.Key != nil {
				if m.Message.Key.FromMe != nil {
					fromMe = *m.Message.Key.FromMe
				}
				if !fromMe && m.Message.Key.Participant != nil && *m.Message.Key.Participant != "" {
					snd = *m.Message.Key.Participant
					sndFull = *m.Message.Key.Participant
				}
				if fromMe && c.WA != nil && c.WA.Store != nil && c.WA.Store.ID != nil {
					snd = c.WA.Store.ID.User
					sndFull = c.WA.Store.ID.String()
				}
			}

			if strings.Contains(snd, "@") {
				if pj, err := types.ParseJID(snd); err == nil {
					snd = pj.User
				} else {
					if i := strings.Index(snd, "@"); i > 0 {
						snd = snd[:i]
					}
				}
			}
			if sndFull == "" {
				sndFull = snd
			}

			// Upsert a per-sender chat entry
			if !fromMe && snd != "" {
				indiv := types.JID{User: snd, Server: "s.whatsapp.net"}
				var existing sql.NullString
				_ = c.Store.Messages.QueryRow("SELECT name FROM chats WHERE jid = ?", indiv.String()).Scan(&existing)
				if !existing.Valid {
					resolved := c.resolvePreferredName(indiv.String())
					_, _ = c.Store.Messages.Exec("INSERT INTO chats (jid, name) VALUES (?, ?)", indiv.String(), resolved)
				} else if existing.String == "" {
					resolved := c.resolvePreferredName(indiv.String())
					if resolved != "" {
						_, _ = c.Store.Messages.Exec("UPDATE chats SET name = ? WHERE jid = ?", resolved, indiv.String())
					}
				}
			}

			id := ""
			if m.Message.Key != nil && m.Message.Key.ID != nil {
				id = *m.Message.Key.ID
			}

			ts := m.Message.GetMessageTimestamp()
			if ts == 0 {
				continue
			}
			t := time.Unix(int64(ts), 0)

			// Resolve sender name for history sync messages
			senderName := c.Store.ResolveSenderName(snd)
			if senderName == "" {
				// Try to resolve from phone-based JID
				phoneJID := types.JID{User: snd, Server: "s.whatsapp.net"}
				senderName = c.resolvePreferredName(phoneJID.String())
			}

			if _, err := c.Store.Messages.Exec(`INSERT INTO messages
				(id, chat_jid, sender, sender_jid, sender_name, content, timestamp, is_from_me, media_type, filename, url, direct_path, media_key_timestamp, media_key, file_sha256, file_enc_sha256, file_length, reply_to_id, reply_to_text)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id, chat_jid) DO UPDATE SET
					sender = excluded.sender,
					sender_jid = CASE WHEN excluded.sender_jid != '' THEN excluded.sender_jid ELSE messages.sender_jid END,
					sender_name = CASE WHEN excluded.sender_name != '' THEN excluded.sender_name ELSE messages.sender_name END,
					content = CASE WHEN excluded.content != '' THEN excluded.content ELSE messages.content END,
					timestamp = excluded.timestamp,
					is_from_me = excluded.is_from_me,
					media_type = CASE WHEN excluded.media_type IS NOT NULL AND excluded.media_type != '' THEN excluded.media_type ELSE messages.media_type END,
					filename = CASE WHEN excluded.filename IS NOT NULL AND excluded.filename != '' THEN excluded.filename ELSE messages.filename END,
					url = CASE WHEN excluded.url != '' THEN excluded.url ELSE messages.url END,
					direct_path = CASE WHEN excluded.direct_path != '' THEN excluded.direct_path ELSE messages.direct_path END,
					media_key_timestamp = CASE WHEN excluded.media_key_timestamp != 0 THEN excluded.media_key_timestamp ELSE messages.media_key_timestamp END,
					media_key = CASE WHEN excluded.media_key IS NOT NULL THEN excluded.media_key ELSE messages.media_key END,
					file_sha256 = CASE WHEN excluded.file_sha256 IS NOT NULL THEN excluded.file_sha256 ELSE messages.file_sha256 END,
					file_enc_sha256 = CASE WHEN excluded.file_enc_sha256 IS NOT NULL THEN excluded.file_enc_sha256 ELSE messages.file_enc_sha256 END,
					file_length = CASE WHEN excluded.file_length != 0 THEN excluded.file_length ELSE messages.file_length END,
				reply_to_id = CASE WHEN excluded.reply_to_id != '' THEN excluded.reply_to_id ELSE messages.reply_to_id END,
				reply_to_text = CASE WHEN excluded.reply_to_text != '' THEN excluded.reply_to_text ELSE messages.reply_to_text END`,
				id, chatJID, snd, sndFull, senderName, text, t, fromMe, mt, fn, u, dp, mkt, mk, sha, enc, fl, rID, rText); err != nil {
				c.Logger.Warn("history sync: failed to store message", "id", id, "chat_jid", chatJID, "err", err)
				continue
			}
			synced++
		}
	}

	c.Logger.Info("history sync persisted messages", "count", synced)
	return HistorySyncResult{MessagesSynced: synced, MoreAvailable: moreAvailable}
}

// backfillChatNames finds chats without a proper name and updates them.
func (c *Client) backfillChatNames() {
	if c.Store == nil || c.Store.Messages == nil {
		return
	}

	rows, err := c.Store.Messages.Query(`SELECT jid, COALESCE(name, '') FROM chats`)
	if err != nil {
		c.Logger.Warn("backfill: query chats failed", "err", err)
		return
	}
	defer func() { _ = rows.Close() }()

	type row struct {
		jid  string
		name string
	}
	var toUpdate []row

	for rows.Next() {
		var jidStr, name string
		if err := rows.Scan(&jidStr, &name); err != nil {
			c.Logger.Warn("backfill: scan failed", "err", err)
			continue
		}

		parsed, err := types.ParseJID(jidStr)
		if err != nil {
			continue
		}

		if parsed.Server == "g.us" {
			if name == "" || name == parsed.User || name == "Group "+parsed.User {
				toUpdate = append(toUpdate, row{jid: jidStr, name: name})
			}
			continue
		}

		phone := parsed.User
		if name == "" || name == phone || strings.HasSuffix(name, "@s.whatsapp.net") {
			toUpdate = append(toUpdate, row{jid: jidStr, name: name})
		}
	}

	updated := 0
	for _, r := range toUpdate {
		resolved := c.resolvePreferredName(r.jid)
		if resolved == "" || resolved == r.name {
			continue
		}

		if _, err := c.Store.Messages.Exec(`UPDATE chats SET name = ? WHERE jid = ?`, resolved, r.jid); err != nil {
			c.Logger.Warn("backfill: update failed", "jid", r.jid, "err", err)
			continue
		}
		updated++
	}

	if updated > 0 {
		c.Logger.Info("backfill: updated chat names", "count", updated)
	}
}
