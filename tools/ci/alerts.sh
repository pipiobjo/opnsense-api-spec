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
#   stale     the last successful scheduled run of pipeline.yml is older
#             than 7 days (WD_STALE_HOURS); a single failed run is not one
#   disabled  pipeline.yml is disabled and cannot be re-enabled here (a
#             workflow GitHub disabled for inactivity is re-enabled silently)
#   coverage  a release created in the last 14 days lost typing against the
#             release before it (its *.quality.json lists regressions);
#             clears by itself 14 days after that release appeared
#
# A person never closes the issue; the next check after the cause is gone
# does. Nothing here is urgent: consumers keep working with the last
# published data.
#
# Tests: tools/ci/alerts_test.sh. Every read from GitHub can be replaced by
# a variable (ALERTS_ISSUES, WD_STATE, WD_LAST_SUCCESS, WD_FAILED_RUN,
# WD_COVERAGE) and ALERTS_DRY_RUN=1 prints the write calls instead of making
# them.
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
NOW="${ALERTS_NOW:-$(date -u +%s)}"
STALE_HOURS="${WD_STALE_HOURS:-168}"
SERVER="${GITHUB_SERVER_URL:-https://github.com}"
LABEL=needs-you
HISTORY_END="${HISTORY_END:-2026-11-16T00:00:00Z}"

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
  gh api "repos/$REPO/actions/workflows/pipeline.yml" --jq .state
}

last_success() {
  if [ -n "${WD_LAST_SUCCESS+x}" ]; then echo "$WD_LAST_SUCCESS"; return; fi
  gh run list -R "$REPO" -w pipeline.yml -e schedule -s success -L 1 --json updatedAt -q '.[0].updatedAt // ""'
}

# failed_run: the newest failed scheduled run with its failed jobs and steps,
# as {url, created, jobs: [{name, steps: [..]}]}, or null
failed_run() {
  if [ -n "${WD_FAILED_RUN+x}" ]; then echo "$WD_FAILED_RUN"; return; fi
  local id
  id=$(gh run list -R "$REPO" -w pipeline.yml -e schedule -s failure -L 1 --json databaseId -q '.[0].databaseId // empty')
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
  local since tags tag dir out="[]" f
  since=$(date -u -d "@$((NOW - ${COVERAGE_DAYS:-14} * 86400))" +%FT%TZ)
  # all releases up to 26.7.9 were created at once on 2026-11-12 and 26.7.8
  # again on 2026-11-15; their creation date says nothing about when
  # OPNsense released them
  [[ "$since" > "$HISTORY_END" ]] || since="$HISTORY_END"
  tags=$(gh release list -R "$REPO" --limit 1000 --json tagName,createdAt \
    --jq ".[] | select(.createdAt >= \"$since\") | .tagName" | sed -n 's/^opnsense-//p')
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

# ------------------------------------------------------------ check

check() {
  local state last failed cov open body stale="" problems=0
  state=$(workflow_state)
  last=$(last_success)
  cov=$(coverage)
  open=$(open_issue)
  body=$(mktemp)

  case "$state" in
    active) state_msg="" ;;
    disabled_inactivity)
      ghw api -X PUT "repos/$REPO/actions/workflows/pipeline.yml/enable"
      echo "check: pipeline.yml was disabled for inactivity, re-enabled"
      state_msg="" ;;
    *) state_msg="The workflow \`pipeline.yml\` is \`$state\`; it is only re-enabled automatically after GitHub disabled it for inactivity." ;;
  esac

  if [ -z "$last" ]; then
    stale="No successful scheduled run of \`pipeline.yml\` was found."
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
      ghw issue create -R "$REPO" --title "Needs you: opnsense-api-spec" --label "$LABEL" --body-file "$body"
    fi
    echo "check: $problems condition(s)"
  else
    if [ -n "$open" ]; then
      ghw issue close "$open" -R "$REPO" --comment "Resolved: last successful scheduled run $last, no typing loss in the newest releases."
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
  *) echo "usage: $0 check" >&2; exit 2 ;;
esac
