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
failed='{"url":"https://example/run/9","created":"2026-11-19T05:17:00Z","jobs":[{"name":"contract (26.7.9)","steps":["Contract test 26.7.9"]}]}'
cov='[{"release":"26.7.10","from":"26.7.9","regressions":["/api/firewall/source_nat/get: typed (base) -> untyped"]}]'

run() { # run [VAR=value ...]: alerts.sh check with healthy defaults
  env WD_STATE=active WD_LAST_SUCCESS="$(hours_ago 10)" WD_FAILED_RUN=null WD_COVERAGE='[]' ALERTS_ISSUES='[]' WD_CREATED="$(hours_ago 2000)" WD_INDEX="$empty_index" "$@" "$A" check
}

out=$(run)
check "healthy: no write at all" "$out" "gh " absent
check "healthy: reported" "$out" "nothing needs a person"

out=$(run WD_LAST_SUCCESS="$(hours_ago 30)" WD_FAILED_RUN="$failed")
check "one failed day: no message" "$out" "gh " absent
out=$(run WD_LAST_SUCCESS="$(hours_ago 167)" WD_FAILED_RUN="$failed")
check "just under 7 days: no message" "$out" "gh " absent

out=$(run WD_LAST_SUCCESS="$(hours_ago 170)" WD_FAILED_RUN="$failed")
check "stale: opens one issue" "$out" "gh issue create -R example/spec --title Needs you: spec --label needs-you"
check "stale: says since when" "$out" "7 days ago"
check "stale: names the failed job and step" "$out" '- job `contract (26.7.9)`, step `Contract test 26.7.9`'
check "stale: links the run" "$out" "https://example/run/9"
check "stale: says what happens if ignored" "$out" "Everything published keeps working"
check "stale: says how to fix" "$out" "Start a Claude Code session"

out=$(run WD_LAST_SUCCESS="$(hours_ago 170)" ALERTS_ISSUES="$open_issue")
check "stale with open issue: edited, not re-posted" "$out" "gh issue edit 3"
check "stale with open issue: no second issue" "$out" "issue create" absent
check "no failed run: says it did not run" "$out" "did not run at all"

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

# used by the client repository: another workflow, no typing-loss check
out=$(run WD_WORKFLOW=spec-update.yml WD_STATE=disabled_inactivity GITHUB_REPOSITORY=example/client)
check "other workflow: re-enables that one" "$out" "gh api -X PUT repos/example/client/actions/workflows/spec-update.yml/enable"
out=$(run WD_WORKFLOW=spec-update.yml WD_LAST_SUCCESS="" GITHUB_REPOSITORY=example/client)
check "other workflow: named in the issue" "$out" 'No successful scheduled run of `spec-update.yml`'
check "other workflow: title names the repository" "$out" "--title Needs you: client"
out=$(env WD_STATE=active WD_LAST_SUCCESS="$(hours_ago 10)" WD_FAILED_RUN=null ALERTS_ISSUES='[]' WD_COVERAGE_CHECK=0 GITHUB_REPOSITORY=example/client "$A" check)
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
