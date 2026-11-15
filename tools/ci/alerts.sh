#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Signals for situations that are not failures of a run (docs/operations.md):
#
#   tools/ci/alerts.sh coverage   new typing regressions in the releases built
#                                 by this run (dist/index.json against
#                                 PREV_INDEX) -> issue labelled "attention"
#   tools/ci/alerts.sh watchdog   the daily pipeline stopped running or has not
#                                 succeeded for 3 days -> issue labelled
#                                 "watchdog"; closed again when it recovers
#   tools/ci/alerts.sh digest     one issue labelled "digest", rewritten in
#                                 place: runs, publications, verification,
#                                 warnings of the last 7 days
#
# Tests: tools/ci/alerts_test.sh. For them, every read from GitHub can be
# replaced by a variable (ALERTS_ISSUES, WD_STATE, WD_LAST_SUCCESS,
# DIGEST_INPUT) and ALERTS_DRY_RUN=1 prints the write calls instead of
# making them.
set -euo pipefail

REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
DIST="${DIST:-dist}"
NOW="${ALERTS_NOW:-$(date -u +%s)}"
STALE_HOURS="${WD_STALE_HOURS:-72}"
SERVER="${GITHUB_SERVER_URL:-https://github.com}"

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

# issues LABEL: issues with this label in any state, as JSON
# [{number,state,title,body}]
issues() {
  if [ -n "${ALERTS_ISSUES:-}" ]; then
    jq --arg l "$1" '[.[] | select(.labels | index($l))]' <<<"$ALERTS_ISSUES"
  else
    gh issue list -R "$REPO" --label "$1" --state all --limit 500 --json number,state,title,body
  fi
}

label() { # label NAME COLOR DESCRIPTION
  [ -n "${ALERTS_DRY_RUN:-}" ] && return
  gh label create "$1" -R "$REPO" --color "$2" --description "$3" 2>/dev/null || true
}

epoch() { date -u -d "$1" +%s; }

# ---------------------------------------------------------------- coverage

# releases whose catalog state changed in this run (new or rebuilt)
changed_releases() {
  local prev="${PREV_INDEX:-$DIST/.index.prev.json}"
  [ -f "$prev" ] || prev=/dev/null
  jq -rn --slurpfile c "$DIST/index.json" --slurpfile p <(cat "$prev"; echo '{}') '
    (($p[0].releases // []) | map({key: .release, value: .current}) | from_entries) as $before
    | $c[0].releases[] | select($before[.release] != .current) | .release'
}

coverage() {
  label attention FBCA04 "Not blocking; look when convenient"
  local known; known=$(issues attention)
  local rel f n=0
  for rel in $(changed_releases); do
    local found="" body comp key
    comp=$(jq -r --arg r "$rel" '.releases[] | select(.release == $r) | .current' "$DIST/index.json")
    for f in "$DIST/$rel/"*"..$rel.quality.json"; do
      [ -f "$f" ] || continue
      [ "$(jq '.regressions | length' "$f")" -gt 0 ] || continue
      found+="$f "
    done
    [ -n "$found" ] || continue
    # same regressions, same issue: the key is the release and the sorted list
    # shellcheck disable=SC2086 # list of file names without spaces
    key="coverage:$rel:$(jq -r '.regressions[]' $found | sort -u | sha256sum | cut -c1-16)"
    if jq -e --arg k "<!-- $key -->" 'any(.[]; .body | contains($k))' <<<"$known" >/dev/null; then
      echo "coverage: $rel already reported ($key)"
      continue
    fi
    body=$(mktemp)
    {
      echo "The derivation lost typing in OPNsense $rel (catalog state \`$comp\`) compared"
      echo "with the release before it. Nothing is blocked: the release was built and"
      echo "published as usual, with these operations or fields untyped."
      echo
      for f in $found; do
        echo "### $(jq -r .from "$f") -> $rel"
        echo
        jq -r '.regressions[0:50][] | "- `\(.)`"' "$f"
        local more; more=$(jq '.regressions | length - 50' "$f")
        [ "$more" -gt 0 ] && echo "- ... and $more more"
        local fw; fw=$(jq -r '.framework_changed | join(", ")' "$f")
        [ -n "$fw" ] && echo && echo "Framework files changed: $fw"
        echo
      done
      echo "## What this means"
      echo
      echo "An operation that was typed (request and response derived from the model) is"
      echo "now known only by path and parameters, or a field's read shape is unknown."
      echo "Generated clients lose the types; the provider would lose the resource or"
      echo "attribute, which its schema check blocks."
      echo
      echo "## What to do"
      echo
      echo "Session: read the diff in the release [opnsense-$rel]($SERVER/$REPO/releases/tag/opnsense-$rel)"
      echo "for these controllers or fields and extend the derivation patterns, or"
      echo "confirm that the upstream code really became custom. Close this issue then."
      echo
      echo "<!-- $key -->"
    } > "$body"
    ghw issue create -R "$REPO" --title "Coverage dropped in $rel" --label attention --body-file "$body"
    rm -f "$body"
    n=$((n + 1))
  done
  echo "coverage: $n new report(s)"
}

# ---------------------------------------------------------------- watchdog

watchdog() {
  label watchdog 5319E7 "Something did not run"
  local state last problems=() open
  state="${WD_STATE:-$(gh api "repos/$REPO/actions/workflows/pipeline.yml" --jq .state)}"
  if [ -n "${WD_LAST_SUCCESS+x}" ]; then
    last="$WD_LAST_SUCCESS"
  else
    last=$(gh run list -R "$REPO" -w pipeline.yml -e schedule -s success -L 1 --json updatedAt -q '.[0].updatedAt // ""')
  fi
  case "$state" in
    active) ;;
    disabled_inactivity)
      problems+=("The workflow \`pipeline.yml\` was disabled by GitHub for inactivity; the watchdog re-enabled it.")
      ghw api -X PUT "repos/$REPO/actions/workflows/pipeline.yml/enable" ;;
    *) problems+=("The workflow \`pipeline.yml\` is \`$state\` (not re-enabled automatically).") ;;
  esac
  if [ -z "$last" ]; then
    problems+=("No successful scheduled run of \`pipeline.yml\` was found.")
  else
    local age=$(( (NOW - $(epoch "$last")) / 3600 ))
    if [ "$age" -ge "$STALE_HOURS" ]; then
      problems+=("The last successful scheduled run ended $last, $age hours ago (limit $STALE_HOURS).")
    fi
  fi
  open=$(issues watchdog | jq -r '[.[] | select(.state == "OPEN" or .state == "open")][0].number // empty')
  local body; body=$(mktemp)
  if [ "${#problems[@]}" -gt 0 ]; then
    {
      printf -- '- %s\n' "${problems[@]}"
      echo
      echo "Runs: $SERVER/$REPO/actions/workflows/pipeline.yml"
      echo
      echo "## What to do"
      echo
      echo "If a \`pipeline-failure\` issue is open, that is the cause: fix it there."
      echo "Otherwise look at the Actions tab: a disabled workflow, an account, billing or"
      echo "runner problem, or a GitHub outage. Nothing is published while the pipeline"
      echo "does not succeed; consumers keep the last published data."
    } > "$body"
    if [ -n "$open" ]; then
      ghw issue comment "$open" -R "$REPO" --body-file "$body"
    else
      ghw issue create -R "$REPO" --title "Pipeline not healthy ($(date -u -d "@$NOW" +%F))" --label watchdog --body-file "$body"
    fi
    echo "watchdog: ${#problems[@]} problem(s)"
  else
    if [ -n "$open" ]; then
      echo "Recovered: last successful scheduled run $last." > "$body"
      ghw issue close "$open" -R "$REPO" --comment "$(cat "$body")"
    fi
    echo "watchdog: healthy (last success $last)"
  fi
  rm -f "$body"
}

# ---------------------------------------------------------------- digest

# digest_input: everything the digest shows, as one JSON document
digest_input() {
  if [ -n "${DIGEST_INPUT:-}" ]; then cat "$DIGEST_INPUT"; return; fi
  local since runs releases catalog issues_open annotations="[]" id job
  since=$(date -u -d "@$((NOW - 7 * 86400))" +%FT%TZ)
  runs=$(gh api "repos/$REPO/actions/runs?created=>=$since&per_page=100" --paginate \
    --jq '[.workflow_runs[] | {id, workflow: .name, event, conclusion, created_at, branch: .head_branch}]' | jq -s 'add // []')
  releases=$(gh api "repos/$REPO/releases?per_page=100" --paginate \
    --jq '[.[] | {tag: .tag_name, updated: ([.assets[].updated_at] | max)}]' | jq -s 'add // []')
  catalog=$(gh release download catalog -R "$REPO" -p catalog.json -O - 2>/dev/null || echo null)
  issues_open=$(gh issue list -R "$REPO" --state open --json number,title,labels \
    --jq '[.[] | {number, title, labels: [.labels[].name]}]')
  for id in $(jq -r '.[] | select(.workflow == "pipeline") | .id' <<<"$runs"); do
    for job in $(gh api "repos/$REPO/actions/runs/$id/jobs" --jq '.jobs[].id'); do
      annotations=$(jq -s 'add' <(echo "$annotations") \
        <(gh api "repos/$REPO/check-runs/$job/annotations" --jq '[.[] | {level: .annotation_level, message}]' 2>/dev/null || echo '[]'))
    done
  done
  jq -n --arg since "$since" --argjson runs "$runs" --argjson releases "$releases" \
    --argjson catalog "$catalog" --argjson issues "$issues_open" --argjson annotations "$annotations" \
    '{since: $since, runs: $runs, releases: $releases, catalog: $catalog, issues: $issues, annotations: $annotations}'
}

digest_render() { # stdin: digest input; stdout: markdown
  jq -r --arg now "$(date -u -d "@$NOW" +'%F %H:%M UTC')" --arg url "$SERVER/$REPO" '
    def vkey: split(".") | map(tonumber? // 0);
    . as $d
    | "Weekly digest, rewritten in place (\($now)); covers the 7 days since \($d.since[0:10]).",
      "Not a failure report: failures and missing runs open their own issues.",
      "",
      "## Runs",
      "",
      "| Workflow | success | failure | other |",
      "|---|---|---|---|",
      # "dynamic" runs are GitHub-internal (Dependabot graph updates)
      ($d.runs | map(select(.event != "dynamic")) | group_by(.workflow)[] | "| \(.[0].workflow) | \(map(select(.conclusion == "success")) | length) | \(map(select(.conclusion == "failure")) | length) | \(map(select(.conclusion != "success" and .conclusion != "failure")) | length) |"),
      (if ($d.runs | map(select(.event != "dynamic")) | length) == 0 then "| (none) | | | |" else empty end),
      "",
      "Last successful scheduled pipeline run: \([$d.runs[] | select(.workflow == "pipeline" and .event == "schedule" and .conclusion == "success") | .created_at] | max // "none in these 7 days")",
      "",
      "## Published",
      "",
      # "state" is storage of the pipeline, not a publication
      ([$d.releases[] | select(.tag != "state" and .updated != null and .updated >= $d.since)] as $r
        | if ($r | length) == 0 then "Nothing published or updated."
          elif ($r | length) > 10 then
            "\($r | length) releases updated (a new generator republishes all): \($r | map(.tag | sub("^opnsense-"; "")) | sort_by(vkey) | join(", "))"
          else ($r | sort_by(.updated) | reverse | .[] | "- [\(.tag)](\($url)/releases/tag/\(.tag)), \(.updated[0:10])") end),
      "",
      "## Verification (newest release per series)",
      "",
      (if $d.catalog == null then "No capability data published yet (release `catalog`)."
       else
         "| Series | Release | Status | Tested | Results |",
         "|---|---|---|---|---|",
         ($d.catalog.releases | group_by(.series)[] | max_by(.release | vkey) as $r
          | ($r.compositions[] | select(.id == $r.current)) as $c
          | "| \($r.series) | \($r.release) | \($r.status) | \(if $c.verified then "\($c.verified.date[0:10]) (\($c.verified.packages | length) packages)" else "not tested" end) | \(if $c.verified then ($c.verified.results | to_entries | map("\(.key) \(.value)") | join(", ")) else "-" end) |")
       end),
      "",
      "## Warnings in pipeline runs",
      "",
      ([$d.annotations[] | select(.level == "warning")] | group_by(.message) | map({n: length, m: .[0].message}) | sort_by(-.n)
        | if length == 0 then "None." else (.[0:30][] | "- \(.n) x \(.m | gsub("\n"; " ") | .[0:200])") end),
      "",
      "## Open issues",
      "",
      (if ($d.issues | length) == 0 then "None."
       else ($d.issues[] | "- #\(.number) \(.title)\(if (.labels | length) > 0 then " (\(.labels | join(", ")))" else "" end)") end)
  '
}

digest() {
  label digest C5DEF5 "Weekly digest, rewritten in place"
  local body open; body=$(mktemp)
  digest_input | digest_render > "$body"
  open=$(issues digest | jq -r '[.[] | select(.state == "OPEN" or .state == "open")][0].number // empty')
  if [ -n "$open" ]; then
    # editing the body sends no notification: the digest is for a glance
    ghw issue edit "$open" -R "$REPO" --body-file "$body"
  else
    ghw issue create -R "$REPO" --title "Weekly digest" --label digest --body-file "$body"
  fi
  rm -f "$body"
}

case "${1:-}" in
  coverage) coverage ;;
  watchdog) watchdog ;;
  digest) digest ;;
  digest-render) digest_render ;;
  *) echo "usage: $0 coverage | watchdog | digest" >&2; exit 2 ;;
esac
