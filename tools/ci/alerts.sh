#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# The one signal to a person (docs/operations.md, ADR 0012):
#
#   tools/ci/alerts.sh check
#
# Opens at most one issue labelled "needs-you", edits it while a condition
# lasts and closes it when none is left. Conditions:
#
#   stale     the last successful scheduled run of the workflow (pipeline.yml,
#             or WD_WORKFLOW) is older
#             than 7 days (WD_STALE_HOURS); a single failed run is not one;
#             a workflow that never succeeded counts only once it exists
#             for 7 days (a new repository has not had its first run yet)
#   disabled  the workflow is disabled and cannot be re-enabled here (a
#             workflow GitHub disabled for inactivity is re-enabled silently)
#   coverage  a release whose catalog state was first built in the last 14
#             days lost typing against the release before it (its
#             *.quality.json lists regressions); clears by itself 14 days
#             later
#   missing   a release on the mirror has had no current data for 7 days:
#             never built because its packages are not served (a new series
#             is the important case), or kept on an old build because a
#             rebuild could not get its packages (index.json "unavailable"
#             and "source_unavailable")
#
# A person never closes the issue; the next check after the cause is gone
# does. Nothing here is urgent: consumers keep working with the last
# published data.
#
# Tests: tools/ci/alerts_test.sh. Every read from GitHub can be replaced by
# a variable (ALERTS_ISSUES, WD_STATE, WD_LAST_SUCCESS, WD_CREATED,
# WD_FAILED_RUN, WD_COVERAGE, WD_INDEX) and ALERTS_DRY_RUN=1 prints the write calls instead of making
# them.
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
# Also used by other repositories of the project (opnsense-api-go checks out
# this script): the scheduled workflow to watch, and whether the typing-loss
# condition applies (it reads this repository's releases).
WORKFLOW="${WD_WORKFLOW:-pipeline.yml}"
COVERAGE="${WD_COVERAGE_CHECK:-1}"
NOW="${ALERTS_NOW:-$(date -u +%s)}"
STALE_HOURS="${WD_STALE_HOURS:-168}"
SERVER="${GITHUB_SERVER_URL:-https://github.com}"
LABEL=needs-you

# ghw: a write call to GitHub, or its printout in a dry run (with the body)
ghw() {
  if [ -n "${ALERTS_DRY_RUN:-}" ]; then
    echo "gh $*"
    local prev="" a
    for a in "$@"; do
      [ "$prev" = --body-file ] && sed 's/^/  | /' "$a"
      prev="$a"
    done
  else
    gh "$@"
  fi
}

open_issue() { # number of the open needs-you issue, or nothing
  if [ -n "${ALERTS_ISSUES+x}" ]; then
    jq -r --arg l "$LABEL" '[.[] | select((.labels | index($l)) and (.state | ascii_upcase) == "OPEN")][0].number // empty' <<<"$ALERTS_ISSUES"
  else
    gh issue list -R "$REPO" --label "$LABEL" --state open --limit 1 --json number --jq '.[0].number // empty'
  fi
}

epoch() { date -u -d "$1" +%s; }

# ------------------------------------------------------------ inputs

workflow_state() {
  if [ -n "${WD_STATE+x}" ]; then echo "$WD_STATE"; return; fi
  gh api "repos/$REPO/actions/workflows/$WORKFLOW" --jq .state
}

# workflow_created: when the workflow file was first registered; a workflow
# younger than STALE_HOURS that never succeeded is new, not stale
workflow_created() {
  if [ -n "${WD_CREATED+x}" ]; then echo "$WD_CREATED"; return; fi
  gh api "repos/$REPO/actions/workflows/$WORKFLOW" --jq .created_at
}

last_success() {
  if [ -n "${WD_LAST_SUCCESS+x}" ]; then echo "$WD_LAST_SUCCESS"; return; fi
  gh run list -R "$REPO" -w "$WORKFLOW" -e schedule -s success -L 1 --json updatedAt -q '.[0].updatedAt // ""'
}

# failed_run: the newest failed scheduled run with its failed jobs and steps,
# as {url, created, jobs: [{name, steps: [..]}]}, or null
failed_run() {
  if [ -n "${WD_FAILED_RUN+x}" ]; then echo "$WD_FAILED_RUN"; return; fi
  local id
  id=$(gh run list -R "$REPO" -w "$WORKFLOW" -e schedule -s failure -L 1 --json databaseId -q '.[0].databaseId // empty')
  [ -n "$id" ] || { echo null; return; }
  gh run view "$id" -R "$REPO" --json url,createdAt,jobs --jq '{url, created: .createdAt,
    jobs: [.jobs[] | select(.conclusion == "failure")
      | {name, steps: [.steps[] | select(.conclusion == "failure") | .name]}]}'
}

# coverage: regressions of releases created in the last COVERAGE_DAYS days
# (14), as [{release, from, regressions: [..]}]. Only new releases count:
# an old loss is recorded in its release and is not a reason to write to
# anyone, and a finished series would otherwise keep the issue open for
# ever.
coverage() {
  if [ -n "${WD_COVERAGE+x}" ]; then echo "$WD_COVERAGE"; return; fi
  local tags tag dir out="[]" f
  tags=$(recent_releases)
  dir=$(mktemp -d)
  for tag in $tags; do
    rm -f "$dir"/*.quality.json
    gh release download "opnsense-$tag" -R "$REPO" -p '*.quality.json' -D "$dir" >/dev/null 2>&1 || continue
    for f in "$dir"/*.quality.json; do
      [ -f "$f" ] || continue
      out=$(jq -c --slurpfile q "$f" --arg r "$tag" \
        '. + [$q[0] | select((.regressions // []) | length > 0) | {release: $r, from, regressions}]' <<<"$out")
    done
  done
  rm -rf "$dir"
  echo "$out"
}

# recent_releases: releases whose current catalog state the pipeline first
# built in the last COVERAGE_DAYS days (first_seen in the published
# index.json of the release "state"; WD_INDEX replaces it in tests).
# States built before first_seen existed have none and never count.
index_json() {
  if [ -n "${WD_INDEX:-}" ]; then
    cat "$WD_INDEX"
  else
    gh release download state -R "$REPO" -p index.json -O - 2>/dev/null || echo '{"releases":[]}'
  fi
}

# missing: releases without current data for STALE_HOURS, as
# [{release, kind, reason, since}]
missing() {
  local cutoff
  cutoff=$(date -u -d "@$((NOW - STALE_HOURS * 3600))" +%FT%TZ)
  index_json | jq -c --arg cut "$cutoff" '
    [(.unavailable // [])[] | {release, kind: "never built", reason, since}]
    + [.releases[] | select(.source_unavailable) | {release, kind: "kept on an old build", reason: .source_unavailable.reason, since: .source_unavailable.since}]
    | map(select(.since != null and .since <= $cut))'
}

recent_releases() {
  local since index
  since=$(date -u -d "@$((NOW - ${COVERAGE_DAYS:-14} * 86400))" +%F)
  index=$(index_json)
  jq -r --arg since "$since" '.releases[] | select((.first_seen // "") >= $since and .first_seen != null) | .release' <<<"$index"
}

# ------------------------------------------------------------ check

check() {
  local state last failed cov miss open body stale="" problems=0
  state=$(workflow_state)
  last=$(last_success)
  cov="[]" miss="[]"
  [ "$COVERAGE" = 1 ] && cov=$(coverage) && miss=$(missing)
  open=$(open_issue)
  body=$(mktemp)

  case "$state" in
    active) state_msg="" ;;
    disabled_inactivity)
      ghw api -X PUT "repos/$REPO/actions/workflows/$WORKFLOW/enable"
      echo "check: $WORKFLOW was disabled for inactivity, re-enabled"
      state_msg="" ;;
    *) state_msg="The workflow \`$WORKFLOW\` is \`$state\`; it is only re-enabled automatically after GitHub disabled it for inactivity." ;;
  esac

  if [ -z "$last" ]; then
    local created; created=$(workflow_created)
    if [ -z "$created" ] || [ $(( (NOW - $(epoch "$created")) / 3600 )) -ge "$STALE_HOURS" ]; then
      stale="No successful scheduled run of \`$WORKFLOW\` was found."
    fi
  else
    local age=$(( (NOW - $(epoch "$last")) / 3600 ))
    if [ "$age" -ge "$STALE_HOURS" ]; then
      stale="The last successful scheduled run ended $last, $((age / 24)) days ago."
    fi
  fi

  {
    echo "Written and closed by the \`watchdog\` workflow; nothing here is urgent."
    echo "Updated $(date -u -d "@$NOW" +'%F %H:%M UTC')."
    echo
    if [ -n "$stale" ] || [ -n "$state_msg" ]; then
      problems=$((problems + 1))
      echo "## Publishing has stopped"
      echo
      echo "**What is stale.** $stale $state_msg"
      echo
      failed=$(failed_run)
      if [ "$failed" != null ] && [ -n "$failed" ]; then
        echo "**Why.** The newest failed scheduled run, $(jq -r .created <<<"$failed"):"
        echo "$(jq -r .url <<<"$failed")"
        echo
        jq -r '.jobs[] | "- job `\(.name)`, step \(if (.steps | length) > 0 then (.steps | map("`\(.)`") | join(", ")) else "(none recorded)" end)"' <<<"$failed"
        echo
        echo "The run's summary page has the contract test findings, if any."
      else
        echo "**Why.** No failed scheduled run was found: the workflow did not run at all"
        echo "(disabled, account or billing problem, GitHub outage)."
      fi
      echo
      echo "**If you do nothing.** Everything published keeps working. New OPNsense"
      echo "releases and plugin updates are not described until a run succeeds;"
      echo "clients use the newest data they have and read back every write."
      echo
    fi
    if [ "$(jq length <<<"$cov")" -gt 0 ]; then
      problems=$((problems + 1))
      echo "## Typing lost in a new release"
      echo
      jq -r --arg url "$SERVER/$REPO/releases/tag/opnsense-" '.[] |
        "**What.** OPNsense \(.release) has operations or fields that were typed in \(.from) and are not any more ([release](\($url)\(.release))):", "",
        (.regressions[0:20][] | "- `\(.)`"),
        (if (.regressions | length) > 20 then "- ... and \(.regressions | length - 20) more" else empty end), ""' <<<"$cov"
      echo "**Why.** Either upstream rewrote these controllers as custom code, or the"
      echo "derivation does not recognise a new pattern."
      echo
      echo "**If you do nothing.** The release is published with these parts untyped."
      echo "Consumers lose them for this release. This section disappears 14 days"
      echo "after the release appeared; the loss stays recorded in the release's"
      echo "\`*.quality.json\`."
      echo
    fi
    if [ "$(jq length <<<"$miss")" -gt 0 ]; then
      problems=$((problems + 1))
      echo "## Releases without current data"
      echo
      jq -r '.[] | "- OPNsense \(.release): \(.kind) since \(.since): \(.reason)"' <<<"$miss"
      echo
      echo "**Why.** The mirror lists these releases but does not serve all of their"
      echo "packages (still being uploaded, withdrawn, or a mirror problem)."
      echo
      echo "**If you do nothing.** Consumers on these releases use older data. For a"
      echo "new series they refuse to write until data for it exists."
      echo
    fi
    echo "## To fix"
    echo
    echo "Start a Claude Code session in this repository with the link to this issue"
    echo "(skill \`opnsense-pipeline-triage\`). This issue closes itself after the fix."
  } > "$body"

  if [ "$problems" -gt 0 ]; then
    gh_label
    if [ -n "$open" ]; then
      # editing sends no new e-mail; the opening did
      ghw issue edit "$open" -R "$REPO" --body-file "$body"
    else
      ghw issue create -R "$REPO" --title "Needs you: ${REPO#*/}" --label "$LABEL" --body-file "$body"
    fi
    echo "check: $problems condition(s)"
  else
    if [ -n "$open" ]; then
      ghw issue close "$open" -R "$REPO" --comment "Resolved: last successful scheduled run $last, no typing loss in the newest releases, every release has current data."
    fi
    echo "check: nothing needs a person (last success $last)"
  fi
  rm -f "$body"
}

gh_label() {
  [ -n "${ALERTS_DRY_RUN:-}" ] && return
  gh label create "$LABEL" -R "$REPO" --color D93F0B --description "Opened and closed by the watchdog; see the issue text" 2>/dev/null || true
}

case "${1:-}" in
  check) check ;;
  recent-releases) recent_releases ;;
  *) echo "usage: $0 check" >&2; exit 2 ;;
esac
