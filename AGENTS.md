# AGENTS.md

This file provides guidance to AI coding agents when working with code in this repository.

## Project Overview

pont provides WhatsApp access from your terminal. Machine-readable output (JSON, JSONL, CSV, TSV, human tables).

## Development Commands

```bash
make build                          # Build binary (requires CGO)
make run CMD="chats --limit 5"      # Run CLI command
make test                           # Run all tests
make lint                           # Check linting
make fmt                            # Format code
make can-release                    # Full CI (lint + test)
```

## Architecture

```
cmd/pont/main.go                   # Entry point (binary: dist/pont)

internal/
├── cli/                            # Cobra commands, config, output, temporal parsing
├── store/                          # SQLite + FTS5 database, models (Chat, Message, Contact)
└── whatsapp/                       # WhatsApp client (connection, sync, messaging, media)
```

Requires Go 1.26+, CGO enabled, SQLite with FTS5. FFmpeg optional for audio.

### Key Pattern

Commands use shared helpers for database and client setup:

```go
// Database-only (chats, messages, search)
func runChats(cmd *cobra.Command, args []string) error {
    return WithDB(func(db *store.DB) error {
        chats, err := db.ListChats(store.ListChatsOptions{...})
        if err != nil {
            return err
        }
        return Output(chats)  // Auto-serialized based on --format
    })
}

// With WhatsApp connection (send, sync)
func runSend(cmd *cobra.Command, args []string) error {
    return WithConnection(func(db *store.DB, client *whatsapp.Client) error {
        result, err := client.SendTextMessage(jid, message)
        if err != nil {
            return err
        }
        return Output(result)
    })
}
```

### Testing

Tests use `go test -tags sqlite_fts5`. Manual testing: `./dist/pont doctor` and `./dist/pont auth status`.

Hard rules for agents: never touch the operator's live session/message store on
this machine — tests must use temp dirs (`--data-dir`/`PONT_DATA_DIR` or
`t.TempDir()`); merge whatsmeow version bumps only after the live smoke test
passes (`scripts/smoke-test.sh` on the dedicated smoke device — green CI cannot
see the WhatsApp network). The watch JSONL envelope is contract-frozen by
`internal/cli/watch_envelope_test.go` goldens (regenerate deliberately with
`go test -tags sqlite_fts5 ./internal/cli/ -update`).

### Data Units

All timestamps ISO8601 UTC. JIDs: `phone@s.whatsapp.net` (individual), `id@g.us` (group).
