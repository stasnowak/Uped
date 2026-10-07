#!/bin/sh
# Install or upgrade uped, the LAN file drop, on Alpine Linux with OpenRC
# (for example an Alpine container on Proxmox). Run it as root:
#
#   wget -qO- https://raw.githubusercontent.com/stasnowak/Uped/main/install.sh | sh
#   curl -fsSL https://raw.githubusercontent.com/stasnowak/Uped/main/install.sh | sh
#
# Run it again to upgrade. Options go after "sh -s --", for example
#   wget -qO- .../install.sh | sh -s -- --uninstall
# See --help for all of them.
#
# POSIX sh only: Alpine comes with busybox ash and wget, and without curl.
# Everything runs from main on the last line, so a partly downloaded script
# does nothing.
set -eu

REPO="stasnowak/Uped"
RELEASES_URL=${UPED_RELEASES_URL:-https://github.com/$REPO/releases}
RAW_URL="https://raw.githubusercontent.com/$REPO/main/install.sh"

usage() {
  cat <<EOF
Install or upgrade uped on Alpine Linux (OpenRC). Run as root.

  wget -qO- $RAW_URL | sh
  wget -qO- $RAW_URL | sh -s -- [options]

Options:
  --version vX.Y.Z  install this release instead of the latest (or set UPED_VERSION)
  --uninstall       stop and remove uped; keep settings and uploaded files
  --purge           remove uped, its settings, uploaded files, logs and user
  --no-service      install files only; do not enable or start the service
  --binary PATH     use this uped binary or uped_linux_<arch>.tar.gz instead of
                    downloading (a checksums.txt next to a tarball is checked)
  --prefix DIR      install under DIR instead of /; implies --no-service, needs
                    no root and creates no user (for testing and packaging)
  -h, --help        show this help

Installs /usr/local/bin/uped, /etc/init.d/uped and /etc/conf.d/uped (settings,
kept on upgrade). Uploads go to /var/lib/uped, logs to /var/log/uped/uped.log.
EOF
}

say() { printf '%s\n' "$*"; }
warn() { printf 'uped install: warning: %s\n' "$*" >&2; }
fatal() {
  printf 'uped install: %s\n' "$*" >&2
  exit 1
}
usage_error() {
  printf 'uped install: %s (see --help)\n' "$*" >&2
  exit 2
}

# dl URL FILE downloads with curl if present, else wget (busybox on Alpine).
dl() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 -o "$2" "$1" || fatal "download failed: $1"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1" || fatal "download failed: $1 (if this is a TLS error, run: apk add ca-certificates ssl_client)"
  else
    fatal "neither curl nor wget is installed (apk add curl)"
  fi
}

sha256() { sha256sum "$1" | awk '{print $1}'; }

detect_arch() {
  case $(uname -m) in
    x86_64 | amd64) ARCH=amd64 ;;
    aarch64 | arm64) ARCH=arm64 ;;
    *) fatal "unsupported CPU architecture $(uname -m); uped releases exist for amd64 and arm64" ;;
  esac
}

check_system() {
  [ -n "$PREFIX" ] && return 0
  [ "$(id -u)" -eq 0 ] || fatal "run this as root (in the Proxmox console of a container you already are)"
  [ -f /etc/alpine-release ] || fatal "this installer is for Alpine Linux; elsewhere use the Docker image or run the uped binary from the release page yourself"
  if [ "$NO_SERVICE" -eq 0 ] && ! command -v rc-service >/dev/null 2>&1; then
    fatal "OpenRC (rc-service) was not found; install it with apk add openrc, or use --no-service"
  fi
}

# fetch_binary puts the new uped binary at $TMP/uped.
fetch_binary() {
  ASSET="uped_linux_$ARCH.tar.gz"
  if [ -n "$BINARY" ]; then
    [ -f "$BINARY" ] || fatal "--binary: $BINARY does not exist"
    case $BINARY in
      *.tar.gz | *.tgz)
        ASSET=$(basename "$BINARY")
        cp "$BINARY" "$TMP/$ASSET"
        if [ -f "$(dirname "$BINARY")/checksums.txt" ]; then
          cp "$(dirname "$BINARY")/checksums.txt" "$TMP/checksums.txt"
          verify_checksum
        fi
        tar -xzf "$TMP/$ASSET" -C "$TMP" uped || fatal "$BINARY does not contain a uped binary"
        ;;
      *) cp "$BINARY" "$TMP/uped" ;;
    esac
  else
    if [ -n "$VERSION" ]; then
      base="$RELEASES_URL/download/$VERSION"
      say "Downloading uped $VERSION for $ARCH..."
    else
      base="$RELEASES_URL/latest/download"
      say "Downloading the latest uped for $ARCH..."
    fi
    dl "$base/$ASSET" "$TMP/$ASSET"
    dl "$base/checksums.txt" "$TMP/checksums.txt"
    verify_checksum
    tar -xzf "$TMP/$ASSET" -C "$TMP" uped || fatal "$ASSET does not contain a uped binary"
  fi
  chmod 755 "$TMP/uped"
  NEW_VERSION=$("$TMP/uped" --version 2>/dev/null | awk '{print $2}')
  [ -n "$NEW_VERSION" ] || fatal "the downloaded uped does not run on this machine ($ARCH)"
}

verify_checksum() {
  want=$(awk -v a="$ASSET" '$2 == a || $2 == "*" a {print $1; exit}' "$TMP/checksums.txt")
  [ -n "$want" ] || fatal "checksums.txt has no entry for $ASSET"
  [ "$(sha256 "$TMP/$ASSET")" = "$want" ] || fatal "checksum mismatch for $ASSET: the download is damaged or was tampered with; nothing was installed"
}

ensure_user() {
  [ -n "$PREFIX" ] && return 0
  grep -q '^uped:' /etc/group || addgroup -S uped
  id -u uped >/dev/null 2>&1 || adduser -S -D -H -h /var/lib/uped -s /sbin/nologin -G uped -g "uped file drop" uped
}

# make_dir DIR creates DIR (mode 0750) and gives it, but not its contents, to uped.
make_dir() {
  mkdir -p "$1"
  chmod 0750 "$1"
  [ -n "$PREFIX" ] || chown uped:uped "$1" || warn "could not give $1 to the uped user"
}

# conf_value NAME DEFAULT prints a setting from the conf.d file, or DEFAULT.
conf_value() {
  # shellcheck disable=SC1090 # the file is ours: written below, edited by root
  (
    if [ -f "$CONF" ]; then . "$CONF" >/dev/null 2>&1; fi
    eval "printf '%s' \"\${$1:-$2}\""
  ) || printf '%s' "$2"
}

install_files() {
  BIN="$PREFIX/usr/local/bin/uped"
  mkdir -p "$PREFIX/usr/local/bin" "$PREFIX/etc/init.d" "$PREFIX/etc/conf.d"
  CHANGED=0

  if [ -f "$BIN" ] && [ "$(sha256 "$BIN")" = "$(sha256 "$TMP/uped")" ]; then
    say "uped $NEW_VERSION is already up to date."
  else
    if [ -f "$BIN" ]; then
      say "Upgrading uped $("$BIN" --version 2>/dev/null | awk '{print $2}') to $NEW_VERSION..."
    else
      say "Installing uped $NEW_VERSION..."
    fi
    # Copy next to the target first so the final mv is an atomic rename.
    cp "$TMP/uped" "$BIN.new"
    chmod 755 "$BIN.new"
    mv -f "$BIN.new" "$BIN"
    CHANGED=1
  fi

  ensure_user
  make_dir "$PREFIX/var/lib/uped"
  make_dir "$PREFIX/var/log/uped"

  # The init script is always rewritten so fixes reach existing installs.
  write_initd >"$TMP/uped.initd"
  if ! cmp -s "$TMP/uped.initd" "$INITD"; then
    cp "$TMP/uped.initd" "$INITD.new"
    chmod 755 "$INITD.new"
    mv -f "$INITD.new" "$INITD"
    CHANGED=1
  fi
  # Settings are written once and then belong to the user.
  if [ ! -f "$CONF" ]; then
    write_confd >"$CONF"
    chmod 644 "$CONF"
  fi
}

# health_check waits up to 10 s for /healthz to answer.
health_check() {
  listen=$(conf_value UPED_LISTEN :8080)
  port=${listen##*:}
  host=${listen%:*}
  case $host in "" | 0.0.0.0 | "[::]" | "[]") host=127.0.0.1 ;; esac
  i=0
  while [ $i -lt 10 ]; do
    if command -v curl >/dev/null 2>&1; then
      curl -fsS -m 2 -o /dev/null "http://$host:$port/healthz" 2>/dev/null && return 0
    else
      wget -q -T 2 -O /dev/null "http://$host:$port/healthz" 2>/dev/null && return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}

start_service() {
  if [ "$NO_SERVICE" -eq 1 ]; then
    say "Not enabling or starting the service (--no-service)."
    return 0
  fi
  rc-update add uped default >/dev/null 2>&1 || warn "could not add uped to the default runlevel"
  if [ "$CHANGED" -eq 1 ]; then
    rc-service uped restart || fatal "uped did not start; see /var/log/uped/uped.log"
  elif ! rc-service uped status >/dev/null 2>&1; then
    rc-service uped start || fatal "uped did not start; see /var/log/uped/uped.log"
  fi
  if ! health_check; then
    warn "uped does not answer yet; check rc-service uped status and /var/log/uped/uped.log"
    tail -n 20 /var/log/uped/uped.log >&2 2>/dev/null || true
    exit 1
  fi
}

lan_ip() {
  ip=$(ip -4 route get 1 2>/dev/null | awk '{for (i = 1; i <= NF; i++) if ($i == "src") { print $(i + 1); exit }}')
  if [ -z "$ip" ]; then
    ip=$(hostname -i 2>/dev/null | awk '{print $1}')
    case $ip in 127.* | "") ip="<this-machine's-ip>" ;; esac
  fi
  printf '%s' "$ip"
}

banner() {
  if [ -n "$PREFIX" ]; then
    say "Installed uped $NEW_VERSION under $PREFIX."
    return 0
  fi
  listen=$(conf_value UPED_LISTEN :8080)
  host=${listen%:*}
  case $host in "" | 0.0.0.0 | "[::]" | "[]") host=$(lan_ip) ;; esac
  say ""
  if [ "$NO_SERVICE" -eq 1 ]; then
    say "uped $NEW_VERSION is installed. Start it with: rc-update add uped default && rc-service uped start"
    say "Then open this on any device on your network:"
  else
    say "uped $NEW_VERSION is running. Open this on any device on your network:"
  fi
  say ""
  say "    http://$host:${listen##*:}/"
  say ""
  say "Settings: /etc/conf.d/uped (then rc-service uped restart)"
  say "Files:    $(conf_value UPED_DATA_DIR /var/lib/uped), deleted after $(conf_value UPED_TTL 168h)"
  say "Logs:     /var/log/uped/uped.log"
  say "Upgrade:  run this installer again. Remove: add --uninstall (keeps files) or --purge."
}

uninstall() {
  if [ "$NO_SERVICE" -eq 0 ]; then
    rc-service uped stop >/dev/null 2>&1 || true
    rc-update del uped default >/dev/null 2>&1 || true
  fi
  rm -f "$PREFIX/usr/local/bin/uped" "$INITD"
}

purge() {
  data=$(conf_value UPED_DATA_DIR /var/lib/uped)
  uninstall
  rm -f "$CONF"
  rm -rf "$PREFIX/var/log/uped"
  # Remove the default data folder, even when it is a mount point (then
  # only its contents can go). A folder configured elsewhere may hold other
  # things, so it is left for the user to delete.
  d="$PREFIX/var/lib/uped"
  if [ -d "$d" ]; then
    rm -rf "${d:?}" 2>/dev/null || rm -rf "${d:?}"/* "$d"/.[!.]* "$d"/..?* 2>/dev/null || true
  fi
  if [ "$data" != /var/lib/uped ]; then
    warn "your data folder $data was left in place; delete it yourself if you no longer need it"
  fi
  if [ -z "$PREFIX" ]; then
    deluser uped 2>/dev/null || true
    delgroup uped 2>/dev/null || true
  fi
}

main() {
  ACTION=install
  VERSION=${UPED_VERSION:-}
  BINARY=
  PREFIX=
  NO_SERVICE=0
  while [ $# -gt 0 ]; do
    case $1 in
      --uninstall) ACTION=uninstall ;;
      --purge) ACTION=purge ;;
      --no-service) NO_SERVICE=1 ;;
      --version | --binary | --prefix)
        [ $# -ge 2 ] || usage_error "$1 needs a value"
        case $1 in
          --version) VERSION=$2 ;;
          --binary) BINARY=$2 ;;
          --prefix) PREFIX=$2 ;;
        esac
        shift
        ;;
      --version=*) VERSION=${1#*=} ;;
      --binary=*) BINARY=${1#*=} ;;
      --prefix=*) PREFIX=${1#*=} ;;
      -h | --help)
        usage
        exit 0
        ;;
      *) usage_error "unknown option: $1" ;;
    esac
    shift
  done
  case $VERSION in "" | v*) ;; *) VERSION=v$VERSION ;; esac
  if [ -n "$PREFIX" ]; then
    case $PREFIX in /*) ;; *) PREFIX=$(pwd)/$PREFIX ;; esac
    PREFIX=${PREFIX%/}
    [ -n "$PREFIX" ] || usage_error "--prefix must not be /"
    NO_SERVICE=1
  fi
  INITD="$PREFIX/etc/init.d/uped"
  CONF="$PREFIX/etc/conf.d/uped"

  check_system
  case $ACTION in
    uninstall)
      uninstall
      say "Removed uped. Kept your settings ($CONF) and files ($(conf_value UPED_DATA_DIR /var/lib/uped))."
      say "To remove those too, run the installer with --purge."
      return 0
      ;;
    purge)
      purge
      say "Removed uped with its settings, files, logs and user."
      return 0
      ;;
  esac

  detect_arch
  TMP=$(mktemp -d)
  trap 'rm -rf "$TMP"' EXIT
  trap 'exit 1' INT TERM
  fetch_binary
  install_files
  start_service
  banner
}

# The two files below are also in the repository as packaging/openrc/uped.initd
# and packaging/openrc/uped.confd; scripts/test-install.sh checks they match.

write_initd() {
  cat <<'EOF'
#!/sbin/openrc-run
# shellcheck shell=sh disable=SC2034 # the variables are read by openrc-run
# uped: LAN file drop, https://github.com/stasnowak/Uped
# Written by install.sh, which replaces this file on every run.
# Change settings in /etc/conf.d/uped instead, then: rc-service uped restart

name="uped"
description="LAN file drop: share files between the devices on your network"

supervisor=supervise-daemon
command="/usr/local/bin/uped"
# conf.d variables are not exported, so they are passed as flags.
command_args="--listen ${UPED_LISTEN:-:8080} --data ${UPED_DATA_DIR:-/var/lib/uped} --ttl ${UPED_TTL:-168h} --min-free ${UPED_MIN_FREE:-1G} --max-file-size ${UPED_MAX_FILE_SIZE:-0} ${UPED_EXTRA_ARGS:-}"
command_user="uped:uped"
directory="${UPED_DATA_DIR:-/var/lib/uped}"
pidfile="/run/uped.pid"
output_log="/var/log/uped/uped.log"
error_log="/var/log/uped/uped.log"
respawn_delay=5
respawn_max=0
umask=027

depend() {
	need net
	after firewall
	use dns logger
}

start_pre() {
	checkpath -d -m 0750 -o uped:uped "${UPED_DATA_DIR:-/var/lib/uped}" /var/log/uped
}
EOF
}

write_confd() {
  cat <<'EOF'
# Settings for the uped service. install.sh writes this file only when it
# is missing, so your changes survive upgrades. Apply them with:
#   rc-service uped restart
# Values must not contain spaces.

# Address and port to listen on. ":8080" means port 8080 on every interface.
UPED_LISTEN=":8080"

# Where uploads are stored. The service creates the folder and gives it to
# the uped user. Point it at a bigger disk or mount point if you like.
UPED_DATA_DIR="/var/lib/uped"

# How long items stay before they are deleted: 168h (7 days), 7d, 24h, 30d.
# 0 keeps them until someone deletes them.
UPED_TTL="168h"

# Refuse uploads that would leave less free disk space than this: 1G, 500M.
UPED_MIN_FREE="1G"

# Largest file accepted, for example 4G; 0 means no limit.
UPED_MAX_FILE_SIZE="0"

# Any other uped flags, for example "--chunk-size 8M". See: uped --help
UPED_EXTRA_ARGS=""
EOF
}

main "$@"
