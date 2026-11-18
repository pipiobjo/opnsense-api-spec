#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of the decisions in tools/testvm.sh that need no VM: what one poll
# of the firmware update status means (update_state).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# only the function under test, not the script (it would dispatch)
eval "$(sed -n '/^update_state() {/,/^}/p' "$HERE/../testvm.sh")"
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

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
