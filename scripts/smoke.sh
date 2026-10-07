#!/bin/sh
# Smoke test for a built uped binary. Starts it on a free port with a
# throwaway data directory, exercises every HTTP endpoint with curl, and
# checks that a 40 MiB file uploaded in 16 MiB chunks downloads byte for byte.
#
# Usage: sh scripts/smoke.sh [path/to/uped]     (default: dist/uped)
# Needs: curl, sha256sum, dd, sed, grep. POSIX sh only.
set -eu

BIN=${1:-dist/uped}
[ -x "$BIN" ] || { echo "smoke: $BIN not found or not executable; run make build" >&2; exit 1; }
for tool in curl sha256sum dd sed grep; do
  command -v "$tool" >/dev/null 2>&1 || { echo "smoke: $tool is required" >&2; exit 1; }
done

WORK=$(mktemp -d)
PID=
cleanup() {
  if [ -n "$PID" ]; then kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

fail() {
  echo "smoke: FAIL: $*" >&2
  echo "--- server log" >&2
  cat "$WORK/log" >&2 || true
  exit 1
}
ok() { echo "ok   $*"; }

# status METHOD URL [curl args...] prints the HTTP status code.
status() {
  m=$1; u=$2; shift 2
  curl -s -o /dev/null -w '%{http_code}' -X "$m" "$@" "$u"
}

expect_status() {
  want=$1; desc=$2; shift 2
  got=$(status "$@") || true
  [ "$got" = "$want" ] || fail "$desc: HTTP $got, want $want"
  ok "$desc ($got)"
}

json_field() { # json_field NAME < json  (flat string fields only)
  sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p"
}

"$BIN" --listen 127.0.0.1:0 --data "$WORK/data" >"$WORK/log" 2>&1 &
PID=$!

PORT=
i=0
while [ $i -lt 100 ]; do
  PORT=$(sed -n 's/.*listen=127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$WORK/log" | head -n 1)
  [ -n "$PORT" ] && break
  kill -0 "$PID" 2>/dev/null || fail "server exited during startup"
  i=$((i + 1))
  sleep 0.1
done
[ -n "$PORT" ] || fail "server did not report its port"
BASE="http://127.0.0.1:$PORT"
ok "server started on $BASE"

# --- basics
[ "$(curl -fsS "$BASE/healthz")" = ok ] || fail "healthz"
ok "healthz"
curl -fsS "$BASE/" | grep -q '<title>uped</title>' || fail "index page"
ok "index page"
expect_status 200 "static app.js" GET "$BASE/static/app.js"
curl -fsS "$BASE/api/config" | grep -q '"chunkSize":16777216' || fail "config chunkSize"
ok "config"
first=$(curl -sN --max-time 2 "$BASE/api/events" | dd bs=12 count=1 2>/dev/null || true)
[ "$first" = ": connected" ] || fail "event stream starts with '$first'"
ok "event stream"

# --- 40 MiB upload in 16 MiB chunks
SIZE=41943040
CHUNK=16777216
dd if=/dev/urandom of="$WORK/big.bin" bs=1048576 count=40 2>/dev/null
SUM=$(sha256sum "$WORK/big.bin" | cut -d ' ' -f 1)
CREATE="{\"name\":\"big.bin\",\"dir\":\"smoke/run\",\"size\":$SIZE,\"fingerprint\":\"smoke-$SUM\"}"
RESP=$(curl -fsS -H 'Content-Type: application/json' -d "$CREATE" "$BASE/api/uploads") || fail "create upload"
ID=$(printf '%s' "$RESP" | json_field id)
[ -n "$ID" ] || fail "no upload id in $RESP"
ok "upload created ($ID)"

expect_status 409 "wrong offset rejected" PUT "$BASE/api/uploads/$ID?offset=5" --data-binary 'x'

OFF=0
N=0
while [ "$OFF" -lt "$SIZE" ]; do
  dd if="$WORK/big.bin" of="$WORK/chunk" bs=$CHUNK skip=$N count=1 2>/dev/null
  got=$(status PUT "$BASE/api/uploads/$ID?offset=$OFF" --data-binary @"$WORK/chunk" \
    -H 'Content-Type: application/offset+octet-stream')
  [ "$got" = 204 ] || fail "chunk $N at offset $OFF: HTTP $got"
  OFF=$((OFF + $(wc -c <"$WORK/chunk")))
  N=$((N + 1))
done
ok "sent $N chunks"
curl -fsSI "$BASE/api/uploads/$ID" | grep -qi "^upload-offset: $SIZE" || fail "HEAD offset"
ok "HEAD reports full offset"

DONE=$(curl -fsS -X POST "$BASE/api/uploads/$ID/finish") || fail "finish"
[ "$(printf '%s' "$DONE" | json_field path)" = "smoke/run/big.bin" ] || fail "finish path in $DONE"
ok "finished"

curl -fsS -o "$WORK/down.bin" "$BASE/d/smoke/run/big.bin" || fail "download"
[ "$(sha256sum "$WORK/down.bin" | cut -d ' ' -f 1)" = "$SUM" ] || fail "sha256 mismatch after download"
ok "40 MiB download matches sha256"
[ "$(curl -fsS -r 0-9 "$BASE/d/smoke/run/big.bin" | wc -c | tr -d ' ')" = 10 ] || fail "range request"
expect_status 206 "range status" GET "$BASE/d/smoke/run/big.bin" -r 0-9

curl -fsS -H 'Content-Type: application/json' -d "$CREATE" "$BASE/api/uploads" | grep -q '"state":"done"' \
  || fail "re-drop of the same file was not skipped"
ok "re-drop skipped"

# --- abort
RESP=$(curl -fsS -H 'Content-Type: application/json' -d '{"name":"gone.bin","size":10}' "$BASE/api/uploads")
ID2=$(printf '%s' "$RESP" | json_field id)
expect_status 204 "abort upload" DELETE "$BASE/api/uploads/$ID2"
expect_status 404 "aborted upload gone" GET "$BASE/api/uploads/$ID2"

# --- text, list, zip, delete
RESP=$(curl -fsS -H 'Content-Type: application/json' -d '{"dir":"smoke","text":"Smoke note\nhello"}' "$BASE/api/text") || fail "post text"
[ "$(printf '%s' "$RESP" | json_field path)" = "smoke/smoke-note.txt" ] || fail "text path in $RESP"
[ "$(curl -fsS "$BASE/d/smoke/smoke-note.txt")" = "$(printf 'Smoke note\nhello')" ] || fail "text content"
ok "text snippet"
LIST=$(curl -fsS "$BASE/api/list?path=smoke") || fail "list"
printf '%s' "$LIST" | grep -q '"name":"smoke-note.txt"' || fail "list lacks note: $LIST"
printf '%s' "$LIST" | grep -q '"name":"run"' || fail "list lacks folder: $LIST"
ok "list"
curl -fsS -o "$WORK/smoke.zip" "$BASE/api/zip?path=smoke" || fail "zip"
[ "$(dd if="$WORK/smoke.zip" bs=2 count=1 2>/dev/null)" = PK ] || fail "zip magic"
[ "$(wc -c <"$WORK/smoke.zip" | tr -d ' ')" -gt "$SIZE" ] || fail "zip too small"
ok "zip"
expect_status 204 "delete folder" DELETE "$BASE/api/items?path=smoke"
curl -fsS "$BASE/api/list" | grep -q '"entries":\[\]' || fail "root not empty after delete"
ok "root empty after delete"

# --- refusals
expect_status 400 "list with .." GET "$BASE/api/list?path=.."
got=$(status GET "$BASE/d/../../etc/passwd" --path-as-is)
case $got in 2*) fail "traversal download returned $got" ;; esac
ok "traversal download refused ($got)"
expect_status 404 "unknown path" GET "$BASE/nope"

# --- clean shutdown
kill -TERM "$PID"
code=0
wait "$PID" || code=$?
PID=
[ "$code" = 0 ] || fail "server exited with $code after SIGTERM"
grep -q 'shutting down' "$WORK/log" || fail "no shutdown log line"
ok "clean shutdown"

echo "SMOKE OK"
