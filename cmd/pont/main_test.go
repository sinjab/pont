package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMainExitCodes(t *testing.T) {
	if os.Getenv("PONT_TEST_MAIN") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"pont"}, os.Args[i+1:]...)
				break
			}
		}
		main()
		os.Exit(0)
	}

	for _, tc := range []struct {
		name string
		args []string
		code int
	}{
		{"help", []string{"--help"}, 0},
		{"send failure", []string{"send", "1234@s.whatsapp.net", "hello"}, 1},
		{"media failure", []string{"send", "1234@s.whatsapp.net", "--file", "photo.jpg"}, 1},
		{"forward failure", []string{"forward", "1234@s.whatsapp.net", "ORIGINAL", "--from", "1234@s.whatsapp.net"}, 1},
		{"react failure", []string{"react", "ORIGINAL", "thumbsup", "--chat", "1234@s.whatsapp.net"}, 1},
		{"delete failure", []string{"delete", "ORIGINAL", "--chat", "1234@s.whatsapp.net"}, 1},
		{"missing arguments", []string{"send"}, 2},
		{"missing required flag", []string{"forward", "1234@s.whatsapp.net", "ORIGINAL"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Empty temp stores fail authentication before Connect is called.
			dir := t.TempDir()
			args := append([]string{"-test.run=^TestMainExitCodes$", "--", "--data-dir", dir, "--format", "json"}, tc.args...)
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = append(os.Environ(), "PONT_TEST_MAIN=1", "PONT_DATA_DIR="+dir)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				code = exitErr.ExitCode()
			}
			if code != tc.code {
				t.Fatalf("exit = %d, want %d; stderr=%s", code, tc.code, &stderr)
			}
			if tc.code != 0 {
				var payload map[string]string
				if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil || payload["error"] == "" {
					t.Errorf("missing stderr error JSON: %q (%v)", &stderr, err)
				}
				if stdout.Len() != 0 {
					t.Errorf("failure printed stdout receipt: %q", &stdout)
				}
				if tc.code == 1 && !strings.Contains(payload["error"], "not authenticated") {
					t.Errorf("unexpected offline failure: %q", payload["error"])
				}
			}
		})
	}
}
