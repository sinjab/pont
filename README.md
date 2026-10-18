# pont

![pont](docs/heading.png)

WhatsApp from your terminal. Pipe it, script it, automate it.

Actively maintained fork of [eddmann/whatsapp-cli](https://github.com/eddmann/whatsapp-cli)
(upstream silent since 2026-05-26). This repo is the canonical home of the
runtime patches that keep the CLI working against WhatsApp server-side changes.

**Carries beyond upstream v1.0.1:** whatsmeow protocol bump (survives the
2026-09-29 "Client outdated 405" wave) · `delete` (revoke for everyone) ·
media download via signed URL after media-conn 403s + filename de-collision ·
document MIME/filename fix · GetQRChannel error propagation (no phantom
"Authenticated!") · `auth login --phone` pairing-code linking (codes live
~160s) · `watch` JSONL event stream for companion-device consumers ·
reaction-key Participant fix (servers drop reactions without the original
author's JID) · `WHATSAPP_LOG_LEVEL` override for wire-level diagnosis.

**Maintenance rules:** weekly bot PRs bump whatsmeow — merge **only after
the live smoke test passes** (`scripts/smoke-test.sh` on the dedicated smoke
device; green CI cannot see the WhatsApp network). Back up session + message
DBs before installs; whatsmeow store migrations are forward-only.
Install: `brew install sinjab/tap/pont`.

> Exploring CLI tools as skills for AI agents. [Background below](#background).

## Features

- All your WhatsApp data — chats, messages, contacts, groups, media
- Script and automate — composable with `--jq`, pipes, xargs, and standard Unix tools
- [AI agent ready](#ai-agent-integration) — install the skill for Claude, Cursor, and other assistants
- Flexible output — JSON for scripts, CSV for spreadsheets, tables for humans
- Durable live event stream — `watch` journals every event before emitting it, replays gaps with `--since-seq`, surfaces delivery receipts (`message.ack`), and can fan out to Unix-socket subscribers and per-message hooks

## Installation

### Homebrew (Recommended)

```bash
brew install sinjab/tap/pont
```

### From Source

Requires Go 1.26+ with CGO enabled (for SQLite). FFmpeg optional for audio conversion.

```bash
git clone https://github.com/sinjab/pont
cd pont
make build

# Binary at ./dist/pont
```

A `whatsapp` symlink is installed alongside `pont` for backwards compatibility — every existing script keeps working.

## Quick Start

```bash
# 1. Authenticate with WhatsApp (scan QR code)
pont auth login

# 2. List your chats to find JIDs
pont chats

# 3. Read messages from a chat
pont messages 1234567890@s.whatsapp.net

# 4. Send a message
pont send 1234567890@s.whatsapp.net 'Hello!'

# 5. Search across all messages
pont search "meeting tomorrow"
```

## Command Reference

### Global Options

| Flag            | Description                                           |
| --------------- | ----------------------------------------------------- |
| `-f, --format`  | Output format: json (default), jsonl, csv, tsv, human |
| `--fields`      | Comma-separated fields to include in output           |
| `--no-header`   | Skip header row in CSV/TSV output                     |
| `--store DIR`   | Override store directory                              |
| `--data-dir DIR`| Point ALL state (session, DBs, media, aliases) at DIR; created 0700 on first use. Flag > `$PONT_DATA_DIR` > default |
| `--timeout DUR` | Bound the sync/auth waits (0 = per-command default) |
| `--jq EXPR`     | gojq expression over the JSON output (strings print bare) |
| `-v, --verbose` | Verbose logging to stderr                             |
| `-V, --version` | Show version                                          |

### Authentication

```bash
pont auth login      # QR code auth + initial sync
pont auth logout     # Disconnect and clear session
pont auth status     # Show connection status and DB stats
```

### Sync

```bash
pont sync            # One-time message sync
pont sync --follow   # Continuous sync (daemon mode)
```

### Chats & Messages

```bash
pont chats                    # List all chats
pont chats --groups           # Groups only
pont chats --query "John"     # Filter by name

pont messages <jid>           # View messages
pont messages <jid> --limit 100
pont messages <jid> --timeframe today
pont messages <jid> --type image
```

### Search

```bash
pont search "keyword"
pont search "keyword" --chat <jid>
pont search "keyword" --timeframe this_week
```

### Send, Forward, React, Delete

```bash
pont send <jid> "message"
pont send <jid> --file photo.jpg --caption "Check this"
pont send <jid> "Reply" --reply-to <msg-id>

pont forward <to-jid> <msg-id> --from <source-jid>

pont react <msg-id> "thumbsup" --chat <jid>
pont react <msg-id> --remove --chat <jid>

pont delete <msg-id> --chat <jid>       # delete for everyone (revoke)
```

### Groups

```bash
pont groups                   # List groups
pont groups <jid>             # Group info + members
pont groups join <code>       # Join via invite
pont groups leave <jid>
pont groups rename <jid> "Name"
```

### Other Commands

```bash
pont contacts [--query]
pont alias [<jid> <name>] [--remove]
pont download <msg-id> --chat <jid>
pont export <jid> [--output file.json]
pont context [--chats N] [--messages N]
pont doctor [--connect]
```

### Watch (live event stream)

```bash
pont watch                          # Stream live events as JSON lines
pont watch --since-seq 42           # Replay journaled events after seq 42, then live
pont watch --socket /path/watch.sock  # Broadcast every live line to Unix-socket subscribers
pont watch --on-message 'handler.sh'  # Run a command per message/message.ack event (JSON on stdin)
```

Every event is journaled durably **before** it is emitted — a restarted consumer loses nothing. Message events carry `schema_version`, canonical `is_from_me` / `media_type` fields (legacy `from_me` / `type` aliases remain for now), and no sink failure can kill the watcher.

Delivery receipts surface as `message.ack` events (schema_version 1.1): delivered / read / read-self / played confirmations for outgoing messages, plus other-device reads of incoming ones. They honor `--jid` filtering, replay via `--since-seq`, and reach the `--socket` fan-out and `--on-message` hooks like any message event:

```json
{"event":"message.ack","ts":"2026-10-18T09:31:02Z","schema_version":"1.1","ack":{"chat_jid":"971500000000@s.whatsapp.net","message_ids":["3EB0ACK0009"],"receipt_type":"read","timestamp":"2026-10-18T09:31:00Z","is_from_me":true}}
```

`ack.sender` names the receipt issuer when it differs from the chat (group reads); `message_ids` preserves server order (Android sends newest-first, iOS oldest-first).

## Timeframe Presets

Use with `--timeframe` on messages and search:

| Preset        | Description        |
| ------------- | ------------------ |
| `last_hour`   | Past 60 minutes    |
| `today`       | Since midnight     |
| `yesterday`   | Yesterday only     |
| `last_3_days` | Past 3 days        |
| `this_week`   | Since Monday       |
| `last_week`   | Previous week      |
| `this_month`  | Since 1st of month |

## Composability

```bash
# Filter groups by name
pont chats --groups | jq '.[] | select(.name | contains("work"))'

# Search today's messages and format
pont search "meeting" --timeframe today | jq -r '.[] | "\(.sender_name): \(.content)"'

# Export messages to CSV
pont messages <jid> --format csv > messages.csv

# gh-style field extraction without jq
pont chats --jq '.[].name'
pont messages <jid> --format jsonl --jq '.content'
```

## Configuration

### Storage Location

All data is stored in `~/.config/whatsapp-cli/`:

```
~/.config/whatsapp-cli/
├── store/
│   ├── session.db      # WhatsApp session (whatsmeow)
│   ├── messages.db     # Messages & chats (SQLite + FTS5, WAL)
│   │                   # + watch_events journal (watch stores only)
│   └── <jid>/          # Downloaded media files
└── aliases.json        # Local JID aliases
```

### Per-Instance Data Directories

Point a pont process's **entire** state at an arbitrary directory with `--data-dir DIR` or `PONT_DATA_DIR` (the flag wins; both beat the default, and `--store` still overrides everything for backwards compatibility). The directory is created with 0700 perms on first use.

This is the seam for running N isolated pont processes — one per WhatsApp number: each gets its own session, message DBs, media, aliases and watch journal, so a `watch` on instance A can never see instance B's events.

```bash
pont auth login --data-dir ~/pont/number-a     # pair once per instance
pont watch --data-dir ~/pont/number-a          # journal + stream, isolated
PONT_DATA_DIR=~/pont/number-b pont send <jid> 'hi'
```

### Environment Variables

| Variable          | Description                                          |
| ----------------- | ---------------------------------------------------- |
| `WHATSAPP_FORMAT` | Default output format (json, jsonl, csv, tsv, human) |
| `PONT_DATA_DIR`   | Data directory for ALL state (overridden by `--data-dir`) |
| `XDG_CONFIG_HOME` | Override config directory base                       |

## AI Agent Integration

This CLI is available as an [Agent Skill](https://agentskills.io/) — it works with Claude Code, Cursor, and other compatible AI agents. See [`SKILL.md`](SKILL.md) for the skill definition.

### Install Agent Skill

```bash
curl -fsSL https://raw.githubusercontent.com/sinjab/pont/main/install-skill.sh | sh
```

Installs the skill to `~/.claude/skills/whatsapp/` and `~/.cursor/skills/whatsapp/`. Agents will auto-detect when you ask about WhatsApp messages.

## Development

```bash
git clone https://github.com/sinjab/pont
cd pont
make build                    # Build binary
make test                     # Run tests
make dev CMD="chats --limit 5"  # Build and run
```

## Background

This project began as [whatsapp-cli](https://github.com/eddmann/whatsapp-cli) by Edd Mann (who also built [whatsapp-mcp](https://github.com/eddmann/whatsapp-mcp)), exploring alternative approaches to giving AI agents capabilities. It is now independently maintained as pont — the origin design carries through.

There's been a lot of discussion around the heavyweight nature of MCP. An alternative approach is to give agents [discoverable skills](https://www.anthropic.com/engineering/equipping-agents-for-the-real-world-with-agent-skills) via well-documented CLI tooling. Give an LLM a terminal and let it use composable CLI tools to build up functionality and solve problems — the Unix philosophy applied to AI agents.

This project is an exploration of [Claude Code Skills](https://simonwillison.net/2025/Oct/16/claude-skills/) and the emerging [Agent Skills](https://agentskills.io/) standard for AI-tool interoperability. The goal was to build a CLI that works seamlessly as both:

1. **A traditional Unix tool** — composable, pipe-friendly, machine-readable
2. **An AI agent skill** — structured output, comprehensive documentation, predictable behavior

Going forward, another approach worth exploring is going one step further than CLI and providing a [code library that agents can import and use directly](https://www.anthropic.com/engineering/code-execution-with-mcp).

## License

MIT

## Credits

Built on [whatsmeow](https://github.com/tulir/whatsmeow).
