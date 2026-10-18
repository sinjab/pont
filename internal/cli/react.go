package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/sinjab/pont/internal/store"
)

var (
	reactChat   string
	reactRemove bool
)

var reactCmd = &cobra.Command{
	Use:   "react <msg-id> <emoji>",
	Short: "React to a message",
	Long: `Add or remove a reaction to a message.

Requires --chat to specify the chat JID.

Examples:
  whatsapp react ABC123 "thumbsup" --chat 1234567890@s.whatsapp.net
  whatsapp react ABC123 "" --chat 1234567890@s.whatsapp.net --remove`,
	Args: func(cmd *cobra.Command, args []string) error {
		remove, _ := cmd.Flags().GetBool("remove")
		if remove {
			if len(args) < 1 {
				return fmt.Errorf("requires at least 1 arg (msg-id)")
			}
			return nil
		}
		if len(args) < 2 {
			return fmt.Errorf("requires 2 args (msg-id and emoji)")
		}
		return nil
	},
	RunE: runReact,
}

func init() {
	rootCmd.AddCommand(reactCmd)
	reactCmd.Flags().StringVar(&reactChat, "chat", "", "Chat JID (required)")
	reactCmd.Flags().BoolVar(&reactRemove, "remove", false, "Remove reaction instead of adding")
	_ = reactCmd.MarkFlagRequired("chat")
}

// reactEmojiShortcodes maps common shortcode names to emoji characters.
// WhatsApp servers silently drop reactions whose text is not an actual
// emoji: a shortcode like "thumbsup" (this CLI's own documented example)
// or a bare U+2764 heart without the FE0F variation selector goes out
// with a clean receipt and journal event but never appears in the chat
// (proven Oct 9 + Oct 14 2026, yusra LID chat).
var reactEmojiShortcodes = map[string]string{
	"thumbsup":    "👍",
	"thumbup":     "👍",
	"heart":       "❤️",
	"love":        "❤️",
	"laugh":       "😂",
	"laughing":    "😂",
	"wow":         "😮",
	"surprised":   "😮",
	"sad":         "😢",
	"cry":         "😢",
	"tears":       "😢",
	"pray":        "🙏",
	"prayer":      "🙏",
	"foldedhands": "🙏",
	"ok":          "👌",
	"fire":        "🔥",
	"party":       "🎉",
	"tada":        "🎉",
	"clap":        "👏",
	"100":         "💯",
}

// normalizeReactEmoji resolves a shortcode to its emoji and appends the
// FE0F variation selector to a bare U+2764 heart (WhatsApp requires it).
func normalizeReactEmoji(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	if e, ok := reactEmojiShortcodes[lower]; ok {
		return e
	}
	if s == "❤" {
		return "❤️"
	}
	return s
}

func runReact(cmd *cobra.Command, args []string) error {
	messageID := args[0]
	emoji := ""
	if len(args) > 1 {
		emoji = normalizeReactEmoji(args[1])
	}

	return withMessageSender(func(client messageSender) error {
		result, err := client.SendReaction(reactChat, messageID, emoji, reactRemove)
		if err := validateSendResult(result, err); err != nil {
			return fmt.Errorf("react failed: %w", err)
		}

		return OutputResult(store.SendResult{
			MessageID: result.MessageID,
			ChatJID:   result.ChatJID,
			Timestamp: result.Timestamp,
		}, fmt.Sprintf("Reacted to message %s", result.MessageID))
	})
}
