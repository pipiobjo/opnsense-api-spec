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
#                                  (url, keyfile, version, plugins, accel,
#                                  slow: the factor waits were stretched by); used by
#                                  the action .github/actions/testvm
#   tools/testvm.sh contract dist/26.7.5/opnsense-26.7.5.ir.json
#                                  contract tests against the running VM
#   tools/testvm.sh verify 26.7 [--release 26.7.5] [--plugins]
#                                  start + contract tests + down
#   tools/testvm.sh contract-coverage
#                                  coverage report of the last contract test,
#                                  with the floor (tools/ci/coverage.sh)
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
# the contract test (verify) stretches its request timeouts by the same
# factor (cmd/opnsense-api-spec, TESTVM_SLOW)
export TESTVM_SLOW="$SLOW"
# waits N: number of wait iterations, stretched for software emulation
waits() { echo $(($1 * SLOW)); }

die() { echo "error: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null || die "$1 not installed"; }
# Downloads from outside (mirror, changelog, GitHub) retry: one dropped
# connection must not end a job that already booted a VM. Everything
# downloaded is verified afterwards (checksums, signatures), so a retry
# cannot let a wrong file through.
NET=(--retry 5 --retry-all-errors --retry-delay 5 --connect-timeout 20)

# image version of a series: images may be built on a patch release
# (26.1 -> 26.1.6); the key is always OPNsense-<series>.pub
image_version() {
  curl -fsS "${NET[@]}" -m 30 "$MIRROR/$1/" | grep -oE "OPNsense-$1(\.[0-9]+)?-nano-amd64\.img\.bz2\"" \
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
    curl -fsS "${NET[@]}" -m 60 -o "$d/$f" "$MIRROR/$series/$f"
  done
  [ -f "$d/$n.bz2" ] || curl -fsS "${NET[@]}" -m 1800 -o "$d/$n.bz2" "$MIRROR/$series/$n.bz2"

  local got want announced
  got=$(sha256sum "$d/$n.bz2" | cut -d' ' -f1)
  want=$(grep "($n.bz2)" "$d/OPNsense-$v-checksums-amd64.sha256" | awk '{print $NF}')
  announced=$(curl -fsS "${NET[@]}" -m 30 "$CHANGELOG/$series/$v" | grep "($n.bz2)" | awk '{print $NF}')
  [ "$got" = "$want" ] || die "checksum mismatch against mirror"
  [ "$got" = "$announced" ] || die "checksum mismatch against changelog announcement"
  echo "checksum ok (mirror + changelog announcement)"

  diff <(sed -n '/BEGIN PUBLIC KEY/,/END PUBLIC KEY/p' "$d/OPNsense-$series.pub") \
       <(curl -fsS "${NET[@]}" -m 30 "$CHANGELOG/$series/$series" | sed -n '/BEGIN PUBLIC KEY/,/END PUBLIC KEY/p' | sed 's/^# *//') \
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
  rm -f "$VM/run.qcow2" "$VM/console.log" "$VM/apikey.json" "$VM/cookies.txt" "$VM"/install-*.log
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
    # -f: an error page (502 while php starts) is not a working GUI
    if curl -skf -m 3 -o /dev/null "$URL/"; then echo "web GUI up: $URL"; return 0; fi
    running || die "VM exited; see $VM/console.log"
    sleep 3
  done
  die "web GUI did not come up within $((6 * SLOW)) minutes"
}

# apikey: log in with the factory credentials and create an API key; three
# attempts, because the GUI can answer before its login works (first boot
# still restarting services)
apikey() {
  local i
  for i in 1 2 3; do
    apikey_once && return 0
    [ "$i" -lt 3 ] && { echo "API key: attempt $i failed, retrying" >&2; sleep "$((10 * SLOW))"; }
  done
  die "could not create an API key in 3 attempts"
}

apikey_once() {
  local j="$VM/cookies.txt" page inp name val tok
  rm -f "$j"
  page=$(curl -sk -m 60 -c "$j" -b "$j" "$URL/") || return 1
  inp=$(grep -oE '<input type="hidden" name="[^"]+" value="[^"]+"' <<<"$page" | head -1)
  name=$(sed -E 's/.*name="([^"]+)".*/\1/' <<<"$inp")
  val=$(sed -E 's/.*value="([^"]+)".*/\1/' <<<"$inp")
  # factory credentials of a fresh, isolated test image only
  curl -sk -m 60 -c "$j" -b "$j" -o /dev/null --data-urlencode usernamefld=root \
    --data-urlencode passwordfld=opnsense --data-urlencode login=1 --data-urlencode "$name=$val" "$URL/" || return 1
  tok=$(curl -sk -m 60 -c "$j" -b "$j" "$URL/ui/core/dashboard" | grep -oE 'X-CSRFToken", "[^"]+"' | head -1 | sed -E 's/.*", "([^"]+)"/\1/')
  [ -n "$tok" ] || { echo "login failed" >&2; return 1; }
  umask 077
  curl -sk -m 60 -b "$j" -H "X-CSRFToken: $tok" -H "Content-Type: application/json" -d '{}' \
    -X POST "$URL/api/auth/user/add_api_key/root" > "$VM/apikey.json" || return 1
  rm -f "$j"
  grep -q '"secret"' "$VM/apikey.json" || { echo "could not create API key: $(cat "$VM/apikey.json")" >&2; return 1; }
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
  # "Content-Type: application/json" and no body (HTTP 400). -m: a hung VM
  # ends the call, not the job hours later.
  curl -sk -m "$((120 * SLOW))" -u "$k:$s" ${3:+-H "Content-Type: application/json"} -X "$1" ${3:+-d "$3"} "$URL$2"
}

# install_plugin PKG: install one plugin and wait for exactly that job.
# /api/core/firmware/running and the status of upgradestatus describe the
# *last* job, which before the new one starts is the previous one (a job
# for an unknown package even finishes in under a second). What identifies
# the new job is that the firmware log changed: remember it, start, wait
# until it differs and the job is no longer running. Prints the firmware's
# error lines when the plugin is missing afterwards; returns 1 then.
fw_log() { api GET /api/core/firmware/upgradestatus | jq -r '.log // ""' 2>/dev/null | sha256sum | cut -c1-16; }
fw_status() { api GET /api/core/firmware/upgradestatus | jq -r '.status // ""' 2>/dev/null || true; }
# fw_wait_idle SECONDS: wait until no firmware job runs. Asked through
# firmware/running, which answers "ready" only when configd answers and is
# idle ("busy" during a job, nothing while configd restarts, as installs
# do). firmware/upgradestatus cannot tell: it answers "error" both for "no
# job yet" (fresh VM) and for "configd restarting" in the middle of a job;
# on 2026-11-17 taking "error" as the end sent the next install while the
# previous one ran, and the firmware dropped it. Measured on 26.7.9_3:
# "busy" for the whole install, "ready" together with "done".
fw_wait_idle() {
  for _ in $(seq 1 "$(( $1 / 3 ))"); do
    [ "$(api GET /api/core/firmware/running | jq -r '.status // ""' 2>/dev/null)" = ready ] && return 0
    sleep 3
  done
  return 1
}

install_plugin() {
  local p="$1" before started=0
  # never start while another firmware job runs: the request would be lost
  fw_wait_idle "$(waits 600)" || echo "install of $p: the previous firmware job did not finish" >&2
  before=$(fw_log)
  api POST "/api/core/firmware/install/$p" '{}' | grep -q '"ok"' || { echo "install of $p not started: firmware refused" >&2; return 1; }
  for _ in $(seq 1 "$(waits 60)"); do
    if [ "$(fw_log)" != "$before" ]; then started=1; break; fi
    sleep 1
  done
  if [ "$started" = 0 ]; then
    echo "install of $p: no firmware job started within $(waits 60) s" >&2
  else
    fw_wait_idle "$(waits 1800)" || echo "install of $p: firmware job still running after $(waits 1800) s" >&2
  fi
  # the package list can lag behind the end of the job
  for _ in $(seq 1 12); do
    if installed_packages | grep -qx "$p"; then return 0; fi
    sleep 5
  done
  # evidence for later: the complete firmware log and what the firmware
  # says about the package (CI keeps $VM/*.log when a plugin is missing)
  {
    echo "# status: $(fw_status), started: $started, $(date -u +%FT%TZ)"
    api GET /api/core/firmware/upgradestatus | jq -r '.log // ""'
    echo "# firmware/info entry:"
    api GET /api/core/firmware/info | jq -c --arg p "$p" '[.package[]?, .plugin[]? | select(.name == $p)]'
  } > "$VM/install-$p.log" 2>&1
  echo "install of $p failed (complete firmware log: $VM/install-$p.log):" >&2
  grep -iE "error|fail|no packages|not found|conflict|$p" "$VM/install-$p.log" | tail -n 5 | sed 's/^/  /' >&2
  return 1
}

# installed_packages: names of installed packages and plugins, with the
# contract test's own detection (built by proxy_for, which every
# installation goes through)
installed_packages() {
  "$ROOT/.cache/bin/opnsense-api-spec" installed -url "$URL" -keyfile "$VM/apikey.json"
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

# update_state CURL_RC BODY LOST: what one upgradestatus poll means.
#   done|reboot   the firmware said so
#   lost          no connection (LOST counts the ones before this poll)
#   unreachable   no connection for the third time in a row: the VM reboots
#   running       anything else, also an answer that is not JSON (an error
#                 page while the web server restarts during the update)
update_state() {
  local rc="$1" body="$2" lost="$3" s
  if [ "$rc" -ne 0 ]; then
    if [ "$lost" -ge 2 ]; then echo unreachable; else echo lost; fi
    return
  fi
  s=$(jq -r '.status // empty' <<<"$body" 2>/dev/null || true)
  case "$s" in done|reboot) echo "$s" ;; *) echo running ;; esac
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
  local lost=0 rc
  for _ in $(seq 1 "$(waits 600)"); do
    rc=0; st=$(api POST /api/core/firmware/upgradestatus '{}' 2>/dev/null) || rc=$?
    status=$(update_state "$rc" "$st" "$lost")
    case "$status" in
      done|reboot|unreachable) break ;;
      lost) lost=$((lost + 1)) ;;
      *) lost=0 ;;
    esac
    sleep 3
  done
  if [ "$status" != "done" ]; then
    # reboot (announced or already in progress): wait for the GUI to return
    echo "waiting for reboot"
    for _ in $(seq 1 "$(waits 60)"); do curl -sk -m 3 -o /dev/null "$URL/" || break; sleep 3; done
    for _ in $(seq 1 "$(waits 200)"); do curl -skf -m 3 -o /dev/null "$URL/" && break; sleep 3; done
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
  local p have
  for p in $list; do install_plugin "$p" || true; done
  # what is actually installed counts, not what the jobs said; a missing
  # plugin's firmware log is printed by install_plugin
  have=$(installed_packages)
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
  local url="https://github.com/${TESTVM_SPEC_REPO:-pipiobjo/opnsense-api-spec}/releases/download/opnsense-$rel/opnsense-$rel.ir.json"
  curl -fsSL "${NET[@]}" -m 120 -o "$f" "$url" \
    || { local rc=$?; rm -f "$f"; die "no IR for $rel: not in dist/, and downloading $url failed (curl exit $rc; 22 means not published)"; }
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
    echo "slow=$SLOW"
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
  # the list of successful installs (installed_packages checked each one),
  # and the plugins that could not be installed (recorded in the report)
  local pk="opnsense" more failed
  more=$(listfile "$VM/plugins-installed.txt")
  [ -n "$more" ] && pk="opnsense,$more"
  failed=$(listfile "$VM/plugins-failed.txt")
  # built with coverage of the contract test's own code (the command and
  # internal/verify; the main package must be instrumented, or no data is
  # written):
  # contract-coverage reports which of it ran against the instance
  rm -rf "$VM/cover"; mkdir -p "$VM/cover"
  local rc=0
  (cd "$ROOT" && GOCOVERDIR="$VM/cover" go run -cover -coverpkg=./cmd/opnsense-api-spec,./internal/verify ./cmd/opnsense-api-spec verify -write -packages "$pk" -not-installable "$failed" \
    -ir "$1" -url "$URL" -keyfile "$VM/apikey.json") || rc=$?
  go tool covdata textfmt -i="$VM/cover" -o "$VM/contract.out" || true
  return "$rc"
}

# contract_coverage: report for the last contract test; the floor is
# checked only with COVERAGE_ENFORCE=1 (tools/ci/coverage.sh)
contract_coverage() {
  "$ROOT/tools/ci/coverage.sh" contract "$VM/contract.out" "$ROOT/coverage"
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
  contract-coverage) contract_coverage ;;
  *) sed -n '4,27p' "$0"; exit 2 ;;
esac
