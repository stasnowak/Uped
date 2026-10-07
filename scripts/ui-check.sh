#!/bin/sh
# Runs scripts/ui-check.js against a fresh uped on a free port.
# Usage: sh scripts/ui-check.sh [path/to/uped] [screenshot dir]
# Needs node with the playwright package and its Chromium (preinstalled in
# cloud sessions; never run "playwright install" there).
set -eu
BIN=${1:-dist/uped}
[ -x "$BIN" ] || { echo "ui-check: $BIN not found; run make build" >&2; exit 1; }
WORK=$(mktemp -d)
PID=
cleanup() {
  if [ -n "$PID" ]; then kill "$PID" 2>/dev/null || true; wait "$PID" 2>/dev/null || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

"$BIN" --listen 127.0.0.1:0 --data "$WORK/data" --chunk-size 1M >"$WORK/log" 2>&1 &
PID=$!
PORT=
i=0
while [ $i -lt 100 ]; do
  PORT=$(sed -n 's/.*listen=127\.0\.0\.1:\([0-9][0-9]*\).*/\1/p' "$WORK/log" | head -n 1)
  [ -n "$PORT" ] && break
  i=$((i + 1))
  sleep 0.1
done
[ -n "$PORT" ] || { echo "ui-check: server did not start" >&2; cat "$WORK/log" >&2; exit 1; }
node "$(dirname "$0")/ui-check.js" "http://127.0.0.1:$PORT" ${2:+"$2"}
