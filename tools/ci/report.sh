#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Failure report of a pipeline run (ADR 0008, ADR 0012): written to the
# run's summary page only. A failed run sends no message; the watchdog
# opens one "needs-you" issue when publishing has been stale for 7 days and
# links the newest failed run, whose summary this is.
#
# Input: RESULTS (toJSON(needs) of the workflow), RUN_URL, and the
# downloaded verified-<release> artifacts under VERIFIED_DIR (default
# .ci/in/verified), each with a file "release" naming its release.
set -euo pipefail

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
    # written by the contract job with "opnsense-api-spec failures": the
    # generator decides what fails, this script only shows it
    fails=$(cat "$d/failures.md" 2>/dev/null || true)
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
  echo "Nothing was published; the next scheduled run retries. A finding is either a generator bug (fix it), or an"
  echo "upstream behaviour to accept in \`verify/accepted.json\` with reason and evidence."
} > "$body"

cat "$body" >> "${GITHUB_STEP_SUMMARY:-/dev/stdout}"
rm -f "$body"
