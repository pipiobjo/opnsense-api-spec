#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Local test runs on several throwaway VMs at once (docs/testing.md,
# "Parallel local runs"). For every release, the plugins are split into
# groups (opnsense-api-spec groups, the groups file): each group gets its
# own VM (tools/testvm.sh slot), which boots, updates to the release,
# installs that group's plugins fresh and runs the tests of exactly those
# packages; the core is tested on exactly one of them. One more VM per
# release installs every plugin together and runs no test: the conflict
# check. The results of a release are merged into one report, and the
# coverage floors apply to the merged profile of each series. Every VM is
# torn down at the end, also after a failure or an interrupt.
#
#   tools/parallel.sh [--matrix JSON] [--groups FILE] [--auto N] [--out DIR]
#                     [--state DIR] [--first-slot N] [--test CMD] [--merge CMD]
#
#   --matrix JSON  releases as [{"release":..,"series":..}]; default: the
#                  newest release of every series in dist/index.json
#                  (opnsense-api-spec plan -matrix -newest)
#   --groups FILE  groups file (default verify/groups.json)
#   --auto N       balanced groups per release (default: the file's)
#   --out DIR      where logs and results go (default .cache/parallel/<time>)
#   --state DIR    where the timings of the last run are kept for balancing
#                  the next one (default .cache/parallel)
#   --first-slot N first TESTVM_SLOT to use (default 1); two runs at once
#                  need separate slot ranges
#   --test CMD     what runs on each group's VM (default: the contract test,
#                  tools/testvm.sh contract); environment below
#   --merge CMD    what merges a release's groups (default: merge-verified
#                  and the contract coverage floor); environment below
#
# Environment of --test: TESTVM_SLOT, RELEASE, SERIES, GROUP, CORE (1 if
# this group tests the core), PLUGINS (the group's installed plugins,
# comma-separated), FAILED (its plugins that did not install), GROUP_DIR
# (for the group's results), TESTVM_URL, TESTVM_KEYFILE, TESTVM_SLOW. Of
# --merge: RELEASE, SERIES, RELEASE_DIR, GROUP_DIRS (space-separated).
#
# CI and the scheduled pipeline do not use this: they test every plugin on
# one VM per release (tools/testvm.sh start --plugins all), which also
# tests the installation of all plugins together in the same run.
set -euo pipefail
export LC_ALL=C

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
# PARALLEL_TESTVM and PARALLEL_SPEC replace the VM harness and the
# generator in the script test (tools/ci/parallel_test.sh) only
TVM="${PARALLEL_TESTVM:-$ROOT/tools/testvm.sh}"
MATRIX="" GROUPS_FILE="$ROOT/verify/groups.json" AUTO=0 OUT="" STATE="$ROOT/.cache/parallel" FIRST=1 TEST="" MERGE=""
die() { echo "error: $*" >&2; exit 1; }
while [ $# -gt 0 ]; do
  case "$1" in
    --matrix) MATRIX="${2:?}"; shift ;;
    --groups) GROUPS_FILE="$(realpath "${2:?}")"; shift ;;
    --auto) AUTO="${2:?}"; shift ;;
    --out) OUT="${2:?}"; shift ;;
    --state) STATE="${2:?}"; shift ;;
    --first-slot) FIRST="${2:?}"; shift ;;
    --test) TEST="${2:?}"; shift ;;
    --merge) MERGE="${2:?}"; shift ;;
    *) sed -n '4,40p' "$0"; exit 2 ;;
  esac
  shift
done
[[ "$AUTO" =~ ^[0-9]+$ && "$FIRST" =~ ^[1-9][0-9]?$ ]] || die "--auto and --first-slot take numbers"
OUT="${OUT:-$ROOT/.cache/parallel/$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$OUT" "$STATE"
OUT="$(realpath "$OUT")" STATE="$(realpath "$STATE")"
read -r -a SPEC <<<"${PARALLEL_SPEC:-go run ./cmd/opnsense-api-spec}"
cd "$ROOT"

T0=$EPOCHSECONDS
# stamp: every line prefixed with the seconds since the run started
stamp() { while IFS= read -r l; do printf '%5d %s\n' "$((EPOCHSECONDS - T0))" "$l"; done; }
log() { echo "$*" | stamp | tee -a "$OUT/run.log"; }

[ -n "$MATRIX" ] || MATRIX=$("${SPEC[@]}" plan -matrix -newest)
mapfile -t RELEASES < <(jq -r '.[] | "\(.release) \(.series)"' <<<"$MATRIX")
[ "${#RELEASES[@]}" -gt 0 ] || die "no releases to test"
log "releases: $(jq -r '[.[].release] | join(" ")' <<<"$MATRIX"); groups file $GROUPS_FILE; output $OUT"

# preparation, one step at a time: base images, IRs, the split
for rs in "${RELEASES[@]}"; do
  read -r rel series <<<"$rs"
  [ -f "$ROOT/.cache/vm/base-$series.qcow2" ] || "$TVM" prepare "$series" 2>&1 | stamp >> "$OUT/run.log"
  irf=$("$TVM" ir "$rel")
  mkdir -p "$OUT/$rel"
  args=(-ir "$irf" -groups "$GROUPS_FILE" -times "$STATE/timings-$series.json")
  [ "$AUTO" -gt 0 ] && args+=(-auto "$AUTO")
  "${SPEC[@]}" groups "${args[@]}" > "$OUT/$rel/groups.json"
  log "$rel: $(jq -r '[.groups[] | "\(.name)\(if .core then "+core" else "" end) \(.plugins | length)"] | join(", ")' "$OUT/$rel/groups.json"), weights from $(jq -r .weights_from "$OUT/$rel/groups.json")"
  jq -r '.notes[]? | "  note: " + .' "$OUT/$rel/groups.json" | stamp | tee -a "$OUT/run.log"
done

# the slots: one per group and one conflict check per release
SLOTS=() # "slot release series group kind"
n=$FIRST
for rs in "${RELEASES[@]}"; do
  read -r rel series <<<"$rs"
  while IFS= read -r g; do SLOTS+=("$n $rel $series $g test"); n=$((n + 1)); done < <(jq -r '.groups[].name' "$OUT/$rel/groups.json")
  SLOTS+=("$n $rel $series conflicts conflicts"); n=$((n + 1))
done
[ "$n" -le 100 ] || die "$((n - FIRST)) VMs from slot $FIRST do not fit into slots 1 to 99"

# teardown: every slot's VM and proxy, also after an interrupt
PIDS=()
teardown() {
  local p s
  for p in "${PIDS[@]}"; do kill -TERM -- "-$p" 2>/dev/null || true; done
  for s in "${SLOTS[@]}"; do TESTVM_SLOT="${s%% *}" "$TVM" down >/dev/null 2>&1 || true; done
}
trap 'teardown; exit 130' INT TERM
trap teardown EXIT

# default test: the contract test of the group's packages
contract_test() {
  local irf no_core=()
  irf=$("$TVM" ir "$RELEASE")
  [ "$CORE" = 1 ] || no_core=(--no-core)
  local rc=0
  "$TVM" contract "$irf" "${no_core[@]}" --out "$GROUP_DIR/group.verified.json" || rc=$?
  rm -rf "$GROUP_DIR/cover"; cp -r "$("$TVM" lockfile | sed 's/\.lock$//')/cover" "$GROUP_DIR/cover"
  return "$rc"
}

# one slot: start, test (or only the installation), down; result.json.
# Output goes to the caller (the slot's log).
run_slot() {
  local slot="$1" rel="$2" series="$3" group="$4" kind="$5" dir="$OUT/$2/$4"
  export TESTVM_SLOT="$slot"
  local lock vm
  lock=$("$TVM" lockfile); vm="${lock%.lock}"
  # held until this function ends; the VM and proxy inherit it, so a slot
  # whose VM still runs stays locked
  exec 9>"$lock"
  flock -n 9 || { echo "slot $slot is in use ($lock)"; return 1; }
  local list core=0
  if [ "$kind" = conflicts ]; then
    list=$(jq -r '.all | join(" ")' "$OUT/$rel/groups.json")
  else
    list=$(jq -r --arg g "$group" '.groups[] | select(.name == $g) | .plugins | join(" ")' "$OUT/$rel/groups.json")
    core=$(jq -r --arg g "$group" '.groups[] | select(.name == $g) | if .core then 1 else 0 end' "$OUT/$rel/groups.json")
  fi
  local args=(--release "$rel") rc=0 t_ready t_test installed="" failed=""
  [ -n "$list" ] && args+=(--plugins "$list")
  echo "slot $slot: $rel $group ($kind), core $core, packages: ${list:-none}"
  "$TVM" start "$series" "${args[@]}" || rc=$?
  t_ready=$EPOCHSECONDS
  env_of() { sed -n "s/^$1=//p" "$vm/state.env"; }
  if [ "$rc" = 0 ]; then
    installed=$(env_of plugins); failed=$(env_of failed)
    if [ "$kind" = test ]; then
      echo "test begins"
      (
        export RELEASE="$rel" SERIES="$series" GROUP="$group" CORE="$core" GROUP_DIR="$dir"
        export PLUGINS="$installed" FAILED="$failed"
        TESTVM_URL=$(env_of url) TESTVM_KEYFILE=$(env_of keyfile) TESTVM_SLOW=$(env_of slow)
        export TESTVM_URL TESTVM_KEYFILE TESTVM_SLOW
        if [ -n "$TEST" ]; then bash -c "$TEST"; else contract_test; fi
      ) || rc=$?
      echo "test ends, exit $rc"
    fi
  fi
  t_test=$EPOCHSECONDS
  "$TVM" down || true
  jq -n --argjson slot "$slot" --arg release "$rel" --arg series "$series" --arg group "$group" --arg kind "$kind" \
    --argjson core "$core" --arg installed "$installed" --arg failed "$failed" --argjson rc "$rc" \
    --argjson ready "$((t_ready - T0))" --argjson tested "$((t_test - T0))" --argjson finished "$((EPOCHSECONDS - T0))" \
    '{slot: $slot, release: $release, series: $series, group: $group, kind: $kind, core: ($core == 1),
      installed: ($installed | split(",") | map(select(. != ""))), failed: ($failed | split(",") | map(select(. != ""))),
      exit: $rc, ready_at: $ready, tested_at: $tested, done_at: $finished}' > "$dir/result.json"
  return "$rc"
}
export OUT TVM T0 TEST

set -m # each slot in its own process group, so teardown reaches its children
for s in "${SLOTS[@]}"; do
  read -r slot rel series group kind <<<"$s"
  mkdir -p "$OUT/$rel/$group"
  (
    run_slot "$slot" "$rel" "$series" "$group" "$kind" 2>&1 | stamp > "$OUT/$rel/$group/log"
    exit "${PIPESTATUS[0]}"
  ) &
  PIDS+=("$!")
done
set +m
log "${#SLOTS[@]} VMs started (slots $FIRST to $((n - 1)))"
bad=0
for i in "${!PIDS[@]}"; do
  wait "${PIDS[$i]}" || { bad=1; log "slot ${SLOTS[$i]%% *} (${SLOTS[$i]#* }) failed: see its log"; }
done
PIDS=()
log "all VMs done"

# phases of one slot from its log (seconds since the run started), by the
# lines tools/testvm.sh prints
at() { awk -v p="$2" '$0 ~ p { print $1; exit }' "$1"; }
phases() { # phases DIR -> boot update install test total
  local l="$1/log" s up key runs inst te dn
  s=$(awk 'NR == 1 { print $1 }' "$l")
  up=$(at "$l" "web GUI up"); key=$(at "$l" "API key: /")
  runs=$(at "$l" "VM runs "); inst=$(at "$l" "plugins: [0-9]+ installed")
  te=$(jq -r .tested_at "$1/result.json"); dn=$(jq -r .done_at "$1/result.json")
  local ready; ready=$(jq -r .ready_at "$1/result.json")
  printf '%s %s %s %s %s\n' "$(( ${key:-$up} - s ))" "$(( ${runs:-${key:-$s}} - ${key:-$s} ))" \
    "$(( ${inst:-$ready} - ${runs:-${key:-$s}} ))" "$(( te - ready ))" "$(( dn - s ))"
}

# per release: plugins that did not install, the conflict check, merge
for rs in "${RELEASES[@]}"; do
  read -r rel series <<<"$rs"
  rdir="$OUT/$rel" dirs=() runs="[]"
  for s in "${SLOTS[@]}"; do
    read -r slot r _ group kind <<<"$s"
    [ "$r" = "$rel" ] || continue
    res="$rdir/$group/result.json"
    [ -f "$res" ] || { log "$rel $group: no result"; bad=1; continue; }
    f=$(jq -r '.failed | join(",")' "$res")
    if [ -n "$f" ]; then log "$rel $group: plugins not installed: $f"; bad=1; fi
    if [ "$kind" = conflicts ]; then
      log "$rel conflict check (all plugins on one VM): $(jq -r '.installed | length' "$res") installed, failed: ${f:-none}"
      jq '{installed, failed}' "$res" > "$rdir/conflicts.json"
      continue
    fi
    dirs+=("$rdir/$group")
    # measured times for the next split: each plugin's installation, the
    # group's test
    read -r _ _ _ ttest _ < <(phases "$rdir/$group")
    inst=$(sed -nE 's/^ *[0-9]+ plugin ([^ ]+): ([0-9]+) s$/\1 \2/p' "$rdir/$group/log" | jq -R -s 'split("\n") | map(select(. != "") | split(" ") | {(.[0]): (.[1] | tonumber)}) | add // {}')
    runs=$(jq --argjson r "$(jq --argjson inst "$inst" --argjson t "$ttest" '{core, plugins: .installed, install: $inst, test: $t}' "$res")" '. + [$r]' <<<"$runs")
  done
  jq -n --arg release "$rel" --argjson runs "$runs" '{release: $release, runs: $runs}' > "$rdir/timings.json"
  [ "$bad" = 0 ] && cp "$rdir/timings.json" "$STATE/timings-$series.json"
  t=$EPOCHSECONDS
  if [ -n "$MERGE" ]; then
    RELEASE="$rel" SERIES="$series" RELEASE_DIR="$rdir" GROUP_DIRS="${dirs[*]}" bash -c "$MERGE" 2>&1 | stamp | tee "$rdir/merge.log" || bad=1
  else
    irf=$("$TVM" ir "$rel")
    parts=() covs=()
    for d in "${dirs[@]}"; do parts+=("$d/group.verified.json"); covs+=("$d/cover"); done
    {
      "${SPEC[@]}" merge-verified -ir "$irf" "${parts[@]}" && rc=0 || rc=$?
      cp "${irf%.ir.json}.verified.json" "$rdir/verified.json" 2>/dev/null || true
      go tool covdata textfmt -i="$(IFS=,; echo "${covs[*]}")" -o "$rdir/contract.out"
      "$ROOT/tools/ci/coverage.sh" contract "$rdir/contract.out" "$rdir/coverage" || rc=1
      exit "$rc"
    } 2>&1 | stamp | tee "$rdir/merge.log" || bad=1
  fi
  log "$rel merged in $((EPOCHSECONDS - t)) s"
done

{
  echo
  echo "| release | group | plugins | boot+key s | update s | install s | test s | VM total s | exit |"
  echo "|---|---|---:|---:|---:|---:|---:|---:|---:|"
  for s in "${SLOTS[@]}"; do
    read -r slot rel _ group _ <<<"$s"
    d="$OUT/$rel/$group"
    [ -f "$d/result.json" ] || continue
    read -r b u i te tot < <(phases "$d")
    printf '| %s | %s%s | %s | %s | %s | %s | %s | %s | %s |\n' "$rel" "$group" "$(jq -r 'if .core then " +core" else "" end' "$d/result.json")" \
      "$(jq -r '.installed | length' "$d/result.json")" "$b" "$u" "$i" "$te" "$tot" "$(jq -r .exit "$d/result.json")"
  done
  echo
  echo "wall time: $((EPOCHSECONDS - T0)) s"
} | tee "$OUT/timing.md"
if [ "$bad" = 0 ]; then log "PASSED"; else log "FAILED"; fi
exit "$bad"
