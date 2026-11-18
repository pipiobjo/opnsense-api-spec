#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# The one signal to a person (docs/operations.md, ADR 0012):
#
#   tools/ci/alerts.sh check
#
# Opens at most one issue labelled "needs-you" in GITHUB_REPOSITORY, edits
# it while a condition lasts and closes it when none is left. Conditions:
#
#   stale     the last successful scheduled run of the workflow (pipeline.yml,
#             or WD_WORKFLOW) is older
#             than 7 days (WD_STALE_HOURS); a single failed run is not one;
#             a workflow that never succeeded counts only once it exists
#             for 7 days (a new repository has not had its first run yet)
#   disabled  the workflow is disabled and cannot be re-enabled here (a
#             workflow GitHub disabled for inactivity is re-enabled silently)
#   watched   the same two conditions for each workflow in WD_WATCH, usually
#             in another repository: GitHub disables all schedules of a
#             repository at once after 60 days without activity, so each
#             repository of the project watches the others (read-only; a
#             watched workflow is never re-enabled from here)
#   promotion WD_PROMOTE set: the branch WD_PROMOTE_FROM (dev) has had
#             commits that WD_PROMOTE_TO (main) lacks for 7 days and no
#             promotion run is in progress (a failed promotion notifies
#             nobody by itself, and automatic dependency merges have no
#             person who would notice)
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
# Interface (stable; opnsense-api-go and later repositories check out this
# script at a pinned commit):
#
#   GITHUB_REPOSITORY   owner/repo that gets the issue and owns WD_WORKFLOW
#   GH_TOKEN            the job token: issues: write, actions: write (only
#                       to re-enable WD_WORKFLOW), reads of public
#                       repositories for WD_WATCH
#   WD_WORKFLOW         own scheduled workflow file (default pipeline.yml)
#   WD_COVERAGE_CHECK   1 (default): the coverage and missing conditions,
#                       which read this repository's releases; 0: off
#   WD_WATCH            space-separated owner/repo:workflow.yml pairs to
#                       watch (default none), e.g.
#                       "pipiobjo/opnsense-api-go:spec-update.yml
#                        pipiobjo/opnsense-api-go:watchdog.yml"
#   WD_PROMOTE          promotion workflow file in GITHUB_REPOSITORY
#                       (default none: no promotion condition), with
#                       WD_PROMOTE_FROM (dev) and WD_PROMOTE_TO (main)
#   WD_STALE_HOURS      168 (7 days), for every condition above
#
# A watched workflow that cannot be read (deleted, renamed, private, API
# fault) is not a condition: the check prints a warning and fails at the
# end, after the issue is written. A failed watchdog run notifies nobody;
# the other repositories' watchdogs see it as a stale watchdog.yml.
#
# Tests: tools/ci/alerts_test.sh. Every read from GitHub can be replaced by
# a variable, and ALERTS_DRY_RUN=1 prints the write calls instead of making
# them. Own workflow: WD_STATE, WD_CREATED, WD_LAST_SUCCESS, WD_RUNS (the
# recent scheduled runs, as "gh run list --json
# databaseId,status,conclusion,createdAt"), WD_RUN_VIEW (the runs to show,
# as "gh run view --json url,createdAt,conclusion,jobs", keyed by run
# id: {"<id>": {...}}); watched
# workflows: WD_WATCH_DATA, {"owner/repo:file": {state, created,
# last_success, runs, run_view}} (a missing pair or field is read from
# GitHub; the field "error" simulates a read failure); promotion:
# WD_PROMOTE_COMPARE ({ahead_by, status, oldest, head}), WD_PROMOTE_RUN
# (newest promotion run, {databaseId, status, conclusion, headSha, url,
# createdAt} or null), WD_PROMOTE_VIEW; releases: WD_COVERAGE, WD_INDEX.
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
WORKFLOW="${WD_WORKFLOW:-pipeline.yml}"
COVERAGE="${WD_COVERAGE_CHECK:-1}"
WATCH="${WD_WATCH:-}"
PROMOTE="${WD_PROMOTE:-}"
PROMOTE_FROM="${WD_PROMOTE_FROM:-dev}"
PROMOTE_TO="${WD_PROMOTE_TO:-main}"
NOW="${ALERTS_NOW:-$(date -u +%s)}"
STALE_HOURS="${WD_STALE_HOURS:-168}"
SERVER="${GITHUB_SERVER_URL:-https://github.com}"
LABEL=needs-you
# what a run that did not succeed may have ended with; "skipped" and
# "neutral" are not failures
NOT_OK='["failure","cancelled","timed_out","startup_failure","action_required","stale"]'

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
hours_since() { echo $(( (NOW - $(epoch "$1")) / 3600 )); }

# ------------------------------------------------------------ inputs

# given NAME REPO FILE: the test value of an input, if the test gives one
# (WD_<NAME> for the own workflow, WD_WATCH_DATA for watched ones)
given() {
  local name="$1" r="$2" w="$3" var
  if [ "$r" = "$REPO" ] && [ "$w" = "$WORKFLOW" ]; then
    var="WD_${name^^}"
    [ -n "${!var+x}" ] || return 1
    printf '%s\n' "${!var}"
  else
    [ -n "${WD_WATCH_DATA:-}" ] || return 1
    jq -e --arg k "$r:$w" --arg n "$name" '.[$k] | has($n)' <<<"$WD_WATCH_DATA" >/dev/null 2>&1 || return 1
    if jq -e --arg k "$r:$w" '.[$k] | has("error")' <<<"$WD_WATCH_DATA" >/dev/null; then
      echo "simulated read failure" >&2; return 2
    fi
    jq -r --arg k "$r:$w" --arg n "$name" '.[$k][$n] | if type == "string" then . else tojson end' <<<"$WD_WATCH_DATA"
  fi
}

# Each reader prints the value and fails when GitHub cannot be read.
# (given returns 1 for "no test value", 2 for a simulated failure.)
wf_state() { # REPO FILE
  local rc=0; given state "$1" "$2" || rc=$?
  [ "$rc" = 1 ] || return "$rc"
  gh api "repos/$1/actions/workflows/$2" --jq .state
}

# wf_created: when the workflow file was first registered; a workflow
# younger than STALE_HOURS that never succeeded is new, not stale
wf_created() {
  local rc=0; given created "$1" "$2" || rc=$?
  [ "$rc" = 1 ] || return "$rc"
  gh api "repos/$1/actions/workflows/$2" --jq .created_at
}

last_success() {
  local rc=0; given last_success "$1" "$2" || rc=$?
  [ "$rc" = 1 ] || return "$rc"
  gh run list -R "$1" -w "$2" -e schedule -s success -L 1 --json updatedAt -q '.[0].updatedAt // ""'
}

recent_runs() {
  local rc=0; given runs "$1" "$2" || rc=$?
  [ "$rc" = 1 ] || return "$rc"
  gh run list -R "$1" -w "$2" -e schedule -L 30 --json databaseId,status,conclusion,createdAt
}

# run_jobs: a run as {url, created, conclusion, jobs: [{name, conclusion,
# steps: [..]}]} with only the jobs and steps that did not succeed
run_jobs() { # REPO FILE ID
  local raw rc=0
  # (test values are keyed by run id, so the choice of run is visible)
  raw=$(given run_view "$1" "$2") || rc=$?
  case "$rc" in
    0) raw=$(jq -c --arg id "$3" '.[$id] // error("no test value for run \($id)")' <<<"$raw") ;;
    1) raw=$(gh run view "$3" -R "$1" --json url,createdAt,conclusion,jobs) ;;
    *) return "$rc" ;;
  esac
  jq -c --argjson bad "$NOT_OK" '{url, created: .createdAt, conclusion,
    jobs: [.jobs[] | select(.conclusion as $c | $bad | index($c))
      | {name, conclusion, steps: [.steps[] | select(.conclusion as $c | $bad | index($c)) | .name]}]}' <<<"$raw"
}

# failed_run REPO FILE LAST: the newest scheduled run after the last
# success (LAST, empty: any) that did not succeed, failed or cancelled or
# timed out or never started, via run_jobs; or null
failed_run() {
  local runs id
  runs=$(recent_runs "$1" "$2") || return 1
  id=$(jq -r --arg last "$3" --argjson bad "$NOT_OK" '[.[]
      | select(.status == "completed" and (.conclusion as $c | $bad | index($c)) and ($last == "" or .createdAt > $last))]
    | sort_by(.createdAt) | last | .databaseId // empty' <<<"$runs")
  [ -n "$id" ] || { echo null; return; }
  run_jobs "$1" "$2" "$id"
}

# why_md RUN: the "why" lines for a run from failed_run or run_jobs
why_md() {
  jq -r '"\(.url) (\(.created), \(.conclusion))", "",
    if (.jobs | length) == 0 then "- no job ran (a run that cannot start: workflow syntax, an action outside the allowed list, billing)"
    else .jobs[] | "- job `\(.name)` \(.conclusion), step \(if (.steps | length) > 0 then (.steps | map("`\(.)`") | join(", ")) else "(none recorded)" end)" end' <<<"$1"
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

# ------------------------------------------------------------ conditions

# workflow_problem REPO FILE OWN: prints the issue section when the
# workflow is stale or disabled, nothing when it is fine; fails when
# GitHub cannot be read. OWN=1: the own workflow (re-enabled after GitHub
# disabled it for inactivity; section title "Publishing has stopped").
workflow_problem() {
  local r="$1" w="$2" own="$3" state last stale="" state_msg="" failed created age
  state=$(wf_state "$r" "$w") || return 1
  last=$(last_success "$r" "$w") || return 1
  case "$state" in
    active) ;;
    disabled_inactivity)
      if [ "$own" = 1 ]; then
        ghw api -X PUT "repos/$r/actions/workflows/$w/enable" >&2
        echo "check: $w was disabled for inactivity, re-enabled" >&2
      else
        state_msg="The workflow \`$w\` in \`$r\` was disabled by GitHub for inactivity; its own repository re-enables it on its next run, or run \`gh workflow enable $w -R $r\`."
      fi ;;
    *) state_msg="The workflow \`$w\` is \`$state\`; it is only re-enabled automatically after GitHub disabled it for inactivity." ;;
  esac
  if [ -z "$last" ]; then
    created=$(wf_created "$r" "$w") || return 1
    if [ -z "$created" ] || [ "$(hours_since "$created")" -ge "$STALE_HOURS" ]; then
      stale="No successful scheduled run of \`$w\` was found."
    fi
  else
    age=$(hours_since "$last")
    if [ "$age" -ge "$STALE_HOURS" ]; then
      stale="The last successful scheduled run of \`$w\` ended $last, $((age / 24)) days ago."
    fi
  fi
  [ -n "$stale" ] || [ -n "$state_msg" ] || return 0
  failed=$(failed_run "$r" "$w" "$last") || return 1
  if [ "$own" = 1 ]; then
    echo "## Publishing has stopped"
  else
    echo "## \`$w\` in [$r]($SERVER/$r/actions/workflows/$w) has stopped"
  fi
  echo
  echo "**What is stale.** $stale $state_msg"
  echo
  if [ "$failed" != null ] && [ -n "$failed" ]; then
    echo "**Why.** The newest scheduled run since the last success that did not succeed:"
    why_md "$failed"
    echo
    echo "The run's summary page has the details (contract test findings, if any)."
  else
    echo "**Why.** No scheduled run since the last success ended at all: the workflow"
    echo "did not run (disabled, account or billing problem, GitHub outage)."
  fi
  echo
  if [ "$own" = 1 ]; then
    echo "**If you do nothing.** Everything published keeps working. New OPNsense"
    echo "releases and plugin updates are not described until a run succeeds;"
    echo "clients use the newest data they have and read back every write."
  else
    echo "**If you do nothing.** That repository's automation stays stopped and"
    echo "nothing there reports it: its own watchdog may be stopped too."
  fi
  echo
}

# promotion_problem: prints the issue section when WD_PROMOTE_FROM has had
# commits WD_PROMOTE_TO lacks for STALE_HOURS and no promotion runs now
promotion_problem() {
  [ -n "$PROMOTE" ] || return 0
  local cmp run view ahead oldest
  cmp="${WD_PROMOTE_COMPARE:-}"
  [ -n "$cmp" ] || cmp=$(gh api "repos/$REPO/compare/$PROMOTE_TO...$PROMOTE_FROM" \
    --jq '{ahead_by, status, oldest: (.commits[0].commit.committer.date // ""), head: (.commits[-1].sha // "")}') || return 1
  ahead=$(jq -r .ahead_by <<<"$cmp"); oldest=$(jq -r .oldest <<<"$cmp")
  [ "$ahead" -gt 0 ] && [ -n "$oldest" ] && [ "$(hours_since "$oldest")" -ge "$STALE_HOURS" ] || return 0
  if [ -n "${WD_PROMOTE_RUN+x}" ]; then run="$WD_PROMOTE_RUN"; else
    run=$(gh run list -R "$REPO" -w "$PROMOTE" -L 1 --json databaseId,status,conclusion,headSha,url,createdAt --jq '.[0] // null') || return 1
  fi
  if [ "$run" != null ] && [ "$(jq -r .status <<<"$run")" != completed ]; then
    echo "check: $PROMOTE is running ($(jq -r .url <<<"$run")); promotion not judged" >&2
    return 0
  fi
  echo "## Promotion to \`$PROMOTE_TO\` has stopped"
  echo
  echo "**What.** \`$PROMOTE_FROM\` has $ahead commit(s) that \`$PROMOTE_TO\` lacks, the oldest from $oldest ($(( $(hours_since "$oldest") / 24 )) days); the scheduled runs keep using the old \`$PROMOTE_TO\`, including old dependencies."
  echo
  if [ "$(jq -r .status <<<"$cmp")" = diverged ]; then
    echo "**Why.** \`$PROMOTE_FROM\` has diverged from \`$PROMOTE_TO\` (\`$PROMOTE_TO\` has commits that \`$PROMOTE_FROM\` lacks), so it cannot be fast-forwarded: rebase \`$PROMOTE_FROM\` on \`$PROMOTE_TO\`."
  elif [ "$run" = null ]; then
    echo "**Why.** \`$PROMOTE\` has never run."
  elif [ "$(jq -r .conclusion <<<"$run")" = success ]; then
    echo "**Why.** The last \`$PROMOTE\` run succeeded, on $(jq -r '.headSha[0:12]' <<<"$run"), but no run tested the head of \`$PROMOTE_FROM\`, $(jq -r '.head[0:12]' <<<"$cmp") (a push that started no run, or a dispatch that failed)."
  else
    if [ -n "${WD_PROMOTE_VIEW+x}" ]; then
      view=$(jq -c --argjson bad "$NOT_OK" '{url, created: .createdAt, conclusion,
        jobs: [.jobs[] | select(.conclusion as $c | $bad | index($c))
          | {name, conclusion, steps: [.steps[] | select(.conclusion as $c | $bad | index($c)) | .name]}]}' <<<"$WD_PROMOTE_VIEW")
    else
      view=$(run_jobs "$REPO" "$PROMOTE" "$(jq -r .databaseId <<<"$run")") || return 1
    fi
    echo "**Why.** The last \`$PROMOTE\` run, on $(jq -r '.headSha[0:12]' <<<"$run"), did not succeed:"
    why_md "$view"
  fi
  echo
  echo "**If you do nothing.** Published data keeps coming from the old \`$PROMOTE_TO\`;"
  echo "fixes and security updates on \`$PROMOTE_FROM\` do not reach it."
  echo
}

# ------------------------------------------------------------ check

check() {
  local cov miss open body sec problems=0 unread=0 pair r w last
  cov="[]" miss="[]"
  [ "$COVERAGE" = 1 ] && cov=$(coverage) && miss=$(missing)
  open=$(open_issue)
  body=$(mktemp)

  {
    echo "Written and closed by the \`watchdog\` workflow; nothing here is urgent."
    echo "Updated $(date -u -d "@$NOW" +'%F %H:%M UTC')."
    echo
  } > "$body"

  # the own workflow: a read failure ends the check (set -e) as before
  sec=$(workflow_problem "$REPO" "$WORKFLOW" 1)
  [ -z "$sec" ] || { problems=$((problems + 1)); echo "$sec" >> "$body"; echo >> "$body"; }

  for pair in $WATCH; do
    r="${pair%%:*}"; w="${pair#*:}"
    [ "$r" != "$pair" ] && [ -n "$w" ] || { echo "::warning::WD_WATCH entry '$pair' is not owner/repo:workflow.yml"; unread=$((unread + 1)); continue; }
    if ! sec=$(workflow_problem "$r" "$w" 0); then
      echo "::warning::could not read $w in $r; not judged"
      unread=$((unread + 1)); continue
    fi
    [ -z "$sec" ] || { problems=$((problems + 1)); echo "$sec" >> "$body"; echo >> "$body"; }
  done

  if ! sec=$(promotion_problem); then
    echo "::warning::could not read the promotion state; not judged"
    unread=$((unread + 1))
  elif [ -n "$sec" ]; then
    problems=$((problems + 1)); echo "$sec" >> "$body"; echo >> "$body"
  fi

  {
    if [ "$(jq length <<<"$cov")" -gt 0 ]; then
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
  } >> "$body"
  [ "$(jq length <<<"$cov")" -gt 0 ] && problems=$((problems + 1))
  [ "$(jq length <<<"$miss")" -gt 0 ] && problems=$((problems + 1))

  last=$(last_success "$REPO" "$WORKFLOW")
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
      ghw issue close "$open" -R "$REPO" --comment "Resolved: every condition is gone (last successful scheduled run of $WORKFLOW: $last)."
    fi
    echo "check: nothing needs a person (last success $last)"
  fi
  rm -f "$body"
  if [ "$unread" -gt 0 ]; then
    echo "check: $unread watched input(s) could not be read; failing so the other repositories' watchdogs notice" >&2
    exit 1
  fi
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
