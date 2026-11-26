#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/parallel.sh with a fake VM harness and a fake generator
# (no VM, no network): every group runs on its own slot with the core on
# exactly one, a plugin that does not install (in a group or in the
# conflict check) and a failed test fail the run, and every VM is torn
# down, also when the run is interrupted.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RUNNER="$HERE/../parallel.sh"
W=$(mktemp -d); [ -n "${KEEP:-}" ] || trap 'rm -rf "$W"' EXIT; echo "work: $W"
fails=0
is() { # is NAME WANT GOT
  if [ "$2" = "$3" ]; then echo "ok   $1"; else echo "FAIL $1: want $2, got $3"; fails=$((fails + 1)); fi
}

# fake tools/testvm.sh: a "VM" is a sleep process with a pid file
cat > "$W/testvm.sh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
vm="$FAKE/vm-slot-${TESTVM_SLOT:-0}"
case "$1" in
  lockfile) echo "$vm.lock" ;;
  ir) touch "$FAKE/ir-$2.json"; echo "$FAKE/ir-$2.json" ;;
  prepare) ;;
  start)
    mkdir -p "$vm"; shift 2
    list=""
    while [ $# -gt 0 ]; do case "$1" in --plugins) list="$2"; shift ;; esac; shift; done
    sleep 300 & echo $! > "$vm/qemu.pid"
    echo "web GUI up: fake"; echo "API key: /fake/apikey.json"; echo "VM runs fake"
    ok=() bad=()
    n=$(wc -w <<<"$list")
    for p in $list; do
      if [[ " ${FAKE_FAIL:-} " == *" $p "* ]] || { [ "$n" -eq 3 ] && [ "$p" = "${FAKE_FAIL_CONFLICT:-}" ]; }; then
        bad+=("$p"); echo "plugin $p: failed after 1 s"
      else
        ok+=("$p"); echo "plugin $p: 2 s"
      fi
    done
    echo "plugins: ${#ok[@]} installed, ${#bad[@]} failed"
    { echo "url=https://127.0.0.1:1"; echo "keyfile=/fake/apikey.json"; echo "slow=1"
      echo "plugins=$(IFS=,; echo "${ok[*]}")"; echo "failed=$(IFS=,; echo "${bad[*]}")"; } > "$vm/state.env" ;;
  down)
    [ -f "$vm/qemu.pid" ] && kill "$(cat "$vm/qemu.pid")" 2>/dev/null || true
    rm -f "$vm/qemu.pid"; echo "$TESTVM_SLOT" >> "$FAKE/downs" ;;
  *) echo "fake testvm: $*" >&2; exit 2 ;;
esac
EOF
# fake generator: the split of every release
cat > "$W/spec.sh" <<'EOF'
#!/usr/bin/env bash
case "$1" in
  groups) echo '{"release":"r","weights_from":"count","all":["os-a","os-b","os-c"],
    "groups":[{"name":"auto-1","core":true,"plugins":["os-a"]},{"name":"auto-2","core":false,"plugins":["os-b","os-c"]}]}' ;;
  *) echo "fake spec: $*" >&2; exit 2 ;;
esac
EOF
chmod +x "$W/testvm.sh" "$W/spec.sh"
MATRIX='[{"release":"26.1.11","series":"26.1"},{"release":"26.7.10","series":"26.7"}]'
TESTCMD='echo $$ > "$GROUP_DIR/pid"; echo "$GROUP $CORE $PLUGINS" > "$GROUP_DIR/ran"; [ -z "${FAKE_SLEEP:-}" ] || sleep "$FAKE_SLEEP"; [ "$GROUP" != "${FAKE_FAILGROUP:-}" ]'
MERGECMD='echo "$GROUP_DIRS" > "$RELEASE_DIR/merged"'

run() { # run CASE [ENV...]: the runner with the fakes; prints its exit status
  local c="$1"; shift
  mkdir -p "$W/$c"
  env "$@" FAKE="$W/$c" PARALLEL_TESTVM="$W/testvm.sh" PARALLEL_SPEC="$W/spec.sh" \
    "$RUNNER" --matrix "$MATRIX" --out "$W/$c/out" --state "$W/$c/state" --first-slot 40 \
    --test "$TESTCMD" --merge "$MERGECMD" > "$W/$c/stdout" 2>&1 && echo 0 || echo $?
}
alive() { # fake VMs still running in a case
  local n=0 f
  for f in "$W/$1"/vm-slot-*/qemu.pid; do [ -f "$f" ] && kill -0 "$(cat "$f")" 2>/dev/null && n=$((n + 1)); done
  echo "$n"
}

is "all pass: exit" 0 "$(run ok)"
is "all pass: 6 VMs (2 groups + conflict check per release), each torn down" "40 41 42 43 44 45" \
  "$(sort -n "$W/ok/downs" | uniq | paste -sd' ')"
is "all pass: no VM left" 0 "$(alive ok)"
is "all pass: the core on exactly one VM per release" "auto-1 1 os-a|auto-2 0 os-b,os-c" \
  "$(cat "$W/ok/out/26.7.10/auto-1/ran" "$W/ok/out/26.7.10/auto-2/ran" | paste -sd'|')"
is "all pass: no test on the conflict VM" no "$([ -f "$W/ok/out/26.7.10/conflicts/ran" ] && echo yes || echo no)"
is "all pass: merged per release from its groups" "$W/ok/out/26.1.11/auto-1 $W/ok/out/26.1.11/auto-2" "$(cat "$W/ok/out/26.1.11/merged")"
is "all pass: timing table, one row per VM" 6 "$(grep -c '^| 26\.' "$W/ok/out/timing.md")"
is "all pass: install times kept for the next split" '{"os-a":2}' \
  "$(jq -c '.runs[] | select(.core) | .install' "$W/ok/state/timings-26.7.json")"

is "plugin not installed in a group: exit" 1 "$(run nogroup FAKE_FAIL=os-b)"
is "plugin not installed in a group: named" 1 "$(grep -c '26.7.10 auto-2: plugins not installed: os-b' "$W/nogroup/stdout")"
is "plugin not installed in a group: no timings kept" no "$([ -f "$W/nogroup/state/timings-26.7.json" ] && echo yes || echo no)"

is "conflict check: a plugin that fails only with all others fails the run" 1 "$(run conflict FAKE_FAIL_CONFLICT=os-c)"
is "conflict check: reported" 1 "$(grep -c '26.7.10 conflict check (all plugins on one VM): 2 installed, failed: os-c' "$W/conflict/stdout")"

is "failed test: exit" 1 "$(run failtest FAKE_FAILGROUP=auto-2)"
is "failed test: every VM torn down" 6 "$(sort -u "$W/failtest/downs" | wc -l)"
is "failed test: no VM left" 0 "$(alive failtest)"

# interrupt while the tests run
mkdir -p "$W/int"
env FAKE="$W/int" FAKE_SLEEP=60 PARALLEL_TESTVM="$W/testvm.sh" PARALLEL_SPEC="$W/spec.sh" \
  "$RUNNER" --matrix "$MATRIX" --out "$W/int/out" --state "$W/int/state" --first-slot 40 \
  --test "$TESTCMD" --merge "$MERGECMD" > "$W/int/stdout" 2>&1 &
pid=$!
for _ in $(seq 1 100); do
  [ "$(cat "$W"/int/out/*/auto-*/ran 2>/dev/null | wc -l)" -ge 4 ] && break
  sleep 0.1
done
t=$SECONDS
kill -TERM "$pid"
rc=0; wait "$pid" || rc=$?
is "interrupt: exit 130" 130 "$rc"
is "interrupt: ends at once" yes "$([ $((SECONDS - t)) -lt 10 ] && echo yes || echo no)"
is "interrupt: every VM torn down" 6 "$(sort -u "$W/int/downs" | wc -l)"
is "interrupt: no VM left" 0 "$(alive int)"
left=0
for f in "$W"/int/out/*/auto-*/pid; do kill -0 "$(cat "$f")" 2>/dev/null && left=$((left + 1)); done
is "interrupt: no test left running" 0 "$left"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
