# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [2.3.1] - 2026-10-12

### Fixed

- `doctor --connect` now waits (up to 10s) for the login handshake
  before reporting `logged_in` — previously it read the flag
  immediately after connect and reported healthy sessions as logged
  out (the same race WithConnection already settles)

### Changed

- Test fixtures bind timestamps as UTC `time.Time`, mirroring
  production writes. This exposed and fixed a false-negative golden:
  the `messages --after/--before` window contract had been frozen as
  an empty result — string-RFC3339 fixture rows never matched the
  driver-format window bounds. The window filter is now actually
  proven (golden regenerated and reviewed)

### Added

- Table-driven unit tests for the store query layer (consult §7, the
  last unshipped recommendation): ListChats ordering/filters/subselects,
  OldestMessageForChat incl. LID join, ListMessages windows/type
  filters, SearchMessages FTS with chat/from/type filters, GetChatName
- ParseTimeframe calendar tests (yesterday→today-midnight, week
  anchors, month start, ordering invariants)

## [2.3.0] - 2026-10-12

### Added

- `--jq <expr>`: gh-style gojq expression over the JSON output
  (P4 feature, Oct 11 rewrite consult). Applies after `--fields`
  filtering, before rendering: with `--format json` the query runs
  over the whole payload (`pont chats --jq '.[].name'`); with
  `--format jsonl` it runs per line. Results print one per line —
  strings bare (grep-able), everything else compact JSON. Non-JSON
  formats reject `--jq` explicitly. Powered by itchyny/gojq

## [2.2.0] - 2026-10-12

### Added

- `watch --socket <path>`: opt-in Unix-domain broadcast bus — every
  live event line (same JSONL bytes as stdout) is written to all
  connected subscribers; they may connect and disconnect at any time
  without killing the watcher (a stalled subscriber is dropped after a
  1s write deadline). Live events only: the journal-replay path stays
  stdout-only, and the classic stdout stream is unchanged. Watch for
  macOS's ~104-byte unix-socket path limit
- `watch --on-message <cmd>`: opt-in hook invoked per message event
  with the JSON line on stdin (run via `sh -c`, async, max 8
  concurrent, 60s cap, saturation skips with a warning). Both sinks
  are strictly AFTER journal + stdout — durability contracts come
  first, and no sink failure is fatal to the watcher (P2 fan-out tail
  from the Oct 11 rewrite consult)

## [2.1.0] - 2026-10-12

### Added

- Watch stream contract normalization: every live event line now
  carries `schema_version: "1.0"`, and message events carry the
  canonical `is_from_me` / `media_type` field names aligned with the
  store models — derived in `MarshalJSON` so alias and canonical can
  never disagree. The legacy `from_me` / `type` aliases remain as
  duplicate output for one release cycle (autoreply and other
  consumers keep working unchanged); they are removed in v3.0
- `--timeout` is now wired: it governs every bounded wait — one-shot
  `sync` (default 2m), connection auto-sync (default 30s), and the
  `auth login` initial sync (default 5m). Passing `0` or omitting the
  flag keeps each command's established default

### Fixed

- Auto-sync freshness lie: a quick-sync that TIMED OUT no longer
  records a fresh last-sync time (which suppressed auto-sync for 24h
  on unconfirmed data). Only a confirmed-complete sync stamps fresh;
  a timed-out attempt re-arms the retry gate after 10 minutes

## [2.0.0] - 2026-10-11

### Changed

- **The project is now `pont`** — an independently maintained original
  project, no longer a fork in name or source: module path
  `github.com/sinjab/pont`, binary `pont`, command identity `pont`.
  Born from whatsapp-cli (MIT, © Edd Mann) and retained with
  attribution; see README
- The Homebrew formula installs `pont` AND a `whatsapp` symlink —
  every existing script, launchd agent, and integration calling
  `whatsapp` keeps working unchanged
- Data homes are UNCHANGED (`~/.config/whatsapp-cli/...` and every
  `--store` path) and env vars keep their `WHATSAPP_*` names — no
  migration, no launchd/TCC/backup changes

## [1.4.0] - 2026-10-11

### Added

- `sender_jid` column (migration v3): the full sender JID
  (user@server) captured at both write sites — disambiguates LID vs
  phone senders sharing digits. Legacy rows stay NULL (never
  fabricated); reads and the JSON contract untouched

### Changed

- Module path is now `github.com/sinjab/whatsapp-cli` (was
  `github.com/eddmann/whatsapp-cli`) — the source identity matches the
  repository that already owns it

## [1.3.0] - 2026-10-11

### Added

- Durable watch event journal (P2): every streamed event is journaled
  to the store BEFORE emission (`watch_events`, migration v2)
- `watch --since-seq N`: replay-then-live — emits all journaled events
  after seq N (lines carry `"seq"`), then continues the live stream;
  a restarted consumer loses nothing and dedupes by id/chat/kind.
  Without the flag the output is byte-identical to the classic stream
- Journal retention: newest 10,000 rows, pruned at watch startup

## [1.2.0] - 2026-10-11

### Changed

- Store hardening (P1): WAL journal mode + busy_timeout; numbered
  migrations tracked via `PRAGMA user_version`; the FTS index rebuild
  now runs ONCE at migration instead of on every database open
- Message/chat persistence uses `INSERT .. ON CONFLICT` upserts —
  rowids stay stable and re-delivered echoes no longer null columns
- Read commands (`chats`, `messages`, `search`) are pure local queries:
  the implicit >24h auto-sync on reads was removed — refresh explicitly
  with `sync`

### Added

- Golden contract tests freezing the JSON output shapes (P0) and a
  hardened live smoke gate (unique per-run token, hash-verified media
  roundtrip)
- `(chat_jid, timestamp DESC)` index for message listing queries

## [1.1.1] - 2026-10-11

### Fixed

- `auth login --phone`: the success banner is now gated on server-confirmed
  pairing (PairSuccess + post-pair reconnect) instead of printing immediately
  after the pairing code (phantom success, #1)
- pairing failures exit honestly: code expiry, server socket loss and
  Ctrl+C report errors instead of claiming authentication
- stale never-paired device rows (empty jid, left behind by a pairing
  holder killed mid-attempt) are swept automatically at client construction

## [1.1.0] - 2026-10-11

### Added

- `watch` command for live message watching (#17)
- `auth login --phone` pairing-code linking (#18)
- `delete` command for local message deletion (#13)

### Changed

- Canonical home is now the sinjab fork (brew: `sinjab/tap/whatsapp-cli-sinjab`)
- whatsmeow protocol update (#14), media URL download fix (#15),
  QR channel error propagation (#16), document MIME fix (#8), reaction-key
  Participant fix, `WHATSAPP_LOG_LEVEL` override

## [1.0.1] - 2026-05-26

### Changed

- Require Go 1.25.10 to pick up current Go standard-library security fixes
- Refresh Cobra and QR terminal dependencies for CLI maintenance

### Fixed

- Update whatsmeow for improved WhatsApp protocol stability (#6)

## [1.0.0] - 2026-05-20

### Added

- Request on-demand historical chat pages with `whatsapp backfill`
- Report backfill progress with requested pages, synced message count, and availability of older messages

### Changed

- Require Go 1.25+ and refresh WhatsApp, SQLite, and supporting dependencies
- Wait briefly after history sync completion so late-arriving messages are persisted before commands continue

### Fixed

- Resolve LID senders to phone-number JIDs for quoted replies in group chats (#2)
- Improve WhatsApp auth stability with the latest whatsmeow client updates
- Prevent empty local stores from being treated as completed offline syncs
- Use the current reflect pointer kind when rendering selected output fields

## [0.2.1] - 2026-01-20

### Changed

- Release workflow now extracts version and release notes automatically from CHANGELOG.md

## [0.2.0] - 2026-01-02

### Added

- Automatic sync when data is stale
- GitHub Pages landing site
- Homebrew tap auto-update to release workflow

### Changed

- Wrap terminal animation in IIFE with ES6 conventions
- Update whatsmeow to latest version for auth stability
- Clarify CLI is available as an Agent Skill, not follows the standard

### Fixed

- Increase golangci-lint timeout to 5 minutes

## [0.1.0] - 2025-12-23

### Added

- Initial whatsapp-cli implementation

### Fixed

- Use macos-15-intel instead of deprecated macos-13

[1.0.1]: https://github.com/eddmann/whatsapp-cli/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/eddmann/whatsapp-cli/compare/v0.2.1...v1.0.0
[0.2.1]: https://github.com/eddmann/whatsapp-cli/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/eddmann/whatsapp-cli/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/eddmann/whatsapp-cli/releases/tag/v0.1.0
