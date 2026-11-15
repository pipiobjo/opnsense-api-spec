#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/alerts.sh without GitHub: inputs come from variables and
# fixtures, write calls are printed (ALERTS_DRY_RUN=1) and checked.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
A="$HERE/alerts.sh"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
export ALERTS_DRY_RUN=1 GITHUB_REPOSITORY=example/spec
export ALERTS_NOW; ALERTS_NOW=$(date -u -d 2026-11-20T12:00:00Z +%s)
fails=0

check() { # check NAME OUTPUT PATTERN [absent]
  if [ "${4:-}" = absent ]; then
    if grep -qF -- "$3" <<<"$2"; then echo "FAIL $1: unexpected '$3'"; fails=$((fails + 1)); else echo "ok   $1"; fi
  else
    if grep -qF -- "$3" <<<"$2"; then echo "ok   $1"; else echo "FAIL $1: missing '$3'"; printf '     %s\n' "$2"; fails=$((fails + 1)); fi
  fi
}

# ---------------------------------------------------------------- coverage
# 26.7.1: unchanged, has an old regression (must not be reported)
# 26.7.2: rebuilt (new catalog state), has a regression
# 26.7.3: new release, no regression
D="$T/dist"; mkdir -p "$D/26.7.1" "$D/26.7.2" "$D/26.7.3"
echo '{"releases":[{"release":"26.7.1","current":"26.7.1~a"},{"release":"26.7.2","current":"26.7.2~b2"},{"release":"26.7.3","current":"26.7.3~c"}]}' > "$D/index.json"
echo '{"releases":[{"release":"26.7.1","current":"26.7.1~a"},{"release":"26.7.2","current":"26.7.2~b1"}]}' > "$D/.index.prev.json"
q() { echo "{\"from\":\"$1\",\"to\":\"$2\",\"regressions\":$3,\"new_gaps\":{},\"framework_changed\":$4}" > "$D/$2/$1..$2.quality.json"; }
q 26.7 26.7.1 '["/api/old/thing/get: typed (base) -> untyped"]' '[]'
q 26.7.1 26.7.2 '["/api/firewall/source_nat/get: typed (base) -> untyped"]' '["controllers/OPNsense/Base/ApiControllerBase.php"]'
q 26.7.2 26.7.3 '[]' '[]'

out=$(DIST="$D" ALERTS_ISSUES='[]' "$A" coverage)
check "coverage: rebuilt release reported" "$out" "--title Coverage dropped in 26.7.2"
check "coverage: regression listed" "$out" "/api/firewall/source_nat/get: typed (base) -> untyped"
check "coverage: framework change named" "$out" "Framework files changed: controllers/OPNsense/Base/ApiControllerBase.php"
check "coverage: unchanged release not reported" "$out" "/api/old/thing/get" absent
check "coverage: release without regressions not reported" "$out" "Coverage dropped in 26.7.3" absent
check "coverage: count" "$out" "coverage: 1 new report(s)"
key=$(grep -oE 'coverage:26\.7\.2:[0-9a-f]{16}' <<<"$out" | head -1)
known=$(jq -n --arg b "text <!-- $key -->" '[{number: 7, state: "CLOSED", title: "Coverage dropped in 26.7.2", body: $b, labels: ["attention"]}]')
out=$(DIST="$D" ALERTS_ISSUES="$known" "$A" coverage)
check "coverage: same regressions not reported twice" "$out" "issue create" absent
q 26.7.1 26.7.2 '["/api/firewall/source_nat/get: typed (base) -> untyped","/api/firewall/d_nat/get: typed (base) -> untyped"]' '[]'
out=$(DIST="$D" ALERTS_ISSUES="$known" "$A" coverage)
check "coverage: a different regression is a new report" "$out" "--title Coverage dropped in 26.7.2"
rm "$D/.index.prev.json"
out=$(DIST="$D" ALERTS_ISSUES='[]' "$A" coverage)
check "coverage: without a previous index every release counts" "$out" "coverage: 2 new report(s)"

# ---------------------------------------------------------------- watchdog
hours_ago() { date -u -d "@$((ALERTS_NOW - $1 * 3600))" +%FT%TZ; }
open_wd='[{"number":3,"state":"OPEN","title":"Pipeline not healthy","body":"","labels":["watchdog"]}]'

out=$(WD_STATE=active WD_LAST_SUCCESS=$(hours_ago 10) ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: healthy, no write" "$out" "gh " absent
check "watchdog: healthy reported" "$out" "watchdog: healthy"
out=$(WD_STATE=active WD_LAST_SUCCESS=$(hours_ago 80) ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: stale opens an issue" "$out" "--label watchdog"
check "watchdog: stale names the age" "$out" "80 hours ago"
out=$(WD_STATE=active WD_LAST_SUCCESS=$(hours_ago 71) ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: just under the limit is healthy" "$out" "issue create" absent
out=$(WD_STATE=active WD_LAST_SUCCESS=$(hours_ago 80) ALERTS_ISSUES="$open_wd" "$A" watchdog)
check "watchdog: stale with open issue comments" "$out" "gh issue comment 3"
check "watchdog: no second issue" "$out" "issue create" absent
out=$(WD_STATE=active WD_LAST_SUCCESS=$(hours_ago 5) ALERTS_ISSUES="$open_wd" "$A" watchdog)
check "watchdog: recovery closes the issue" "$out" "gh issue close 3"
out=$(WD_STATE=disabled_inactivity WD_LAST_SUCCESS=$(hours_ago 5) ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: disabled for inactivity is re-enabled" "$out" "gh api -X PUT repos/example/spec/actions/workflows/pipeline.yml/enable"
check "watchdog: and reported" "$out" "re-enabled it"
out=$(WD_STATE=disabled_manually WD_LAST_SUCCESS=$(hours_ago 5) ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: disabled by hand is not re-enabled" "$out" "/enable" absent
check "watchdog: but reported" "$out" "disabled_manually"
out=$(WD_STATE=active WD_LAST_SUCCESS="" ALERTS_ISSUES='[]' "$A" watchdog)
check "watchdog: no success at all is a problem" "$out" "No successful scheduled run"

# ---------------------------------------------------------------- digest
cat > "$T/digest.json" <<'EOF'
{"since":"2026-11-13T12:00:00Z",
 "runs":[
  {"workflow":"pipeline","event":"schedule","conclusion":"success","created_at":"2026-11-19T05:17:00Z"},
  {"workflow":"pipeline","event":"schedule","conclusion":"failure","created_at":"2026-11-18T05:17:00Z"},
  {"workflow":"ci","event":"push","conclusion":"success","created_at":"2026-11-18T10:00:00Z"},
  {"workflow":"Graph Update: go_modules in /.","event":"dynamic","conclusion":"success","created_at":"2026-11-18T10:00:00Z"}],
 "releases":[
  {"tag":"opnsense-26.7.10","updated":"2026-11-19T06:00:00Z"},
  {"tag":"opnsense-26.7.9","updated":"2026-11-01T06:00:00Z"},
  {"tag":"empty","updated":null},
  {"tag":"state","updated":"2026-11-19T06:00:00Z"}],
 "catalog":{"releases":[
  {"release":"26.7.9","series":"26.7","status":"final","current":"x9","compositions":[{"id":"x9"}]},
  {"release":"26.7.10","series":"26.7","status":"provisional","current":"x10","compositions":[{"id":"old"},{"id":"x10","verified":{"date":"2026-11-19T06:10:00Z","packages":["opnsense","os-frr"],"results":{"write-cycle/ok":160,"read/ok":900}}}]},
  {"release":"26.1.11","series":"26.1","status":"final","current":"y","compositions":[{"id":"y"}]}]},
 "issues":[{"number":5,"title":"Coverage dropped in 26.7.10","labels":["attention"]}],
 "annotations":[
  {"level":"warning","message":"no KVM on this runner"},
  {"level":"warning","message":"no KVM on this runner"},
  {"level":"failure","message":"Process completed with exit code 1."}]}
EOF
out=$("$A" digest-render < "$T/digest.json")
check "digest: run table" "$out" "| pipeline | 1 | 1 | 0 |"
check "digest: last scheduled success" "$out" "Last successful scheduled pipeline run: 2026-11-19T05:17:00Z"
check "digest: published this week" "$out" "[opnsense-26.7.10]("
check "digest: older release not listed" "$out" "[opnsense-26.7.9]" absent
check "digest: state is not a publication" "$out" "[state]" absent
check "digest: GitHub-internal runs left out" "$out" "Graph Update" absent
check "digest: newest release per series (version order, not text)" "$out" "| 26.7 | 26.7.10 | provisional | 2026-11-19 (2 packages) |"
check "digest: verification results" "$out" "write-cycle/ok 160"
check "digest: untested release" "$out" "| 26.1 | 26.1.11 | final | not tested | - |"
check "digest: warnings grouped" "$out" "- 2 x no KVM on this runner"
check "digest: failures are not warnings" "$out" "exit code 1" absent
check "digest: open issues" "$out" "- #5 Coverage dropped in 26.7.10 (attention)"
out=$(jq '.catalog = null | .runs = [] | .annotations = []' "$T/digest.json" | "$A" digest-render)
check "digest: no catalog yet" "$out" "No capability data published yet"
check "digest: no runs" "$out" "| (none) | | | |"
out=$(jq '.releases = [range(12) | {tag: "opnsense-26.1.\(.)", updated: "2026-11-19T06:00:00Z"}]' "$T/digest.json" | "$A" digest-render)
check "digest: many releases on one line, in version order" "$out" "12 releases updated (a new generator republishes all): 26.1.0, 26.1.1, 26.1.2,"
out=$(DIGEST_INPUT="$T/digest.json" ALERTS_ISSUES='[]' "$A" digest)
check "digest: created when none is open" "$out" "--title Weekly digest --label digest"
out=$(DIGEST_INPUT="$T/digest.json" ALERTS_ISSUES='[{"number":9,"state":"OPEN","title":"Weekly digest","body":"","labels":["digest"]}]' "$A" digest)
check "digest: rewritten in place" "$out" "gh issue edit 9"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
