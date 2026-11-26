#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of the decisions in tools/testvm.sh that need no VM: what one poll
# of the firmware update status means (update_state), and which paths,
# ports and names a slot gets (slot_config).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# only the function under test, not the script (it would dispatch)
eval "$(sed -n '/^update_state() {/,/^}/p; /^slot_config() {/,/^}/p; /^die() {/p' "$HERE/../testvm.sh")"
fails=0

is() { # is NAME WANT GOT
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want $2, got $3"; fails=$((fails + 1)); fi
}

is "update: done" "done" "$(update_state 0 '{"status":"done","log":"..."}' 0)"
is "update: reboot announced" reboot "$(update_state 0 '{"status":"reboot"}' 0)"
is "update: still running" running "$(update_state 0 '{"status":"running"}' 0)"
is "update: error page while the web server restarts keeps polling" running \
  "$(update_state 0 '<html><body>502 Bad Gateway</body></html>' 0)"
is "update: empty answer keeps polling" running "$(update_state 0 '' 0)"
is "update: first lost connection is not a reboot" lost "$(update_state 7 '' 0)"
is "update: second lost connection is not a reboot" lost "$(update_state 52 '' 1)"
is "update: third lost connection in a row is the reboot" unreachable "$(update_state 28 '' 2)"

# slot_config in a subshell: prints what one slot gets, or "refused"
ROOT=/r BASE=/r/.cache/vm
slot() { # slot SLOT [PORT] [PROXY_PORT]
  (unset PORT PROXY_PORT; TESTVM_SLOT="$1"; [ -n "${2:-}" ] && PORT="$2"; [ -n "${3:-}" ] && PROXY_PORT="$3"
   slot_config 2>/dev/null && echo "$VM $PORT $PROXY_PORT $QNAME $BIN $LOCK") || echo refused
}
is "slot: none keeps the fixed paths and ports" "/r/.cache/vm 18443 18080  /r/.cache/bin /r/.cache/vm.lock" "$(slot '')"
is "slot: none keeps PORT and PROXY_PORT overrides" "/r/.cache/vm 19443 19080  /r/.cache/bin /r/.cache/vm.lock" "$(slot '' 19443 19080)"
is "slot: 3 has its own directory, ports, name, binary and lock" \
  "/r/.cache/vm-slot-3 18446 18083 -slot3 /r/.cache/vm-slot-3/bin /r/.cache/vm-slot-3.lock" "$(slot 3)"
is "slot: PORT overrides a slot's port" "/r/.cache/vm-slot-3 20000 18083 -slot3 /r/.cache/vm-slot-3/bin /r/.cache/vm-slot-3.lock" "$(slot 3 20000)"
for bad in 0 100 a 1a -1 " 2" 07; do
  is "slot: '$bad' is refused" refused "$(slot "$bad")"
done
# no two slots (and not the default) share a directory, port or lock
all=$( { slot ''; for n in $(seq 1 99); do slot "$n"; done; } )
is "slot: 100 configurations" 100 "$(wc -l <<<"$all")"
for col in 1 2 3 6; do
  is "slot: column $col unique over all slots" 0 "$(awk -v c="$col" '{print $c}' <<<"$all" | sort | uniq -d | wc -l)"
done
is "slot: a slot's port is never a proxy port" 0 \
  "$(comm -12 <(awk '{print $2}' <<<"$all" | sort) <(awk '{print $3}' <<<"$all" | sort) | wc -l)"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
