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
#     whatsapp auth login --phone 971503972970
#   # Settings -> Linked Devices -> Link a device -> Link with phone number instead
#
# Usage:
#   scripts/smoke-test.sh                 # uses `whatsapp` from PATH
#   WA_BIN=/path/to/binary scripts/smoke-test.sh
set -euo pipefail

STORE="${WA_SMOKE_STORE:-$HOME/Library/Application Support/wa-smoke/store}"
BIN="${WA_BIN:-$(command -v whatsapp)}"
SELF="${WA_SELF_JID:-971503972970@s.whatsapp.net}"

fail() { echo "SMOKE FAIL: $*" >&2; exit 1; }

echo "== binary: $BIN ($("$BIN" --version 2>/dev/null || echo '?'))"
echo "== store:  $STORE"
[ -d "$STORE" ] || fail "smoke store missing — run the one-time setup in the header"

echo "== connect check"
"$BIN" --store "$STORE" doctor --connect

TS=$(date +%s)
echo "== send text to self"
OUT=$("$BIN" --store "$STORE" send "$SELF" "smoke ${TS}")
echo "$OUT"
echo "$OUT" | grep -q '"message_id"' || fail "no message_id in send receipt"

echo "== send file to self + download roundtrip"
FILE_OUT=$("$BIN" --store "$STORE" send "$SELF" "smoke file ${TS}" --file "$0")
echo "$FILE_OUT"
MID=$(echo "$FILE_OUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["message_id"])')
sleep 5
"$BIN" --store "$STORE" download "$MID" --chat "$SELF" | tee /dev/stderr | grep -q '"' || fail "download returned nothing"

sleep 5
echo "== settle check (rows visible in store)"
"$BIN" --store "$STORE" messages "$SELF" --timeframe last_hour --limit 5 | grep -q "smoke" || fail "smoke rows not visible in store after settle"

echo
echo "SMOKE OK — safe to merge."
echo "Reminder: back up session + message DBs before installing the new binary;"
echo "whatsmeow store migrations are forward-only."
