package whatsapp

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	waE2E "go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/types"
)

// parseJID parses a JID string into a types.JID.
func parseJID(jid string) (types.JID, error) {
	return types.ParseJID(jid)
}

// extractTextContent extracts text content from a WhatsApp message.
func extractTextContent(m *waE2E.Message) string {
	if m == nil {
		return ""
	}

	if t := m.GetConversation(); t != "" {
		return t
	}

	if et := m.GetExtendedTextMessage(); et != nil {
		return et.GetText()
	}

	if loc := m.GetLocationMessage(); loc != nil {
		return fmt.Sprintf("📍 Location: %.6f, %.6f", loc.GetDegreesLatitude(), loc.GetDegreesLongitude())
	}

	if contact := m.GetContactMessage(); contact != nil {
		name := contact.GetDisplayName()
		if name == "" {
			name = "Contact"
		}
		return fmt.Sprintf("👤 %s", name)
	}

	if sticker := m.GetStickerMessage(); sticker != nil {
		return "🎭 Sticker"
	}

	if liveLoc := m.GetLiveLocationMessage(); liveLoc != nil {
		return fmt.Sprintf("📍 Live Location: %.6f, %.6f", liveLoc.GetDegreesLatitude(), liveLoc.GetDegreesLongitude())
	}

	if poll := m.GetPollCreationMessage(); poll != nil {
		return fmt.Sprintf("📊 Poll: %s", poll.GetName())
	}

	if reaction := m.GetReactionMessage(); reaction != nil {
		return fmt.Sprintf("😊 Reaction: %s", reaction.GetText())
	}

	if m.GetProtocolMessage() != nil {
		return "🔧 System Message"
	}

	return ""
}

// extractReplyInfo extracts the quoted-reply target from a WhatsApp
// message: the quoted message's id (ContextInfo.StanzaId) and a text
// snapshot of the quoted content. Replies can ride any concrete
// wrapper (extended text, images, documents, voice notes...), so walk
// the same type set as the text/media extractors. The snapshot keeps
// replies self-describing even when the quoted original is absent
// from the store (phone-sent, another chat, pre-adoption). Empty
// returns mean "not a reply".
func extractReplyInfo(m *waE2E.Message) (replyToID, replyToText string) {
	if m == nil {
		return "", ""
	}

	var ctx *waE2E.ContextInfo
	switch {
	case m.GetExtendedTextMessage() != nil:
		ctx = m.GetExtendedTextMessage().GetContextInfo()
	case m.GetImageMessage() != nil:
		ctx = m.GetImageMessage().GetContextInfo()
	case m.GetVideoMessage() != nil:
		ctx = m.GetVideoMessage().GetContextInfo()
	case m.GetAudioMessage() != nil:
		ctx = m.GetAudioMessage().GetContextInfo()
	case m.GetDocumentMessage() != nil:
		ctx = m.GetDocumentMessage().GetContextInfo()
	case m.GetStickerMessage() != nil:
		ctx = m.GetStickerMessage().GetContextInfo()
	case m.GetLocationMessage() != nil:
		ctx = m.GetLocationMessage().GetContextInfo()
	case m.GetContactMessage() != nil:
		ctx = m.GetContactMessage().GetContextInfo()
	case m.GetLiveLocationMessage() != nil:
		ctx = m.GetLiveLocationMessage().GetContextInfo()
	case m.GetPollCreationMessage() != nil:
		ctx = m.GetPollCreationMessage().GetContextInfo()
	}
	if ctx == nil {
		return "", ""
	}

	replyToID = ctx.GetStanzaID()
	if qm := ctx.GetQuotedMessage(); qm != nil {
		replyToText = extractTextContent(qm)
		// Cap the snapshot at a sane preview length (WhatsApp itself
		// previews ~one screen); rune-safe truncation.
		if r := []rune(replyToText); len(r) > 480 {
			replyToText = string(r[:480]) + "…"
		}
	}
	return replyToID, replyToText
}

// extractMediaInfo extracts media information from a WhatsApp message.
// directPath + mediaKeyTimestamp are the protocol's OWN media reference
// fields (migration v5): forwarding must re-send the TRUE directPath —
// synthesizing one from the URL strips its ?oh= auth token and produces
// an invalid reference that directPath-first clients (Android) cannot
// download (proven Oct 25 2026).
func extractMediaInfo(m *waE2E.Message) (mediaType, filename, url, directPath string, mediaKeyTimestamp int64, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
	if m == nil {
		return "", "", "", "", 0, nil, nil, nil, 0
	}

	if img := m.GetImageMessage(); img != nil {
		return "image", fmt.Sprintf("image_%s.jpg", time.Now().Format("20060102_150405")), img.GetURL(), img.GetDirectPath(), img.GetMediaKeyTimestamp(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}

	if vid := m.GetVideoMessage(); vid != nil {
		return "video", fmt.Sprintf("video_%s.mp4", time.Now().Format("20060102_150405")), vid.GetURL(), vid.GetDirectPath(), vid.GetMediaKeyTimestamp(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}

	if aud := m.GetAudioMessage(); aud != nil {
		return "audio", fmt.Sprintf("audio_%s.ogg", time.Now().Format("20060102_150405")), aud.GetURL(), aud.GetDirectPath(), aud.GetMediaKeyTimestamp(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}

	if doc := m.GetDocumentMessage(); doc != nil {
		name := doc.GetFileName()
		if name == "" {
			name = fmt.Sprintf("document_%s", time.Now().Format("20060102_150405"))
		}
		return "document", name, doc.GetURL(), doc.GetDirectPath(), doc.GetMediaKeyTimestamp(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	if sticker := m.GetStickerMessage(); sticker != nil {
		return "sticker", fmt.Sprintf("sticker_%s.webp", time.Now().Format("20060102_150405")), sticker.GetURL(), sticker.GetDirectPath(), sticker.GetMediaKeyTimestamp(), sticker.GetMediaKey(), sticker.GetFileSHA256(), sticker.GetFileEncSHA256(), sticker.GetFileLength()
	}

	return "", "", "", "", 0, nil, nil, nil, 0
}

// classifyToWA converts media type string to WhatsApp MediaType.
func classifyToWA(t string) whatsmeow.MediaType {
	switch t {
	case "image":
		return whatsmeow.MediaImage
	case "video":
		return whatsmeow.MediaVideo
	case "audio":
		return whatsmeow.MediaAudio
	case "document":
		return whatsmeow.MediaDocument
	default:
		return whatsmeow.MediaDocument
	}
}

// extractDirectPathFromURL extracts the direct path from a WhatsApp media URL.
func extractDirectPathFromURL(url string) string {
	parts := strings.SplitN(url, ".net/", 2)
	if len(parts) < 2 {
		return url
	}
	p := strings.SplitN(parts[1], "?", 2)[0]
	return "/" + p
}

// uniqueMediaFilename de-collides CLI-GENERATED media filenames
// (image_/video_/audio_/document_<sync-clock-second>) by suffixing the
// message ID: one sync run generates the SAME name for every media row,
// so batch downloads overwrite each other (proven Sep 29 2026 — five VNs,
// one surviving file). Real document filenames pass through untouched.
func uniqueMediaFilename(filename, messageID string) string {
	base := filepath.Base(filename)
	for _, prefix := range []string{"image_", "video_", "audio_", "document_"} {
		if strings.HasPrefix(base, prefix) && !strings.Contains(base, messageID[:8]) {
			ext := filepath.Ext(base)
			stem := strings.TrimSuffix(base, ext)
			return stem + "_" + messageID[:8] + ext
		}
	}
	return filename
}

// downloadable implements whatsmeow.DownloadableMessage interface.
type downloadable struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *downloadable) GetDirectPath() string             { return d.DirectPath }
func (d *downloadable) GetURL() string                    { return d.URL }
func (d *downloadable) GetMediaKey() []byte               { return d.MediaKey }
func (d *downloadable) GetFileLength() uint64             { return d.FileLength }
func (d *downloadable) GetFileSHA256() []byte             { return d.FileSHA256 }
func (d *downloadable) GetFileEncSHA256() []byte          { return d.FileEncSHA256 }
func (d *downloadable) GetMediaType() whatsmeow.MediaType { return d.MediaType }
