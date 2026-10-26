# pont-meta — FB Messenger + Instagram DMs for pont (AGPL-3.0)

Separate Go module (AGPL-3.0 — see LICENSE) so pont core stays MIT with
its own whatsmeow pin. Rides the protocol clients inside
`go.mau.fi/mautrix-meta` (no Matrix, no bridge). Design decisions + the
3-lane consult record: life repo `data/dispatch-out/pont-meta-consult-r1/`.

## Build

From the pont repo root: `make build-meta` → `dist/pont-meta`.

## Cookie setup (one-time, per platform)

Cookies come from a logged-in real Chrome via CDP. Extract with
agent-browser (any debug-port Chrome):

```bash
agent-browser --json cookies get | python3 - <<'PY'   # FB variant
import sys, json, os
data = json.load(sys.stdin); cookies = data.get('data', {}).get('cookies', [])
want = ['xs','c_user','datr','sb','wd','dpr','fr','ps_n','presence']
out = {c['name']: c['value'] for c in cookies
       if c.get('domain','').endswith('facebook.com') and c['name'] in want}
p = os.path.expanduser('~/.config/pont-meta/cookies-fb.json')
json.dump(out, open(p,'w')); os.chmod(p, 0o600)
PY
```

IG variant: domain `instagram.com`, names
`['sessionid','csrftoken','ds_user_id','mid','ig_did','rur','ig_nrcb','wd']`
→ `cookies-ig.json`. Cookies rotate server-side — every verb writes the
freshest set back.

## Verbs

| verb | what |
|---|---|
| `threads <fb-cookies>` | thread list w/ E2EE flags, contact names |
| `messages <fb-cookies> <thread_key>` | full Messenger thread history |
| `ig-threads <ig-cookies>` | IG inbox w/ names + snippets |
| `ig-messages <ig-cookies> <thread_key\|igid>` | full IG thread (dual-ID resolved) |
| `send <fb-cookies> <thread_key> <text>` | native send, OTID-verified; exit 0 sent / 1 rejected / 3 MAYBE-SENT (never blind-retry) |
| `e2ee-prepare <fb-cookies>` | register E2EE companion device (ONCE; device persists in `~/.config/pont-meta/e2ee-store.db`) |
| `watch <fb-cookies>` | JSONL live events (message, typing, read, conn) + `message_e2ee` when the device exists |

## Laws learned (2026-10-26, live-proven)

- `replace github.com/imroc/req/v3 => beeper/req/v3` in this module's
  go.mod is load-bearing — code calls fork-only functions.
- Init order: `NewClient → LoadMessagesPage → Connect` (Connect refuses
  an unconfigured socket user id).
- Call `PostHandlePublishResponse(tbl)` after every handled LSTable or
  sync cursors stall (pagination + DumpState break).
- Messenger 1:1s are E2EE-cutover (ThreadType 15/16) in general, but
  bodies for those threads need the companion device (`e2ee-prepare`).
  Watch prints `message_e2ee` events (text extraction TODO: armadillo
  unwrap — currently `(encrypted message)` placeholder).
- Read actions must NOT mark-read; pont verbs don't send read tasks.
- IG: `GetThread` wants the IGID (ShortID), not the 34028… long id —
  resolve from thread_key via `FetchThreadID`.
