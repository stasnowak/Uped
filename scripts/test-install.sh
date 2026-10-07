#!/bin/sh
# Tests install.sh without root, OpenRC or internet access. Every install
# uses --prefix (which implies --no-service), with locally built binaries
# and a local HTTP server standing in for GitHub Releases.
#
# Usage: sh scripts/test-install.sh        (needs go; Linux)
# Optional: python3 for the download tests, busybox to run the installer
# with nothing but busybox tools, as on Alpine. Missing ones are skipped.
set -eu

cd "$(dirname "$0")/.."
WORK=$(mktemp -d)
PIDS=
cleanup() {
  for p in $PIDS; do kill "$p" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

fail() {
  echo "test-install: FAIL: $*" >&2
  if [ -f "$WORK/out" ]; then
    echo "--- installer output" >&2
    cat "$WORK/out" >&2
  fi
  exit 1
}
ok() { echo "ok   $*"; }
skip() { echo "skip $*"; }

# inst ARGS... runs install.sh with output in $WORK/out and returns its status.
inst() { sh ./install.sh "$@" >"$WORK/out" 2>&1; }
says() { grep -q -- "$1" "$WORK/out" || fail "installer output lacks \"$1\""; }
mode() { stat -c %a "$1"; }
version_of() { "$1" --version | awk '{print $2}'; }
must_exist() { for f in "$@"; do [ -e "$f" ] || fail "$f is missing"; done; }
must_not_exist() { for f in "$@"; do [ ! -e "$f" ] || fail "$f should not exist"; done; }

case $(uname -m) in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "unsupported machine $(uname -m)" ;;
esac
ASSET=uped_linux_$ARCH.tar.gz

echo "building two versions of uped"
for v in 1 2; do
  CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=v0.0.$v-test" -o "$WORK/v$v/uped" ./cmd/uped
done
# A release tree laid out like github.com/<repo>/releases.
make_release() { # make_release DIR BINARY_DIR
  mkdir -p "$1"
  tar -czf "$1/$ASSET" -C "$2" uped
  (cd "$1" && sha256sum "$ASSET" >checksums.txt)
}
make_release "$WORK/rel/latest/download" "$WORK/v2"
make_release "$WORK/rel/download/v0.0.1-test" "$WORK/v1"

# ---------- syntax and options

sh -n install.sh || fail "install.sh has syntax errors"
sh -n packaging/openrc/uped.initd || fail "uped.initd has syntax errors"
sh -n packaging/openrc/uped.confd || fail "uped.confd has syntax errors"
ok "sh -n install.sh and the OpenRC files"

inst --help || fail "--help failed"
says "--uninstall"
says "--prefix DIR"
ok "--help"

status=0
inst --bogus || status=$?
[ "$status" -eq 2 ] || fail "--bogus exited $status, want 2"
says "unknown option: --bogus"
ok "unknown option exits 2"

if [ ! -f /etc/alpine-release ]; then
  if inst --binary "$WORK/v1/uped"; then fail "a real install outside Alpine (or without root) was accepted"; fi
  grep -qE "run this as root|for Alpine Linux" "$WORK/out" || fail "refusal does not say why"
  ok "refuses a real install off Alpine or without root: $(cat "$WORK/out")"
fi

# ---------- install, re-run, upgrade

P=$WORK/p1
inst --binary "$WORK/v1/uped" --prefix "$P" || fail "install with --binary failed"
says "Installing uped v0.0.1-test"
BIN=$P/usr/local/bin/uped
INITD=$P/etc/init.d/uped
CONF=$P/etc/conf.d/uped
must_exist "$BIN" "$INITD" "$CONF" "$P/var/lib/uped" "$P/var/log/uped"
[ "$(mode "$BIN")" = 755 ] || fail "binary mode $(mode "$BIN")"
[ "$(mode "$INITD")" = 755 ] || fail "init script mode $(mode "$INITD")"
[ "$(mode "$CONF")" = 644 ] || fail "conf.d mode $(mode "$CONF")"
[ "$(mode "$P/var/lib/uped")" = 750 ] || fail "data dir mode $(mode "$P/var/lib/uped")"
[ "$(mode "$P/var/log/uped")" = 750 ] || fail "log dir mode $(mode "$P/var/log/uped")"
[ "$(version_of "$BIN")" = v0.0.1-test ] || fail "installed version $(version_of "$BIN")"
ok "install --binary --prefix: binary, init script, conf.d, data and log dirs with the right modes"

sh -n "$INITD" || fail "installed init script has syntax errors"
cmp -s "$INITD" packaging/openrc/uped.initd || fail "installed init script differs from packaging/openrc/uped.initd"
cmp -s "$CONF" packaging/openrc/uped.confd || fail "installed conf.d differs from packaging/openrc/uped.confd"
ok "installed init script and conf.d match packaging/openrc/ and pass sh -n"

# The init script turns conf.d settings into flags; uped must accept them.
# Later flags win, so the real address and data dir can be overridden.
check_args() { # check_args WANT
  # shellcheck disable=SC1090,SC2154 # sourced files define command_args
  args=$(. "$CONF" && . "$INITD" && printf '%s' "$command_args")
  [ "$args" = "$1" ] || fail "command_args is \"$args\", want \"$1\""
  # shellcheck disable=SC2086 # word splitting is the point, as in openrc-run
  "$BIN" $args --listen 127.0.0.1:0 --data "$WORK/run-data" >"$WORK/run.log" 2>&1 &
  pid=$!
  PIDS="$PIDS $pid"
  i=0
  until grep -q "listen=127.0.0.1:" "$WORK/run.log"; do
    kill -0 "$pid" 2>/dev/null || fail "uped rejected the init script's flags: $(cat "$WORK/run.log")"
    i=$((i + 1))
    [ $i -lt 100 ] || fail "uped did not start with the init script's flags"
    sleep 0.1
  done
  kill "$pid"
  wait "$pid" 2>/dev/null || true
}
check_args "--listen :8080 --data /var/lib/uped --ttl 168h --min-free 1G --max-file-size 0 "
cp "$CONF" "$WORK/conf.orig"
sed -i -e 's/^UPED_TTL=.*/UPED_TTL="30d"/' -e 's/^UPED_EXTRA_ARGS=.*/UPED_EXTRA_ARGS="--chunk-size 8M"/' \
  -e 's/^UPED_MAX_FILE_SIZE=.*/UPED_MAX_FILE_SIZE="20G"/' "$CONF"
check_args "--listen :8080 --data /var/lib/uped --ttl 30d --min-free 1G --max-file-size 20G --chunk-size 8M"
ok "uped starts with the flags the init script builds from conf.d (defaults and edited)"

inst --binary "$WORK/v1/uped" --prefix "$P" || fail "re-run failed"
says "already up to date"
ok "re-run with the same binary: already up to date"

echo "# my setting" >>"$CONF"
echo "# stale line" >>"$INITD"
inst --binary "$WORK/v2/uped" --prefix "$P" || fail "upgrade failed"
says "Upgrading uped v0.0.1-test to v0.0.2-test"
[ "$(version_of "$BIN")" = v0.0.2-test ] || fail "upgraded version $(version_of "$BIN")"
grep -q "# my setting" "$CONF" || fail "upgrade overwrote conf.d"
grep -q 'UPED_TTL="30d"' "$CONF" || fail "upgrade overwrote conf.d"
cmp -s "$INITD" packaging/openrc/uped.initd || fail "upgrade did not rewrite the init script"
must_not_exist "$BIN.new" "$INITD.new"
ok "upgrade replaces the binary, rewrites the init script and keeps conf.d"

# ---------- release tarballs and checksums

inst --binary "$WORK/rel/latest/download/$ASSET" --prefix "$WORK/p2" || fail "install from tarball failed"
[ "$(version_of "$WORK/p2/usr/local/bin/uped")" = v0.0.2-test ] || fail "tarball install has the wrong version"
ok "--binary $ASSET with its checksums.txt"

mkdir -p "$WORK/bad"
cp "$WORK/rel/latest/download/$ASSET" "$WORK/bad/"
echo "0000000000000000000000000000000000000000000000000000000000000000  $ASSET" >"$WORK/bad/checksums.txt"
if inst --binary "$WORK/bad/$ASSET" --prefix "$WORK/p3"; then fail "a tarball with a wrong checksum was installed"; fi
says "checksum mismatch"
must_not_exist "$WORK/p3/usr/local/bin/uped"
ok "a checksum mismatch stops the install before anything is written"

# ---------- downloads from a stand-in release server

if command -v python3 >/dev/null 2>&1; then
  python3 -u -m http.server 0 --bind 127.0.0.1 --directory "$WORK/rel" >"$WORK/http.log" 2>&1 &
  PIDS="$PIDS $!"
  PORT=
  i=0
  while [ -z "$PORT" ] && [ $i -lt 100 ]; do
    PORT=$(sed -n 's/.* port \([0-9][0-9]*\).*/\1/p' "$WORK/http.log" | head -n 1)
    i=$((i + 1))
    sleep 0.1
  done
  [ -n "$PORT" ] || fail "the test HTTP server did not start: $(cat "$WORK/http.log")"
  export UPED_RELEASES_URL="http://127.0.0.1:$PORT"

  inst --prefix "$WORK/p4" || fail "download of the latest release failed"
  says "Downloading the latest uped for $ARCH"
  [ "$(version_of "$WORK/p4/usr/local/bin/uped")" = v0.0.2-test ] || fail "latest download has the wrong version"
  ok "downloads releases/latest/download/$ASSET and checks it"

  inst --version 0.0.1-test --prefix "$WORK/p5" || fail "download of a pinned version failed"
  says "Downloading uped v0.0.1-test"
  [ "$(version_of "$WORK/p5/usr/local/bin/uped")" = v0.0.1-test ] || fail "pinned download has the wrong version"
  ok "--version 0.0.1-test downloads releases/download/v0.0.1-test/"

  if inst --version v9.9.9 --prefix "$WORK/p6"; then fail "a missing release was installed"; fi
  says "download failed"
  ok "a missing release fails with a message"

  if command -v busybox >/dev/null 2>&1; then
    # Only busybox applets on PATH: no curl, so the installer uses wget.
    mkdir -p "$WORK/bb"
    busybox --install -s "$WORK/bb"
    must_not_exist "$WORK/bb/curl" "$WORK/bb/bash"
    env -i PATH="$WORK/bb" HOME="$WORK" UPED_RELEASES_URL="$UPED_RELEASES_URL" \
      "$WORK/bb/sh" ./install.sh --prefix "$WORK/p7" >"$WORK/out" 2>&1 || fail "install under busybox failed"
    [ "$(version_of "$WORK/p7/usr/local/bin/uped")" = v0.0.2-test ] || fail "busybox install has the wrong version"
    cmp -s "$WORK/p7/etc/init.d/uped" packaging/openrc/uped.initd || fail "busybox install wrote a different init script"
    env -i PATH="$WORK/bb" HOME="$WORK" "$WORK/bb/sh" ./install.sh --purge --prefix "$WORK/p7" >"$WORK/out" 2>&1 ||
      fail "purge under busybox failed"
    must_not_exist "$WORK/p7/usr/local/bin/uped" "$WORK/p7/etc/conf.d/uped" "$WORK/p7/var/lib/uped"
    ok "install (via wget) and purge with busybox tools only"
  else
    skip "busybox tests: busybox is not installed"
  fi
  unset UPED_RELEASES_URL
else
  skip "download tests: python3 is not installed"
fi

# ---------- uninstall and purge

echo "keep me" >"$P/var/lib/uped/upload.txt"
# shellcheck disable=SC2002 # the pipe is the point: this is how users run it
cat install.sh | sh -s -- --uninstall --prefix "$P" >"$WORK/out" 2>&1 || fail "piped --uninstall failed"
says "Kept your settings"
must_not_exist "$BIN" "$INITD"
must_exist "$CONF" "$P/var/lib/uped/upload.txt" "$P/var/log/uped"
ok "curl ... | sh -s -- --uninstall removes binary and init script, keeps conf.d and files"

inst --purge --prefix "$P" || fail "--purge failed"
must_not_exist "$CONF" "$P/var/lib/uped" "$P/var/log/uped"
ok "--purge removes conf.d, files and logs"

inst --binary "$WORK/v1/uped" --prefix "$WORK/p8" || fail "install for the custom data dir test failed"
sed -i 's|^UPED_DATA_DIR=.*|UPED_DATA_DIR="/srv/shared"|' "$WORK/p8/etc/conf.d/uped"
inst --purge --prefix "$WORK/p8" || fail "--purge with a custom data dir failed"
says "/srv/shared was left in place"
ok "--purge leaves a data folder configured elsewhere"

echo "INSTALL TEST OK"
