package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/sinjab/pont/internal/whatsapp"
)

type fakeMessageSender struct {
	result *whatsapp.SendMessageResult
	err    error
	called string
}

func (f *fakeMessageSender) SendText(string, string, string) (*whatsapp.SendMessageResult, error) {
	f.called = "text"
	return f.result, f.err
}

func (f *fakeMessageSender) SendMedia(string, string, string, string) (*whatsapp.SendMessageResult, error) {
	f.called = "media"
	return f.result, f.err
}

func (f *fakeMessageSender) ForwardMessage(string, string, string) (*whatsapp.SendMessageResult, error) {
	f.called = "forward"
	return f.result, f.err
}

func (f *fakeMessageSender) SendReaction(string, string, string, bool) (*whatsapp.SendMessageResult, error) {
	f.called = "react"
	return f.result, f.err
}

func (f *fakeMessageSender) RevokeMessage(string, string) (*whatsapp.SendMessageResult, error) {
	f.called = "delete"
	return f.result, f.err
}

// Run the actual command/receipt path with a fake sender. No authentication or
// network is possible, and all configuration points at a temporary directory.
func runReceiptCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	resetFlagGlobals()
	sendFile, sendCaption, sendReplyTo = "", "", ""
	forwardFrom, reactChat, deleteChat = "", "", ""
	reactRemove = false
	var resetChanged func(*cobra.Command)
	resetChanged = func(cmd *cobra.Command) {
		cmd.Flags().VisitAll(func(flag *pflag.Flag) { flag.Changed = false })
		for _, child := range cmd.Commands() {
			resetChanged(child)
		}
	}
	resetChanged(rootCmd)
	t.Cleanup(resetFlagGlobals)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	oldOut := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = oldOut }()
	rootCmd.SetArgs(append([]string{"--data-dir", t.TempDir()}, args...))
	err = Execute()
	_ = w.Close()
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(out), err
}

func TestReceiptCommandsExitStatus(t *testing.T) {
	oldSender := withMessageSender
	t.Cleanup(func() { withMessageSender = oldSender })
	cause := errors.New("websocket disconnected before message send returned response")
	good := &whatsapp.SendMessageResult{Success: true, MessageID: "SENT123", ChatJID: fixtureChatNamed, Timestamp: "2026-10-19T00:00:00Z"}
	commands := []struct {
		name, method string
		args         []string
	}{
		{"send", "text", []string{"send", fixtureChatNamed, "hello"}},
		{"media", "media", []string{"send", fixtureChatNamed, "--file", "audio.ogg", "--caption", "hello"}},
		{"forward", "forward", []string{"forward", fixtureChatNamed, "ORIGINAL", "--from", fixtureChatNamed}},
		{"react", "react", []string{"react", "ORIGINAL", "thumbsup", "--chat", fixtureChatNamed}},
		{"unreact", "react", []string{"react", "ORIGINAL", "--remove", "--chat", fixtureChatNamed}},
		{"delete", "delete", []string{"delete", "ORIGINAL", "--chat", fixtureChatNamed}},
	}
	results := []struct {
		name   string
		result *whatsapp.SendMessageResult
		err    error
		want   int
	}{
		{"success", good, nil, 0},
		{"client error", nil, cause, 1},
		{"error with receipt", good, cause, 1},
		{"nil receipt", nil, nil, 1},
		{"failed result", &whatsapp.SendMessageResult{Message: "send aborted"}, nil, 1},
		{"failed result with id", &whatsapp.SendMessageResult{MessageID: "SENT123"}, nil, 1},
		{"missing id", &whatsapp.SendMessageResult{Success: true}, nil, 1},
		{"blank id", &whatsapp.SendMessageResult{Success: true, MessageID: " \t"}, nil, 1},
	}
	for _, command := range commands {
		for _, result := range results {
			for _, format := range []string{"json", "jsonl", "human"} {
				t.Run(command.name+"/"+result.name+"/"+format, func(t *testing.T) {
					fake := &fakeMessageSender{result: result.result, err: result.err}
					withMessageSender = func(fn func(messageSender) error) error { return fn(fake) }
					args := append([]string{"--format", format}, command.args...)
					out, err := runReceiptCommand(t, args...)
					if got := ExitCode(err); got != result.want {
						t.Fatalf("exit = %d, want %d (error: %v)", got, result.want, err)
					}
					if fake.called != command.method {
						t.Fatalf("called %q, want %q", fake.called, command.method)
					}
					if result.err != nil && !errors.Is(err, result.err) {
						t.Errorf("underlying error lost: %v", err)
					}
					if result.want != 0 {
						if out != "" {
							t.Errorf("failure printed success receipt: %q", out)
						}
					} else if format == "human" {
						if strings.TrimSpace(out) == "" {
							t.Error("success printed no receipt")
						}
					} else {
						var receipt map[string]any
						if err := json.Unmarshal([]byte(out), &receipt); err != nil || receipt["message_id"] != "SENT123" {
							t.Errorf("invalid success receipt: %q (%v)", out, err)
						}
					}
				})
			}
		}
	}
}

func TestReceiptCommandUsageExitStatus(t *testing.T) {
	oldSender := withMessageSender
	t.Cleanup(func() { withMessageSender = oldSender })
	withMessageSender = func(func(messageSender) error) error {
		t.Fatal("usage errors must not connect")
		return nil
	}
	for _, args := range [][]string{
		{"send"},
		{"send", "--file", "photo.jpg"},
		{"send", fixtureChatNamed, "hello", "--unknown"},
		{"send", fixtureChatNamed, "hello", "--timeout", "bad"},
		{"forward", fixtureChatNamed, "ORIGINAL"},
		{"react", "ORIGINAL", "thumbsup"},
		{"react", "ORIGINAL", "--remove"},
		{"delete", "ORIGINAL"},
		{"unknown-command"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, err := runReceiptCommand(t, args...)
			if got := ExitCode(err); got != 2 {
				t.Errorf("exit = %d, want 2 (error: %v)", got, err)
			}
			if out != "" {
				t.Errorf("usage error printed receipt: %q", out)
			}
		})
	}
}

func TestReceiptCommandConnectionFailure(t *testing.T) {
	oldSender := withMessageSender
	t.Cleanup(func() { withMessageSender = oldSender })
	cause := errors.New("connection aborted")
	withMessageSender = func(func(messageSender) error) error { return cause }
	out, err := runReceiptCommand(t, "send", fixtureChatNamed, "hello")
	if ExitCode(err) != 1 || !errors.Is(err, cause) || out != "" {
		t.Fatalf("connection failure: stdout=%q, error=%v, exit=%d", out, err, ExitCode(err))
	}
}
