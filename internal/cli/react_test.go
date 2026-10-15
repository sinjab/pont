package cli

import "testing"

func TestNormalizeReactEmoji(t *testing.T) {
	cases := map[string]string{
		"thumbsup":     "\U0001F44D",
		"THUMBSUP":     "\U0001F44D",
		"heart":        "\u2764\uFE0F",
		" heart ":      "\u2764\uFE0F",
		"\u2764":       "\u2764\uFE0F", // bare heart gains VS16
		"\u2764\uFE0F": "\u2764\uFE0F", // already correct stays
		"\U0001F525":   "\U0001F525",   // passthrough emoji
		"":             "",
	}
	for in, want := range cases {
		if got := normalizeReactEmoji(in); got != want {
			t.Errorf("normalizeReactEmoji(%q) = %q, want %q", in, got, want)
		}
	}
}
