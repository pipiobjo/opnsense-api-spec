#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/alerts.sh without GitHub: inputs come from variables,
# write calls are printed (ALERTS_DRY_RUN=1) and checked.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
A="$HERE/alerts.sh"
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

empty_index="${TMPDIR:-/tmp}/alerts-empty-index.json"
echo '{"releases":[]}' > "$empty_index"
trap 'rm -f "$empty_index"' EXIT
hours_ago() { date -u -d "@$((ALERTS_NOW - $1 * 3600))" +%FT%TZ; }
open_issue='[{"number":3,"state":"OPEN","labels":["needs-you"]}]'
closed_issue='[{"number":2,"state":"CLOSED","labels":["needs-you"]}]'
# a failed scheduled run as GitHub lists it (WD_RUNS) and shows it (WD_RUN_VIEW)
failed_runs='[{"databaseId":9,"status":"completed","conclusion":"failure","createdAt":"2026-11-19T05:17:00Z"}]'
failed_view='{"9":{"url":"https://example/run/9","createdAt":"2026-11-19T05:17:00Z","conclusion":"failure","jobs":[
  {"name":"build","conclusion":"success","steps":[{"name":"Gates","conclusion":"success"}]},
  {"name":"contract (26.7.9)","conclusion":"failure","steps":[{"name":"Set up job","conclusion":"success"},{"name":"Contract test 26.7.9","conclusion":"failure"},{"name":"Summary","conclusion":"success"}]},
  {"name":"report","conclusion":"skipped","steps":[]}]}}'
cov='[{"release":"26.7.10","from":"26.7.9","regressions":["/api/firewall/source_nat/get: typed (base) -> untyped"]}]'

run() { # run [VAR=value ...]: alerts.sh check with healthy defaults (output and messages)
  env WD_STATE=active WD_LAST_SUCCESS="$(hours_ago 10)" WD_RUNS='[]' WD_COVERAGE='[]' ALERTS_ISSUES='[]' WD_CREATED="$(hours_ago 2000)" WD_INDEX="$empty_index" "$@" "$A" check 2>&1 || echo "alerts.sh exited $?"
}

out=$(run)
check "healthy: no write at all" "$out" "gh " absent
check "healthy: the check succeeds" "$out" "alerts.sh exited" absent
check "healthy: reported" "$out" "nothing needs a person"

out=$(run WD_LAST_SUCCESS="$(hours_ago 30)" WD_RUNS="$failed_runs" WD_RUN_VIEW="$failed_view")
check "one failed day: no message" "$out" "gh " absent
out=$(run WD_LAST_SUCCESS="$(hours_ago 167)" WD_RUNS="$failed_runs" WD_RUN_VIEW="$failed_view")
check "just under 7 days: no message" "$out" "gh " absent

out=$(run WD_LAST_SUCCESS="$(hours_ago 170)" WD_RUNS="$failed_runs" WD_RUN_VIEW="$failed_view")
check "stale: opens one issue" "$out" "gh issue create -R example/spec --title Needs you: spec --label needs-you"
check "stale: says since when" "$out" "7 days ago"
check "stale: names the failed job and step" "$out" '- job `contract (26.7.9)` failure, step `Contract test 26.7.9`'
check "stale: only jobs that did not succeed" "$out" 'job `build`' absent
check "stale: a skipped job is not named" "$out" 'job `report`' absent
check "stale: links the run" "$out" "https://example/run/9"
check "stale: says what happens if ignored" "$out" "Everything published keeps working"
check "stale: says how to fix" "$out" "Start a Claude Code session"

out=$(run WD_LAST_SUCCESS="$(hours_ago 170)" ALERTS_ISSUES="$open_issue")
check "stale with open issue: edited, not re-posted" "$out" "gh issue edit 3"
check "stale with open issue: no second issue" "$out" "issue create" absent
check "no failed run: says it did not run" "$out" "did not run (disabled"

out=$(run WD_LAST_SUCCESS="$(hours_ago 170)" ALERTS_ISSUES="$closed_issue")
check "closed issue is not reused" "$out" "issue create"

out=$(run ALERTS_ISSUES="$open_issue")
check "recovered: closes the issue" "$out" "gh issue close 3"

out=$(run WD_STATE=disabled_inactivity)
check "disabled for inactivity: re-enabled" "$out" "gh api -X PUT repos/example/spec/actions/workflows/pipeline.yml/enable"
check "disabled for inactivity: no issue" "$out" "issue create" absent
out=$(run WD_STATE=disabled_manually)
check "disabled by hand: not re-enabled" "$out" "/enable" absent
check "disabled by hand: reported" "$out" "disabled_manually"
out=$(run WD_LAST_SUCCESS="")
check "no success at all: reported" "$out" "No successful scheduled run"
out=$(run WD_LAST_SUCCESS="" WD_CREATED="$(hours_ago 7)")
check "new workflow, not run yet: no message" "$out" "gh " absent
out=$(run WD_LAST_SUCCESS="" WD_CREATED="$(hours_ago 170)")
check "never succeeded in 7 days: opens the issue" "$out" "gh issue create"

out=$(run WD_COVERAGE="$cov")
check "coverage: opens the issue" "$out" "--label needs-you"
check "coverage: names release and operation" "$out" '/api/firewall/source_nat/get: typed (base) -> untyped'
check "coverage: names both releases" "$out" "OPNsense 26.7.10 has operations or fields that were typed in 26.7.9"
check "coverage alone: no stale section" "$out" "Publishing has stopped" absent
out=$(run WD_COVERAGE="$cov" WD_LAST_SUCCESS="$(hours_ago 200)")
check "both conditions: one issue, two sections" "$out" "check: 2 condition(s)"
out=$(run WD_COVERAGE='[]' ALERTS_ISSUES="$open_issue")
check "coverage gone: closes the issue" "$out" "gh issue close 3"

# which run explains a stale workflow: the newest scheduled run after the
# last success that did not succeed, whatever the reason
last="$(hours_ago 170)"
out=$(run WD_LAST_SUCCESS="$last" WD_RUNS='[{"databaseId":5,"status":"completed","conclusion":"failure","createdAt":"2026-10-20T05:17:00Z"}]' \
  WD_RUN_VIEW='{"5":{"url":"https://example/run/5","createdAt":"2026-10-20T05:17:00Z","conclusion":"failure","jobs":[]}}')
check "why: a failure before the last success is not named" "$out" "example/run/5" absent
check "why: then nothing ran since" "$out" "did not run (disabled"
runs='[{"databaseId":7,"status":"completed","conclusion":"failure","createdAt":"2026-11-15T05:17:00Z"},
       {"databaseId":8,"status":"completed","conclusion":"timed_out","createdAt":"2026-11-18T05:17:00Z"},
       {"databaseId":9,"status":"in_progress","conclusion":"","createdAt":"2026-11-20T05:17:00Z"},
       {"databaseId":6,"status":"completed","conclusion":"skipped","createdAt":"2026-11-19T05:17:00Z"}]'
views='{"7":{"url":"https://example/run/7","createdAt":"2026-11-15T05:17:00Z","conclusion":"failure","jobs":[]},
        "8":{"url":"https://example/run/8","createdAt":"2026-11-18T05:17:00Z","conclusion":"timed_out","jobs":[
          {"name":"contract (26.7.9)","conclusion":"cancelled","steps":[{"name":"Contract test 26.7.9","conclusion":"cancelled"}]}]}}'
out=$(run WD_LAST_SUCCESS="$last" WD_RUNS="$runs" WD_RUN_VIEW="$views")
check "why: the newest run that did not succeed (timed out)" "$out" "https://example/run/8 (2026-11-18T05:17:00Z, timed_out)"
check "why: a cancelled job is named" "$out" '- job `contract (26.7.9)` cancelled, step `Contract test 26.7.9`'
check "why: an older failure is not named" "$out" "example/run/7" absent
out=$(run WD_LAST_SUCCESS="$last" WD_RUNS='[{"databaseId":4,"status":"completed","conclusion":"startup_failure","createdAt":"2026-11-19T05:17:00Z"}]' \
  WD_RUN_VIEW='{"4":{"url":"https://example/run/4","createdAt":"2026-11-19T05:17:00Z","conclusion":"startup_failure","jobs":[]}}')
check "why: a run that could not start is named" "$out" "https://example/run/4 (2026-11-19T05:17:00Z, startup_failure)"
check "why: and says no job ran" "$out" "no job ran"

# watched workflows of other repositories (WD_WATCH): read-only, reported
# in the own issue
W='example/client:spec-update.yml'
ok_watch='{"example/client:spec-update.yml":{"state":"active","last_success":"'"$(hours_ago 20)"'","runs":[]}}'
out=$(run WD_WATCH="$W" WD_WATCH_DATA="$ok_watch")
check "watch: healthy, no write" "$out" "gh " absent
stale_watch='{"example/client:spec-update.yml":{"state":"active","last_success":"'"$(hours_ago 200)"'",
  "runs":[{"databaseId":3,"status":"completed","conclusion":"failure","createdAt":"'"$(hours_ago 30)"'"}],
  "run_view":{"3":{"url":"https://example/client/run/3","createdAt":"x","conclusion":"failure","jobs":[{"name":"integration (26.7)","conclusion":"failure","steps":[{"name":"Integration test","conclusion":"failure"}]}]}}}}'
out=$(run WD_WATCH="$W" WD_WATCH_DATA="$stale_watch")
check "watch: stale opens the issue in the own repository" "$out" "gh issue create -R example/spec"
check "watch: names repository and workflow" "$out" '## `spec-update.yml` in [example/client]'
check "watch: names the failed job there" "$out" '- job `integration (26.7)` failure, step `Integration test`'
check "watch: own workflow not reported" "$out" "Publishing has stopped" absent
out=$(run WD_WATCH="$W" WD_WATCH_DATA='{"example/client:spec-update.yml":{"state":"disabled_inactivity","last_success":"'"$(hours_ago 20)"'","runs":[]}}')
check "watch: disabled for inactivity is reported" "$out" "was disabled by GitHub for inactivity"
check "watch: and never re-enabled from here" "$out" "/enable" absent
out=$(run WD_WATCH="$W" WD_WATCH_DATA='{"example/client:spec-update.yml":{"state":"active","last_success":"","created":"'"$(hours_ago 30)"'","runs":[]}}')
check "watch: a new workflow that has not run yet is fine" "$out" "gh " absent
out=$(run WD_WATCH="$W example/client:watchdog.yml" WD_WATCH_DATA='{"example/client:spec-update.yml":{"error":"x"},"example/client:watchdog.yml":{"state":"active","last_success":"'"$(hours_ago 300)"'","runs":[]}}')
check "watch: unreadable is a warning" "$out" "::warning::could not read spec-update.yml in example/client"
check "watch: unreadable fails the check" "$out" "alerts.sh exited 1"
check "watch: the other pair is still judged" "$out" '## `watchdog.yml` in [example/client]'
out=$(run WD_WATCH="example/client")
check "watch: a malformed entry is a warning" "$out" "is not owner/repo:workflow.yml"
check "watch: and fails the check" "$out" "alerts.sh exited 1"
out=$(run ALERTS_ISSUES="$open_issue" WD_WATCH="$W" WD_WATCH_DATA='{"example/client:spec-update.yml":{"error":"x"}}')
check "watch: unreadable alone does not keep an issue open" "$out" "gh issue close 3"

# promotion (WD_PROMOTE): dev ahead of main for 7 days and no run in progress
P=(WD_PROMOTE=promote.yml)
cmp_old='{"ahead_by":2,"status":"ahead","oldest":"'"$(hours_ago 200)"'","head":"bbbbbbbbbbbbbbbbbbbb"}'
prun_failed='{"databaseId":21,"status":"completed","conclusion":"failure","headSha":"bbbbbbbbbbbbbbbbbbbb","url":"https://example/run/21","createdAt":"x"}'
pview='{"url":"https://example/run/21","createdAt":"2026-11-12T10:00:00Z","conclusion":"failure","jobs":[{"name":"dry-run / build","conclusion":"failure","steps":[{"name":"Gates","conclusion":"failure"}]},{"name":"main","conclusion":"skipped","steps":[]}]}'
out=$(run "${P[@]}" WD_PROMOTE_COMPARE="$cmp_old" WD_PROMOTE_RUN="$prun_failed" WD_PROMOTE_VIEW="$pview")
check "promotion: failed for 7 days opens the issue" "$out" "gh issue create"
check "promotion: says what" "$out" '`dev` has 2 commit(s) that `main` lacks'
check "promotion: names the failed job" "$out" '- job `dry-run / build` failure, step `Gates`'
check "promotion: names the commit" "$out" "on bbbbbbbbbbbb, did not succeed"
out=$(run "${P[@]}" WD_PROMOTE_COMPARE='{"ahead_by":2,"status":"ahead","oldest":"'"$(hours_ago 100)"'","head":"b"}' WD_PROMOTE_RUN="$prun_failed" WD_PROMOTE_VIEW="$pview")
check "promotion: younger than 7 days, no message" "$out" "gh " absent
out=$(run "${P[@]}" WD_PROMOTE_COMPARE='{"ahead_by":0,"status":"identical","oldest":"","head":""}' WD_PROMOTE_RUN="$prun_failed")
check "promotion: nothing ahead, no message" "$out" "gh " absent
out=$(run "${P[@]}" WD_PROMOTE_COMPARE="$cmp_old" WD_PROMOTE_RUN='{"databaseId":22,"status":"in_progress","conclusion":"","headSha":"b","url":"https://example/run/22","createdAt":"x"}')
check "promotion: a run in progress, no message" "$out" "gh " absent
out=$(run "${P[@]}" WD_PROMOTE_COMPARE="$cmp_old" WD_PROMOTE_RUN='{"databaseId":20,"status":"completed","conclusion":"success","headSha":"aaaaaaaaaaaaaaaa","url":"https://example/run/20","createdAt":"x"}')
check "promotion: success on an older commit is reported" "$out" "no run tested the head of \`dev\`, bbbbbbbbbbbb"
out=$(run "${P[@]}" WD_PROMOTE_COMPARE='{"ahead_by":1,"status":"diverged","oldest":"'"$(hours_ago 200)"'","head":"b"}' WD_PROMOTE_RUN="$prun_failed" WD_PROMOTE_VIEW="$pview")
check "promotion: diverged says rebase" "$out" "rebase \`dev\` on \`main\`"
out=$(run WD_PROMOTE_COMPARE="$cmp_old" WD_PROMOTE_RUN="$prun_failed" WD_PROMOTE_VIEW="$pview")
check "promotion: off without WD_PROMOTE" "$out" "gh " absent
check "promotion: off reads nothing" "$out" "alerts.sh exited" absent

# used by the client repository: another workflow, no typing-loss check
out=$(run WD_WORKFLOW=spec-update.yml WD_STATE=disabled_inactivity GITHUB_REPOSITORY=example/client)
check "other workflow: re-enables that one" "$out" "gh api -X PUT repos/example/client/actions/workflows/spec-update.yml/enable"
out=$(run WD_WORKFLOW=spec-update.yml WD_LAST_SUCCESS="" GITHUB_REPOSITORY=example/client)
check "other workflow: named in the issue" "$out" 'No successful scheduled run of `spec-update.yml`'
check "other workflow: title names the repository" "$out" "--title Needs you: client"
out=$(env WD_STATE=active WD_LAST_SUCCESS="$(hours_ago 10)" WD_RUNS='[]' ALERTS_ISSUES='[]' WD_COVERAGE_CHECK=0 GITHUB_REPOSITORY=example/client "$A" check)
check "no coverage check: healthy without reading releases" "$out" "nothing needs a person"

# missing data: unavailable for 7 days opens the issue, younger does not
cat > "${TMPDIR:-/tmp}/alerts-missing.json" <<JSON
{"releases":[{"release":"26.7.9","source_unavailable":{"reason":"os-foo 1.2 not served","since":"$(hours_ago 200)"}}],
 "unavailable":[{"release":"27.1","reason":"core package not served","since":"$(hours_ago 170)"},
                {"release":"27.1.1","reason":"core package not served","since":"$(hours_ago 5)"}]}
JSON
out=$(run WD_INDEX="${TMPDIR:-/tmp}/alerts-missing.json")
check "missing: opens the issue" "$out" "gh issue create"
check "missing: names a new series" "$out" "OPNsense 27.1: never built since"
check "missing: names a kept build" "$out" "OPNsense 26.7.9: kept on an old build"
check "missing: younger than 7 days not named" "$out" "27.1.1" absent
out=$(run WD_INDEX="${TMPDIR:-/tmp}/alerts-missing.json" WD_COVERAGE_CHECK=0)
check "missing: not checked without the coverage check" "$out" "gh " absent
rm -f "${TMPDIR:-/tmp}/alerts-missing.json"

# a state that cannot be read is an unread input, not "nothing missing"
# (finding OPS-4); here without WD_COVERAGE, so the typing-loss condition
# reads the index too
out=$(env WD_STATE=active WD_LAST_SUCCESS="$(hours_ago 10)" WD_RUNS='[]' ALERTS_ISSUES='[]' WD_CREATED="$(hours_ago 2000)" \
  WD_INDEX="${TMPDIR:-/tmp}/alerts-no-such-index.json" "$A" check 2>&1 || echo "alerts.sh exited $?")
check "index unreadable: missing data not judged" "$out" "::warning::could not read the published state; missing data not judged"
check "index unreadable: typing loss not judged" "$out" "::warning::could not read the releases' quality reports"
check "index unreadable: fails the check" "$out" "alerts.sh exited 1"
check "index unreadable: no issue written" "$out" "gh issue" absent

# accepted entries that no newest contract test needed (index.json
# unused_accepted, computed by the generator)
cat > "${TMPDIR:-/tmp}/alerts-unused.json" <<'JSON'
{"releases":[],"unused_accepted":["/api/u/get shape 25.7.* match \"HTTP 500\""]}
JSON
out=$(run WD_INDEX="${TMPDIR:-/tmp}/alerts-unused.json")
check "unused: opens the issue" "$out" "gh issue create"
check "unused: names the entry" "$out" '- `/api/u/get shape 25.7.* match "HTTP 500"`'
check "unused: says what to do" "$out" "remove them"
rm -f "${TMPDIR:-/tmp}/alerts-unused.json"

# a stale threshold per watched entry (a weekly workflow is not stale after 8 days)
weekly='{"example/client:spec-update.yml":{"state":"active","last_success":"'"$(hours_ago 200)"'","runs":[]}}'
out=$(run WD_WATCH="example/client:spec-update.yml:840" WD_WATCH_DATA="$weekly")
check "watch hours: under its own threshold, no message" "$out" "gh " absent
check "watch hours: the check succeeds" "$out" "alerts.sh exited" absent
out=$(run WD_WATCH="example/client:spec-update.yml:190" WD_WATCH_DATA="$weekly")
check "watch hours: over its own threshold, reported" "$out" '## `spec-update.yml` in [example/client]'
out=$(run WD_WATCH="example/client:spec-update.yml:7d")
check "watch hours: not a number is a malformed entry" "$out" "is not owner/repo:workflow.yml[:hours]"

# which releases count as new for the typing-loss condition: first_seen of
# the current catalog state within 14 days; no first_seen never counts
cat > "${TMPDIR:-/tmp}/alerts-index.json" <<'JSON'
{"releases":[
 {"release":"26.7.10","first_seen":"2026-11-19"},
 {"release":"26.7.9","first_seen":"2026-11-02"},
 {"release":"26.1.11"},
 {"release":"26.7.8","first_seen":"2026-11-06"}]}
JSON
out=$(WD_INDEX="${TMPDIR:-/tmp}/alerts-index.json" "$A" recent-releases)
check "recent: first seen yesterday" "$out" "26.7.10"
check "recent: 14 days ago counts" "$out" "26.7.8"
check "recent: older does not" "$out" "26.7.9" absent
check "recent: no date never counts" "$out" "26.1.11" absent
rm -f "${TMPDIR:-/tmp}/alerts-index.json"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
