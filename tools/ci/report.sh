#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Failure report of a pipeline run (ADR 0008): one open issue labelled
# pipeline-failure; later failures add a comment instead of a new issue.
# Close the issue once the cause is fixed (or accepted in
# verify/accepted.json with evidence).
#
# Input: RESULTS (toJSON(needs) of the workflow), RUN_URL, and the
# downloaded verified-<release> artifacts under VERIFIED_DIR (default
# .ci/in/verified), each with a file "release" naming its release.
set -euo pipefail

LABEL="pipeline-failure"
body=$(mktemp)
{
  echo "Pipeline run: $RUN_URL"
  echo
  echo "| Job | Result |"
  echo "|---|---|"
  jq -r 'to_entries[] | "| \(.key) | \(.value.result) |"' <<<"$RESULTS"
  echo
  found=0
  # One directory per contract job, marked by the file "release" (the
  # layout of downloaded artifacts depends on how many there are).
  while IFS= read -r m; do
    d=$(dirname "$m"); rel=$(cat "$m")
    f="$d/opnsense-$rel.verified.json"
    if [ ! -f "$f" ]; then
      echo "### $rel: no contract test result"
      echo "The run stopped before verify finished; last lines of the log:"
      echo '```'
      tail -n 25 "$d/verify.log" 2>/dev/null || echo "(no log)"
      echo '```'
      found=1
      continue
    fi
    fails=$(jq -r '.results[] | select(.status == "mismatch" or .status == "error")
      | "- \(.check) `\(.path)` (\(.status)): \(.findings // [] | .[0:3] | join("; "))"' "$f")
    [ -n "$fails" ] || continue
    found=1
    echo "### $rel (test instance $(jq -r .instance_version "$f"))"
    echo "$fails" | head -n 50
    echo
  done < <(find "${VERIFIED_DIR:-.ci/in/verified}" -name release -type f 2>/dev/null)
  if [ "$found" = 0 ]; then
    echo "No contract test findings; see the failed job's log (gates, matrix, client smoke test or publish)."
  fi
  echo
  echo "Nothing was published. A finding is either a generator bug (fix it), or an"
  echo "upstream behaviour to accept in \`verify/accepted.json\` with reason and evidence."
} > "$body"

gh label create "$LABEL" --color B60205 --description "Scheduled pipeline failed; nothing published" 2>/dev/null || true
open=$(gh issue list --label "$LABEL" --state open --json number --jq '.[0].number // empty')
if [ -n "$open" ]; then
  gh issue comment "$open" --body-file "$body"
  echo "commented on issue #$open"
else
  gh issue create --title "Pipeline failed ($(date -u +%F))" --label "$LABEL" --body-file "$body"
fi
rm -f "$body"
