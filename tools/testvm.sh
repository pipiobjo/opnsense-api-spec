#!/usr/bin/env bash
# Throwaway OPNsense test VM for contract tests (QEMU/KVM, official nano image).
#
#   tools/testvm.sh prepare 26.7   download + verify image, build read-only qcow2 base
#   tools/testvm.sh up 26.7        boot a fresh overlay (previous one is discarded)
#   tools/testvm.sh apikey         create a throwaway API key (factory login root/opnsense)
#   tools/testvm.sh plugins 26.7 [os-a os-b ...]
#                                  install plugins (default: all with an API
#                                  in the IR) through the package proxy
#   tools/testvm.sh down           stop the VM (and proxy), delete the overlay
#   tools/testvm.sh verify 26.7 [--plugins]
#                                  up + apikey [+ plugins] + contract tests + down
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

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 not installed"; }

# image version of a series: images may be built on a patch release
# (26.1 -> 26.1.6); the key is always OPNsense-<series>.pub
image_version() {
  curl -fsS -m 30 "$MIRROR/$1/" | grep -oE "OPNsense-$1(\.[0-9]+)?-nano-amd64\.img\.bz2\"" \
    | head -1 | sed -E "s/^OPNsense-(.*)-nano-amd64.*/\1/"
}

# release running in the VM built from base-<series>.qcow2
vm_version() { cat "$VM/base-$1.version" 2>/dev/null || echo "$1"; }

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
  echo "base image: $VM/base-$series.qcow2 ($v)"
}

running() { [ -f "$VM/qemu.pid" ] && kill -0 "$(cat "$VM/qemu.pid")" 2>/dev/null; }

up() {
  local series="$1"
  [ -f "$VM/base-$series.qcow2" ] || die "run: $0 prepare $series"
  running && die "VM already running (pid $(cat "$VM/qemu.pid")); run: $0 down"
  need qemu-system-x86_64
  [ -r /dev/kvm ] || die "/dev/kvm not accessible"
  rm -f "$VM/run.qcow2" "$VM/console.log" "$VM/apikey.json" "$VM/cookies.txt"
  # 24G: room for plugins; the nano image grows to the disk size on first boot
  qemu-img create -q -f qcow2 -F qcow2 -b "base-$series.qcow2" "$VM/run.qcow2" 24G
  # vtnet0 = LAN (192.168.1.1), vtnet1 = WAN, vtnet2/3 = spare NICs for
  # interface tests; all networks are isolated (restrict=on)
  qemu-system-x86_64 -name "opnsense-test-$series" -enable-kvm -cpu host -m 2048 -smp 2 \
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
  for _ in $(seq 1 120); do
    if curl -sk -m 3 -o /dev/null "$URL/"; then echo "web GUI up: $URL"; return 0; fi
    running || die "VM exited; see $VM/console.log"
    sleep 3
  done
  die "web GUI did not come up within 6 minutes"
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
  rm -f "$VM/qemu.pid" "$VM/proxy.pid" "$VM/run.qcow2" "$VM/apikey.json" "$VM/cookies.txt" \
    "$VM/plugins-installed.txt" "$VM/plugins-failed.txt"
}

api() { # api METHOD PATH [JSON]
  local k s
  k=$(jq -r .key "$VM/apikey.json"); s=$(jq -r .secret "$VM/apikey.json")
  curl -sk -u "$k:$s" -H "Content-Type: application/json" -X "$1" ${3:+-d "$3"} "$URL$2"
}

firmware_job() { # wait for a firmware job; prints last log lines on failure
  local st status
  for _ in $(seq 1 400); do
    st=$(api POST /api/core/firmware/upgradestatus '{}')
    status=$(jq -r .status <<<"$st")
    [ "$status" != running ] && break
    sleep 3
  done
  [ "$status" = done ] || { jq -r .log <<<"$st" | tail -5 >&2; return 1; }
  jq -r .log <<<"$st"
}

plugins() {
  local series="$1"; shift
  local v; v=$(vm_version "$series")
  local irf="$ROOT/dist/$v/opnsense-$v.ir.json" list ok=0 failed=() installed=()
  [ -f "$VM/apikey.json" ] || die "no API key; run: $0 apikey"
  need jq; need socat
  (cd "$ROOT" && go build -o .cache/bin/opnsense-api-spec ./cmd/opnsense-api-spec)
  if ! { [ -f "$VM/proxy.pid" ] && kill -0 "$(cat "$VM/proxy.pid")" 2>/dev/null; }; then
    (cd "$ROOT" && exec .cache/bin/opnsense-api-spec proxy -release "$v" -listen "127.0.0.1:$PROXY_PORT" >"$VM/proxy.log" 2>&1) &
    echo $! > "$VM/proxy.pid"
    for _ in $(seq 1 50); do ss -ltn | grep -q ":$PROXY_PORT " && break; sleep 0.2; done
  fi
  api POST /api/core/firmware/set "{\"firmware\":{\"mirror\":\"http://$PROXY_IP\",\"flavour\":\"\",\"type\":\"\",\"subscription\":\"\"}}" \
    | grep -q '"ok"' || die "could not set firmware mirror"
  if [ $# -gt 0 ]; then
    list="$*"
  else
    [ -f "$irf" ] || die "no IR for $v"
    # every plugin that provides API endpoints (alternatives are not listed separately)
    list=$(jq -r '[.endpoints[].package | select(. != "opnsense")] | unique | .[]' "$irf")
  fi
  for p in $list; do
    api POST "/api/core/firmware/install/$p" '{}' | grep -q '"ok"' || { failed+=("$p"); continue; }
    if firmware_job | grep -qiE "^Installation out of date|No packages available|pkg: .*(error|conflict)"; then
      failed+=("$p")
    else
      ok=$((ok + 1)); installed+=("$p"); echo "installed $p"
    fi
  done
  echo "plugins: $ok installed, ${#failed[@]} failed${failed[*]:+: ${failed[*]}}"
  printf '%s\n' "${failed[@]}" > "$VM/plugins-failed.txt"
  printf '%s\n' "${installed[@]}" > "$VM/plugins-installed.txt"
}

verify() {
  local series="$1" with_plugins="${2:-}" v irf
  v=$(vm_version "$series"); irf="$ROOT/dist/$v/opnsense-$v.ir.json"
  [ -f "$irf" ] || die "no IR for $v; run: go run ./cmd/opnsense-api-spec build -release $v -plugins -out dist/$v"
  up "$series"
  trap down EXIT
  apikey
  local pk="opnsense"
  if [ "$with_plugins" = "--plugins" ]; then
    plugins "$series"
    # the list of successful installs; /api/core/firmware/info is not used
    # because its large response arrives broken over HTTP/1.1 (26.7)
    pk="opnsense,$(paste -sd, "$VM/plugins-installed.txt")"
  fi
  (cd "$ROOT" && go run ./cmd/opnsense-api-spec verify -write -packages "$pk" \
    -ir "$irf" -url "$URL" -keyfile "$VM/apikey.json")
}

case "${1:-}" in
  prepare) prepare "${2:?series, e.g. 26.7}" ;;
  up)      up "${2:?series}" ;;
  apikey)  apikey ;;
  down)    down ;;
  plugins) s="${2:?series}"; shift 2; plugins "$s" "$@" ;;
  verify)  verify "${2:?series}" "${3:-}" ;;
  *) sed -n '2,13p' "$0"; exit 2 ;;
esac
