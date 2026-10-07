#!/bin/sh
# Runs install.sh for real, as root on Alpine with OpenRC starting uped
# under supervise-daemon, as in a Proxmox LXC. It changes the system, so run
# it only in a throwaway container. CI does:
#
#   make cross ARCHES=amd64
#   docker run --rm -v "$PWD:/src" -w /src alpine:3.22 sh scripts/test-install-alpine.sh dist
#
# DIST holds uped_linux_<arch>.tar.gz and checksums.txt, as `make cross` writes them.
set -eu

if [ ! -f /etc/alpine-release ] || [ "$(id -u)" -ne 0 ]; then
  echo "test-install-alpine: run this as root in a throwaway Alpine container" >&2
  exit 1
fi
DIST=${1:-dist}
case $(uname -m) in
  x86_64) ARCH=amd64 ;;
  aarch64) ARCH=arm64 ;;
  *)
    echo "test-install-alpine: unsupported machine $(uname -m)" >&2
    exit 1
    ;;
esac
TARBALL=$DIST/uped_linux_$ARCH.tar.gz
OUT=/tmp/install.out

fail() {
  echo "test-install-alpine: FAIL: $*" >&2
  if [ -f "$OUT" ]; then
    echo "--- installer output" >&2
    cat "$OUT" >&2
  fi
  if [ -f /var/log/uped/uped.log ]; then
    echo "--- /var/log/uped/uped.log" >&2
    tail -n 30 /var/log/uped/uped.log >&2
  fi
  exit 1
}
ok() { echo "ok   $*"; }
inst() { sh ./install.sh "$@" >"$OUT" 2>&1; }
says() { grep -q -- "$1" "$OUT" || fail "installer output lacks \"$1\""; }
healthz() { wget -q -T 3 -O - "http://127.0.0.1:$1/healthz" 2>/dev/null; }
# wait_for CMD... retries a command for up to 15 s.
wait_for() {
  i=0
  until "$@"; do
    i=$((i + 1))
    [ $i -lt 30 ] || return 1
    sleep 0.5
  done
}
is_ok() { [ "$(healthz "$1")" = ok ]; }
is_down() { ! healthz "$1" >/dev/null; }
must_exist() { for f in "$@"; do [ -e "$f" ] || fail "$f is missing"; done; }
must_not_exist() { for f in "$@"; do [ ! -e "$f" ] || fail "$f should not exist"; done; }

echo "Alpine $(cat /etc/alpine-release), installing openrc"
apk add --no-cache openrc >/dev/null
# A container is not booted by OpenRC: mark it as booted, and let loopback
# provide the "net" that the networking service provides in an LXC.
mkdir -p /run/openrc
touch /run/openrc/softlevel
sed -i -e 's/^#*rc_provide=.*/rc_provide="loopback net"/' -e 's/^#*rc_sys=.*/rc_sys="docker"/' /etc/rc.conf
grep -q '^rc_provide="loopback net"' /etc/rc.conf || echo 'rc_provide="loopback net"' >>/etc/rc.conf
command -v curl >/dev/null 2>&1 && fail "curl is installed; this test should use busybox wget like a fresh Alpine"

# ---------- install

inst --binary "$TARBALL" || fail "install failed"
says "Installing uped"
says "is running. Open this on any device on your network"
says "http://"
cat "$OUT"
is_ok 8080 || fail "/healthz does not answer ok"
rc-service uped status 2>&1 | grep -q started || fail "rc-service uped status is not started"
rc-update show default | grep -q uped || fail "uped is not in the default runlevel"
ok "install as root: service enabled, started and healthy"

id uped >/dev/null 2>&1 || fail "no uped user"
[ "$(stat -c %U:%G:%a /var/lib/uped)" = uped:uped:750 ] || fail "/var/lib/uped is $(stat -c %U:%G:%a /var/lib/uped)"
[ "$(stat -c %U:%G:%a /var/log/uped)" = uped:uped:750 ] || fail "/var/log/uped is $(stat -c %U:%G:%a /var/log/uped)"
[ "$(stat -c %a /usr/local/bin/uped)" = 755 ] || fail "binary mode"
runs_as=$(ps -o user,comm | awk '$2 == "uped" { print $1; exit }')
[ "$runs_as" = uped ] || fail "uped runs as \"$runs_as\", not uped"
grep -q "uped starting" /var/log/uped/uped.log || fail "no startup line in /var/log/uped/uped.log"
ok "uped runs as the uped user; data and log dirs belong to it; it logs to /var/log/uped/uped.log"

wget -q -O /dev/null --header "Content-Type: application/json" --post-data '{"dir":"","text":"hello from alpine"}' \
  http://127.0.0.1:8080/api/text || fail "POST /api/text failed"
[ "$(stat -c %U /var/lib/uped/files/hello-from-alpine.txt)" = uped ] || fail "the note is not owned by uped"
[ "$(wget -q -O - http://127.0.0.1:8080/d/hello-from-alpine.txt)" = "hello from alpine" ] || fail "download differs"
ok "an upload is stored under /var/lib/uped/files and downloads again"

# ---------- re-run, settings, respawn

inst --binary "$TARBALL" || fail "re-run failed"
says "already up to date"
is_ok 8080 || fail "not healthy after re-run"
ok "re-run: already up to date, still running"

sed -i 's/^UPED_LISTEN=.*/UPED_LISTEN=":9090"/' /etc/conf.d/uped
rc-service uped restart >/dev/null 2>&1 || fail "restart failed"
wait_for is_ok 9090 || fail "not answering on :9090 after changing UPED_LISTEN"
is_down 8080 || fail "still answering on :8080"
inst --binary "$TARBALL" || fail "re-run with a changed port failed"
says ":9090/"
grep -q 'UPED_LISTEN=":9090"' /etc/conf.d/uped || fail "the installer overwrote /etc/conf.d/uped"
ok "UPED_LISTEN in /etc/conf.d/uped takes effect and survives a re-run"

pid=$(ps -o pid,comm | awk '$2 == "uped" { print $1; exit }')
kill -9 "$pid"
wait_for is_down 9090 || fail "uped did not die"
wait_for is_ok 9090 || fail "supervise-daemon did not restart uped"
ok "supervise-daemon restarts uped after a crash"

# ---------- uninstall and purge

inst --uninstall || fail "--uninstall failed"
wait_for is_down 9090 || fail "still running after --uninstall"
must_not_exist /usr/local/bin/uped /etc/init.d/uped
must_exist /etc/conf.d/uped /var/lib/uped/files/hello-from-alpine.txt
rc-update show default | grep -q uped && fail "uped is still in the default runlevel"
id uped >/dev/null 2>&1 || fail "--uninstall removed the user"
ok "--uninstall stops and removes the service, keeps settings, files and user"

inst --purge || fail "--purge failed"
must_not_exist /etc/conf.d/uped /var/lib/uped /var/log/uped
id uped >/dev/null 2>&1 && fail "--purge left the uped user"
grep -q '^uped:' /etc/group && fail "--purge left the uped group"
ok "--purge removes settings, files, logs, user and group"

echo "ALPINE INSTALL TEST OK"
