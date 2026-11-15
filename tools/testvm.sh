#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Throwaway OPNsense test VM for contract tests (QEMU/KVM, official nano image).
#
#   tools/testvm.sh prepare 26.7   download + verify image, build read-only qcow2 base
#   tools/testvm.sh image-version 26.7
#                                  image release of a series on the mirror (26.1 -> 26.1.6)
#   tools/testvm.sh up 26.7        boot a fresh overlay (previous one is discarded)
#   tools/testvm.sh apikey         create a throwaway API key (factory login root/opnsense)
#   tools/testvm.sh plugins 26.7 [os-a os-b ...]
#                                  install plugins (default: all with an API
#                                  in the IR) through the package proxy
#   tools/testvm.sh down           stop the VM (and proxy), delete the overlay
#   tools/testvm.sh update 26.7.5  update the running VM to exactly that
#                                  release (through the package proxy)
#   tools/testvm.sh start 26.7 [--release 26.7.5|latest] [--plugins all|"os-a os-b"]
#                                  up + apikey [+ update] [+ plugins], leaves
#                                  the VM running and writes .cache/vm/state.env
#                                  (url, keyfile, version, plugins); used by
#                                  the action .github/actions/testvm
#   tools/testvm.sh contract dist/26.7.5/opnsense-26.7.5.ir.json
#                                  contract tests against the running VM
#   tools/testvm.sh verify 26.7 [--release 26.7.5] [--plugins]
#                                  start + contract tests + down
#
# Isolation: user-mode networking with restrict=on (no route to the LAN or the
# internet). The only way in is a port forward on 127.0.0.1:${PORT:-18443} to
# the VM's LAN (192.168.1.1:443). Never point the contract tests elsewhere.
# The only way out is http://10.99.0.200 on the WAN side, forwarded (socat per
# connection) to the package proxy on 127.0.0.1:${PROXY_PORT:-18080}, which
# serves nothing but the signed package snapshot of the VM's release.
#
# Image verification (see pkg.opnsense.org/releases/<series>/README):
#   1. SHA-256 of the .bz2 matches the mirror's checksum file AND the release
#      announcement in github.com/opnsense/changelog (second source)
#   2. OpenSSL signature of the unpacked image against the release key; the
#      key must match the one in the series announcement (<series>/<series>;
#      patch announcements such as 26.1/26.1.6 only carry checksums)
#
# Acceleration: KVM when /dev/kvm is accessible. Without it the run fails,
# unless TESTVM_ACCEL=tcg allows software emulation (several times slower;
# all waits are then stretched by TESTVM_SLOW, default 6). CI sets this only
# as an explicit fallback and reports which one was used.
# TESTVM_DROP_RAW=1 deletes the downloaded and unpacked image after the
# qcow2 base is built (disk space on CI runners).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VM="$ROOT/.cache/vm"
IMG="$ROOT/.cache/images"
PORT="${PORT:-18443}"
PROXY_PORT="${PROXY_PORT:-18080}"
PROXY_IP="10.99.0.200"   # address of the package proxy as seen from the VM (WAN side)
URL="https://127.0.0.1:$PORT"
MIRROR="https://pkg.opnsense.org/releases"
CHANGELOG="https://raw.githubusercontent.com/opnsense/changelog/master/community"
ACCEL="${TESTVM_ACCEL:-kvm}"
case "$ACCEL" in
  kvm) SLOW="${TESTVM_SLOW:-1}" ;;
  tcg) SLOW="${TESTVM_SLOW:-6}" ;;
  *) echo "error: TESTVM_ACCEL must be kvm or tcg" >&2; exit 1 ;;
esac
# waits N: number of wait iterations, stretched for software emulation
waits() { echo $(($1 * SLOW)); }

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 not installed"; }

# image version of a series: images may be built on a patch release
# (26.1 -> 26.1.6); the key is always OPNsense-<series>.pub
image_version() {
  curl -fsS -m 30 "$MIRROR/$1/" | grep -oE "OPNsense-$1(\.[0-9]+)?-nano-amd64\.img\.bz2\"" \
    | head -1 | sed -E "s/^OPNsense-(.*)-nano-amd64.*/\1/"
}

# release running in the VM built from base-<series>.qcow2, or the release
# it was updated to (TARGET)
vm_version() {
  if [ -n "${TARGET:-}" ]; then echo "$TARGET"; return; fi
  cat "$VM/base-$1.version" 2>/dev/null || echo "$1"
}

prepare() {
  local series="$1" d="$IMG/$1" v n
  need curl; need openssl; need bunzip2; need qemu-img
  v=$(image_version "$series"); [ -n "$v" ] || die "no nano image for $series"
  n="OPNsense-$v-nano-amd64.img"
  echo "series $series: image $v"
  mkdir -p "$d" "$VM"
  for f in "OPNsense-$v-checksums-amd64.sha256" "$n.sig" "OPNsense-$series.pub"; do
    curl -fsS -m 60 -o "$d/$f" "$MIRROR/$series/$f"
  done
  [ -f "$d/$n.bz2" ] || curl -fsS -m 1800 -o "$d/$n.bz2" "$MIRROR/$series/$n.bz2"

  local got want announced
  got=$(sha256sum "$d/$n.bz2" | cut -d' ' -f1)
  want=$(grep "($n.bz2)" "$d/OPNsense-$v-checksums-amd64.sha256" | awk '{print $NF}')
  announced=$(curl -fsS -m 30 "$CHANGELOG/$series/$v" | grep "($n.bz2)" | awk '{print $NF}')
  [ "$got" = "$want" ] || die "checksum mismatch against mirror"
  [ "$got" = "$announced" ] || die "checksum mismatch against changelog announcement"
  echo "checksum ok (mirror + changelog announcement)"

  diff <(sed -n '/BEGIN PUBLIC KEY/,/END PUBLIC KEY/p' "$d/OPNsense-$series.pub") \
       <(curl -fsS -m 30 "$CHANGELOG/$series/$series" | sed -n '/BEGIN PUBLIC KEY/,/END PUBLIC KEY/p' | sed 's/^# *//') \
       >/dev/null || die "release key differs from the changelog announcement"
  [ -f "$d/$n" ] || bunzip2 -k "$d/$n.bz2"
  openssl base64 -d -in "$d/$n.sig" -out "$d/$n.sig.bin"
  openssl dgst -sha256 -verify "$d/OPNsense-$series.pub" -signature "$d/$n.sig.bin" "$d/$n" >/dev/null \
    || die "signature invalid"
  echo "signature ok (release key matches announcement)"

  rm -f "$VM/base-$series.qcow2"
  qemu-img convert -O qcow2 "$d/$n" "$VM/base-$series.qcow2"
  chmod a-w "$VM/base-$series.qcow2"
  echo "$v" > "$VM/base-$series.version"
  if [ "${TESTVM_DROP_RAW:-}" = 1 ]; then rm -f "$d/$n" "$d/$n.bz2"; fi
  echo "base image: $VM/base-$series.qcow2 ($v)"
}

running() { [ -f "$VM/qemu.pid" ] && kill -0 "$(cat "$VM/qemu.pid")" 2>/dev/null; }

up() {
  local series="$1"
  [ -f "$VM/base-$series.qcow2" ] || die "run: $0 prepare $series"
  running && die "VM already running (pid $(cat "$VM/qemu.pid")); run: $0 down"
  need qemu-system-x86_64
  local accel=(-enable-kvm -cpu host)
  if [ "$ACCEL" = tcg ]; then
    accel=(-accel "tcg,thread=multi" -cpu max)
    echo "software emulation (TCG), waits x$SLOW"
  else
    [ -r /dev/kvm ] && [ -w /dev/kvm ] || die "/dev/kvm not accessible (TESTVM_ACCEL=tcg allows software emulation)"
  fi
  rm -f "$VM/run.qcow2" "$VM/console.log" "$VM/apikey.json" "$VM/cookies.txt"
  # 24G: room for plugins; the nano image grows to the disk size on first boot
  qemu-img create -q -f qcow2 -F qcow2 -b "base-$series.qcow2" "$VM/run.qcow2" 24G
  # vtnet0 = LAN (192.168.1.1), vtnet1 = WAN, vtnet2/3 = spare NICs for
  # interface tests; all networks are isolated (restrict=on)
  qemu-system-x86_64 -name "opnsense-test-$series" "${accel[@]}" -m 2048 -smp "${TESTVM_CPUS:-2}" \
    -drive "file=$VM/run.qcow2,if=virtio,format=qcow2" \
    -netdev "user,id=lan,net=192.168.1.0/24,host=192.168.1.2,dhcpstart=192.168.1.100,restrict=on,hostfwd=tcp:127.0.0.1:$PORT-192.168.1.1:443" \
    -device virtio-net-pci,netdev=lan \
    -netdev "user,id=wan,net=10.99.0.0/24,restrict=on,guestfwd=tcp:$PROXY_IP:80-cmd:socat - TCP:127.0.0.1:$PROXY_PORT" \
    -device virtio-net-pci,netdev=wan \
    -netdev user,id=opt1,net=10.99.1.0/24,restrict=on -device virtio-net-pci,netdev=opt1 \
    -netdev user,id=opt2,net=10.99.2.0/24,restrict=on -device virtio-net-pci,netdev=opt2 \
    -display none -serial "file:$VM/console.log" -monitor none \
    -daemonize -pidfile "$VM/qemu.pid"
  echo "booting (console: $VM/console.log) ..."
  for _ in $(seq 1 "$(waits 120)"); do
    if curl -sk -m 3 -o /dev/null "$URL/"; then echo "web GUI up: $URL"; return 0; fi
    running || die "VM exited; see $VM/console.log"
    sleep 3
  done
  die "web GUI did not come up within $((6 * SLOW)) minutes"
}

apikey() {
  local j="$VM/cookies.txt" page inp name val tok
  rm -f "$j"
  page=$(curl -sk -c "$j" -b "$j" "$URL/")
  inp=$(grep -oE '<input type="hidden" name="[^"]+" value="[^"]+"' <<<"$page" | head -1)
  name=$(sed -E 's/.*name="([^"]+)".*/\1/' <<<"$inp")
  val=$(sed -E 's/.*value="([^"]+)".*/\1/' <<<"$inp")
  # factory credentials of a fresh, isolated test image only
  curl -sk -c "$j" -b "$j" -o /dev/null --data-urlencode usernamefld=root \
    --data-urlencode passwordfld=opnsense --data-urlencode login=1 --data-urlencode "$name=$val" "$URL/"
  tok=$(curl -sk -c "$j" -b "$j" "$URL/ui/core/dashboard" | grep -oE 'X-CSRFToken", "[^"]+"' | head -1 | sed -E 's/.*", "([^"]+)"/\1/')
  [ -n "$tok" ] || die "login failed"
  umask 077
  curl -sk -b "$j" -H "X-CSRFToken: $tok" -H "Content-Type: application/json" -d '{}' \
    -X POST "$URL/api/auth/user/add_api_key/root" > "$VM/apikey.json"
  rm -f "$j"
  grep -q '"secret"' "$VM/apikey.json" || die "could not create API key: $(cat "$VM/apikey.json")"
  echo "API key: $VM/apikey.json"
}

down() {
  if running; then
    local pid; pid=$(cat "$VM/qemu.pid")
    kill "$pid"
    for _ in $(seq 1 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.2; done
    kill -0 "$pid" 2>/dev/null && kill -9 "$pid"
    echo "VM stopped"
  fi
  if [ -f "$VM/proxy.pid" ] && kill -0 "$(cat "$VM/proxy.pid")" 2>/dev/null; then
    kill "$(cat "$VM/proxy.pid")"; echo "proxy stopped"
  fi
  rm -f "$VM/qemu.pid" "$VM/proxy.pid" "$VM/proxy.release" "$VM/run.qcow2" "$VM/apikey.json" "$VM/cookies.txt" \
    "$VM/plugins-installed.txt" "$VM/plugins-failed.txt" "$VM/state.env"
}

api() { # api METHOD PATH [JSON]
  local k s
  k=$(jq -r .key "$VM/apikey.json"); s=$(jq -r .secret "$VM/apikey.json")
  # Content-Type only with a body: OPNsense rejects a GET with
  # "Content-Type: application/json" and no body (HTTP 400)
  curl -sk -u "$k:$s" ${3:+-H "Content-Type: application/json"} -X "$1" ${3:+-d "$3"} "$URL$2"
}

# firmware_wait: wait until the firmware job just started has finished.
# /api/core/firmware/running reports the firmware lock ("busy"/"ready");
# upgradestatus is not used here because it reports the *last* job and its
# log may be cut, so an earlier job could be taken for the new one.
firmware_wait() {
  local st
  for _ in $(seq 1 "$(waits 10)"); do
    st=$(api GET /api/core/firmware/running | jq -r '.status // ""' 2>/dev/null || true)
    [ "$st" = busy ] && break
    sleep 1
  done
  for _ in $(seq 1 "$(waits 600)"); do
    st=$(api GET /api/core/firmware/running | jq -r '.status // ""' 2>/dev/null || true)
    [ "$st" = ready ] && return 0
    sleep 3
  done
  return 1
}

# installed_packages: names of installed packages and plugins, from
# firmware/info over HTTP/2 (the large response is broken over HTTP/1.1)
installed_packages() {
  local k s
  k=$(jq -r .key "$VM/apikey.json"); s=$(jq -r .secret "$VM/apikey.json")
  curl -sk --http2 -u "$k:$s" "$URL/api/core/firmware/info" \
    | jq -r '[.package[]?, .plugin[]? | select(.installed == "1") | .name] | unique | .[]'
}

# proxy_for REL: the package proxy serves exactly the snapshot of REL and
# the VM's firmware points at it
proxy_for() {
  local rel="$1"
  need socat
  (cd "$ROOT" && go build -o .cache/bin/opnsense-api-spec ./cmd/opnsense-api-spec)
  if [ -f "$VM/proxy.pid" ] && kill -0 "$(cat "$VM/proxy.pid")" 2>/dev/null; then
    if [ "$(cat "$VM/proxy.release" 2>/dev/null)" = "$rel" ]; then return; fi
    kill "$(cat "$VM/proxy.pid")"
    for _ in $(seq 1 50); do ss -ltn | grep -q ":$PROXY_PORT " || break; sleep 0.2; done
  fi
  (cd "$ROOT" && exec .cache/bin/opnsense-api-spec proxy -release "$rel" -listen "127.0.0.1:$PROXY_PORT" >>"$VM/proxy.log" 2>&1) &
  echo $! > "$VM/proxy.pid"
  echo "$rel" > "$VM/proxy.release"
  for _ in $(seq 1 50); do ss -ltn | grep -q ":$PROXY_PORT " && break; sleep 0.2; done
  api POST /api/core/firmware/set "{\"firmware\":{\"mirror\":\"http://$PROXY_IP\",\"flavour\":\"\",\"type\":\"\",\"subscription\":\"\"}}" \
    | grep -q '"ok"' || die "could not set firmware mirror"
}

product_version() {
  api GET /api/core/firmware/status | jq -r '.product_version // .product.product_version // empty'
}

# update TARGET: update the running VM to exactly TARGET (same series as
# the image); waits for a reboot if the update needs one
update() {
  local target="$1" st status before
  [ -f "$VM/apikey.json" ] || die "no API key; run: $0 apikey"
  before=$(product_version)
  proxy_for "$target"
  echo "updating $before -> $target"
  api POST /api/core/firmware/update '{}' | grep -q '"ok"' || die "firmware update was not started"
  status=running
  for _ in $(seq 1 "$(waits 600)"); do
    st=$(api POST /api/core/firmware/upgradestatus '{}' 2>/dev/null || true)
    status=$(jq -r '.status // "unreachable"' <<<"$st" 2>/dev/null || echo unreachable)
    case "$status" in done|reboot|unreachable) break ;; esac
    sleep 3
  done
  if [ "$status" != "done" ]; then
    # reboot (announced or already in progress): wait for the GUI to return
    echo "waiting for reboot"
    for _ in $(seq 1 "$(waits 60)"); do curl -sk -m 3 -o /dev/null "$URL/" || break; sleep 3; done
    for _ in $(seq 1 "$(waits 200)"); do curl -sk -m 3 -o /dev/null "$URL/" && break; sleep 3; done
    for _ in $(seq 1 "$(waits 60)"); do [ -n "$(product_version 2>/dev/null)" ] && break; sleep 3; done
  fi
  local now; now=$(product_version)
  case "$now" in
    "$target"|"$target"_*) echo "VM runs $now" ;;
    *) die "update ended at $now, expected $target (log: $(jq -r '.log // ""' <<<"${st:-}" | tail -3))" ;;
  esac
}

plugins() {
  local series="$1"; shift
  local v; v=$(vm_version "$series")
  local irf="$ROOT/dist/$v/opnsense-$v.ir.json" list ok=0 failed=() installed=()
  [ -f "$VM/apikey.json" ] || die "no API key; run: $0 apikey"
  need jq
  proxy_for "$v"
  if [ $# -gt 0 ]; then
    list="$*"
  else
    [ -f "$irf" ] || die "no IR for $v"
    # every plugin that provides API endpoints (alternatives are not listed separately)
    list=$(jq -r '[.endpoints[].package | select(. != "opnsense")] | unique | .[]' "$irf")
  fi
  for p in $list; do
    api POST "/api/core/firmware/install/$p" '{}' | grep -q '"ok"' || { echo "install of $p not started" >&2; continue; }
    firmware_wait || echo "firmware job for $p did not finish in time" >&2
  done
  # what is actually installed counts, not what the jobs said
  local have; have=$(installed_packages)
  for p in $list; do
    if grep -qx "$p" <<<"$have"; then
      ok=$((ok + 1)); installed+=("$p"); echo "installed $p"
    else
      failed+=("$p")
    fi
  done
  echo "plugins: $ok installed, ${#failed[@]} failed${failed[*]:+: ${failed[*]}}"
  printf '%s\n' "${failed[@]}" > "$VM/plugins-failed.txt"
  printf '%s\n' "${installed[@]}" > "$VM/plugins-installed.txt"
}

# listfile FILE: non-empty lines of FILE, comma-separated
listfile() { grep -v '^$' "$1" 2>/dev/null | paste -sd, - || true; }

# latest_release SERIES: newest release of a series on the mirror
latest_release() {
  (cd "$ROOT" && go run ./cmd/opnsense-api-spec releases) | awk -v s="$1" '$2 == s {print $1}' | sort -V | tail -1
}

# ensure_ir REL: the IR of REL, from dist/ or from the published release
# (repositories that use the action have no dist/)
ensure_ir() {
  local rel="$1" f="$ROOT/dist/$1/opnsense-$1.ir.json"
  [ -f "$f" ] && return
  mkdir -p "$(dirname "$f")"
  curl -fsSL -m 120 -o "$f" \
    "https://github.com/${TESTVM_SPEC_REPO:-pipiobjo/opnsense-api-spec}/releases/download/opnsense-$rel/opnsense-$rel.ir.json" \
    || { rm -f "$f"; die "no IR for $rel (not in dist/ and not published)"; }
}

start() {
  local series="$1"; shift
  local want_plugins="" image
  while [ $# -gt 0 ]; do
    case "$1" in
      --plugins)
        case "${2:-}" in ""|--*) want_plugins=all ;; *) want_plugins="$2"; shift ;; esac ;;
      --release)
        case "${2:-}" in ""|--*) die "--release needs a release, e.g. --release 26.7.5 or latest" ;; esac
        TARGET="$2"; shift ;;
      *) die "unknown option $1" ;;
    esac
    shift
  done
  if [ "${TARGET:-}" = latest ]; then
    TARGET=$(latest_release "$series"); [ -n "$TARGET" ] || die "no release found for series $series"
  fi
  case "${TARGET:-$series}" in "$series"|"$series".*) ;; *) die "release ${TARGET} is not in series $series" ;; esac
  image=$(cat "$VM/base-$series.version" 2>/dev/null || echo "$series")
  rm -f "$VM/state.env"
  up "$series"
  trap down EXIT
  apikey
  if [ -n "${TARGET:-}" ] && [ "$TARGET" != "$image" ]; then
    update "$TARGET"
  fi
  : > "$VM/plugins-installed.txt"; : > "$VM/plugins-failed.txt"
  if [ "$want_plugins" = all ]; then
    ensure_ir "$(vm_version "$series")"
    plugins "$series"
  elif [ -n "$want_plugins" ]; then
    # shellcheck disable=SC2086 # a space-separated list of package names
    plugins "$series" $want_plugins
  fi
  local version; version=$(product_version)
  [ -n "$version" ] || die "VM does not answer with the new API key"
  {
    echo "url=$URL"
    echo "keyfile=$VM/apikey.json"
    echo "version=$version"
    echo "release=$(vm_version "$series")"
    echo "accel=$ACCEL"
    echo "plugins=$(listfile "$VM/plugins-installed.txt")"
    echo "failed=$(listfile "$VM/plugins-failed.txt")"
  } > "$VM/state.env"
  trap - EXIT
  echo "VM ready: $URL, $version"
}

verify() {
  local series="$1"; shift
  local args=() v irf
  while [ $# -gt 0 ]; do
    case "$1" in
      --plugins) args+=(--plugins all) ;;
      --release)
        case "${2:-}" in ""|--*) die "--release needs a release, e.g. --release 26.7.5" ;; esac
        TARGET="$2"; args+=(--release "$2"); shift ;;
      *) die "unknown option $1" ;;
    esac
    shift
  done
  v=$(vm_version "$series"); irf="$ROOT/dist/$v/opnsense-$v.ir.json"
  [ -f "$irf" ] || die "no IR for $v; run: go run ./cmd/opnsense-api-spec build -release $v -plugins -out dist/$v"
  start "$series" "${args[@]}"
  trap down EXIT
  contract "$irf"
}

# contract IRFILE: contract tests against the running VM (after start)
contract() {
  # the list of successful installs; /api/core/firmware/info is not used
  # because its large response arrives broken over HTTP/1.1 (26.7)
  local pk="opnsense" more
  more=$(listfile "$VM/plugins-installed.txt")
  [ -n "$more" ] && pk="opnsense,$more"
  (cd "$ROOT" && go run ./cmd/opnsense-api-spec verify -write -packages "$pk" \
    -ir "$1" -url "$URL" -keyfile "$VM/apikey.json")
}

case "${1:-}" in
  prepare) prepare "${2:?series, e.g. 26.7}" ;;
  image-version) image_version "${2:?series}" ;;
  up)      up "${2:?series}" ;;
  apikey)  apikey ;;
  down)    down ;;
  plugins) s="${2:?series}"; shift 2; plugins "$s" "$@" ;;
  update)  TARGET="${2:?release}"; update "$TARGET" ;;
  start)   s="${2:?series}"; shift 2; start "$s" "$@" ;;
  contract) running || die "no VM running; run: $0 start <series>"; contract "${2:?IR file}" ;;
  verify)  s="${2:?series}"; shift 2; verify "$s" "$@" ;;
  *) sed -n '4,27p' "$0"; exit 2 ;;
esac
