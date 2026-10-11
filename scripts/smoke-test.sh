#!/usr/bin/env bash
# Live smoke gate for whatsapp-cli — required before merging any whatsmeow
# bump (and recommended before any release install).
#
# Green CI cannot see the live WhatsApp network; this script can. It runs
# entirely against the DEDICATED smoke companion device store, so it never
# touches the main store or the watcher device.
#
# One-time setup (interactive, on the Mac — pairing codes live ~160 SECONDS,
# generate only when the phone entry screen is open):
#
#   WA_SMOKE_STORE="$HOME/Library/Application Support/wa-smoke/store" \
#     whatsapp auth login --phone <digits>
#   # Settings -> Linked Devices -> Link a device -> Link with phone number instead
#
# Usage:
#   scripts/smoke-test.sh                 # uses `whatsapp` from PATH
#   WA_BIN=/path/to/binary scripts/smoke-test.sh
#
# Contract (P0 hardening, Oct 11 2026 — rewrite-consult lanes):
#   - every run carries a UNIQUE token; the settle check must see THIS
#     run's token (a stale last-hour "smoke" row can never pass the gate)
#   - send receipts are the delivery oracle; settle visibility is proven
#     with retries, and a receipt-proven/token-missing settle is a FAIL
#     with the echo-suppression hint (A/B against the installed binary
#     before blaming the diff — proven server-side Oct 11)
#   - when the media row exists, the download roundtrip is hash-verified
#     against the sent file
set -euo pipefail

STORE="${WA_SMOKE_STORE:-$HOME/Library/Application Support/wa-smoke/store}"
BIN="${WA_BIN:-$(command -v whatsapp)}"
SELF="${WA_SELF_JID:-}"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

if [ -z "$SELF" ]; then
  fail "WA_SELF_JID is required (the account's own JID, e.g. 9715XXXXXXXX@s.whatsapp.net) — no hardcoded default"
fi

echo "== binary: $BIN ($("$BIN" --version 2>/dev/null || echo '?'))"
echo "== store:  $STORE  self: $SELF"
[ -d "$STORE" ] || fail "smoke store missing — run the one-time setup in the header"

echo "== connect check"
"$BIN" --store "$STORE" doctor --connect
# NOTE: doctor --connect may report Connection Test ok:false with
# connected:true + logged_in:false — the known flag race (status reads
# before the login handshake); the sends below are the real gate.

TOKEN="smoke-$(date +%s)-$$"
echo "== run token: $TOKEN"

echo "== send text to self"
OUT=$("$BIN" --store "$STORE" send "$SELF" "$TOKEN")
echo "$OUT"
echo "$OUT" | grep -q '"message_id"' || fail "no message_id in send receipt"
TEXT_MID=$(echo "$OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["message_id"])')

echo "== send file to self + download roundtrip"
FILE_OUT=$("$BIN" --store "$STORE" send "$SELF" "$TOKEN file" --file "$0")
echo "$FILE_OUT"
MID=$(echo "$FILE_OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["message_id"])')
SENT_SHA=$(shasum -a 256 "$0" | cut -d' ' -f1)
# Outbound rows are STRUCTURALLY unreliable in the store (receipt is the
# delivery oracle) — sync first so the DeviceSentMessage echo can materialize
# the row, and treat a still-missing row as SKIP, not FAIL.
"$BIN" --store "$STORE" sync >/dev/null 2>&1 || true
sleep 3
if DL=$("$BIN" --store "$STORE" download "$MID" --chat "$SELF" 2>&1); then
  echo "$DL"
  DL_PATH=$(echo "$DL" | python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' 2>/dev/null || true)
  if [ -n "$DL_PATH" ] && [ -f "$DL_PATH" ]; then
    DL_SHA=$(shasum -a 256 "$DL_PATH" | cut -d' ' -f1)
    [ "$DL_SHA" = "$SENT_SHA" ] \
      && echo "== media hash OK ($DL_SHA)" \
      || fail "downloaded media hash mismatch: sent $SENT_SHA got $DL_SHA"
  else
    echo "WARN: could not extract/verify downloaded path — hash check skipped"
  fi
else
  echo "WARN: media row not in store after sync (outbound rows are structurally"
  echo "absent) — download roundtrip SKIPPED; receipts above are the gate."
fi

echo "== settle check (THIS run's token visible in store)"
SETTLE_OK=0
for attempt in 1 2 3; do
  if "$BIN" --store "$STORE" messages "$SELF" --timeframe last_hour --limit 20 2>/dev/null | grep -q "$TOKEN"; then
    SETTLE_OK=1
    echo "== token visible on attempt $attempt (mid $TEXT_MID)"
    break
  fi
  echo "   attempt $attempt: token not visible yet — syncing + waiting"
  "$BIN" --store "$STORE" sync >/dev/null 2>&1 || true
  sleep 5
done
if [ "$SETTLE_OK" != 1 ]; then
  fail "run token '$TOKEN' never appeared in the store (receipts $TEXT_MID/$MID were accepted).
If receipts landed but the row never materialized, this is the known server-side
echo-suppression mode (proven Oct 11: BOTH old and new binaries affected):
A/B against the INSTALLED binary on the same store before blaming this diff;
if both fail, receipts are the gate — document and consciously override."
fi

echo
echo "SMOKE OK — safe to merge."
echo "Reminder: back up session + message DBs before installing the new binary;"
echo "whatsmeow store migrations are forward-only."
