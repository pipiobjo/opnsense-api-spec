#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Coverage report and floor for one Go coverage profile (the same script
# as tools/ci/coverage.sh in opnsense-api-go):
#
#   tools/ci/coverage.sh NAME PROFILE [OUTDIR]
#
# Prints a Markdown table (statements covered per package and in total;
# also appended to $GITHUB_STEP_SUMMARY in CI), writes OUTDIR/NAME.html
# (default coverage/), and fails when the total is below the floor for
# NAME in tools/ci/coverage-floor.txt. The floor is the measured value
# rounded down; raise it by hand when coverage rose (the script says when),
# never lower it without a reason in the commit message.
#
# Profiles come from go test -coverpkg=./... -coverprofile=..., so code is
# counted wherever a test reaches it, not only from its own package.
set -euo pipefail
export LC_ALL=C # decimal point, whatever the locale

NAME="${1:?usage: coverage.sh NAME PROFILE [OUTDIR]}"
PROFILE="${2:?usage: coverage.sh NAME PROFILE [OUTDIR]}"
OUT="${3:-coverage}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FLOORS="$HERE/coverage-floor.txt"
MODULE=$(go list -m)

mkdir -p "$OUT"
go tool cover -html="$PROFILE" -o "$OUT/$NAME.html"

# A block appears once per test binary with -coverpkg; count each block
# once, covered if any binary covered it. Test helpers (test/...) are not
# product code.
table=$(awk -v mod="$MODULE/" '
  NR == 1 { next }
  {
    split($1, a, ":"); file = a[1]; sub("^" mod, "", file)
    if (file ~ /^test\//) next
    key = $1; n[key] = $2; if ($3 > 0) hit[key] = 1
    pkg = file; sub("/[^/]*$", "", pkg); p[key] = pkg
  }
  END {
    for (k in n) { tot[p[k]] += n[k]; all += n[k]; if (k in hit) { cov[p[k]] += n[k]; c += n[k] } }
    for (q in tot) printf "| `%s` | %d | %.1f%% |\n", q, tot[q], 100 * cov[q] / tot[q]
    printf "TOTAL %.1f\n", (all ? 100 * c / all : 0)
  }' "$PROFILE" | sort)
total=$(sed -n 's/^TOTAL //p' <<<"$table")

report=$(
  echo "### Coverage: $NAME"
  echo
  echo "| Package | Statements | Covered |"
  echo "|---|---:|---:|"
  grep -v '^TOTAL' <<<"$table"
  echo "| **total** | | **$total%** |"
)
echo "$report"
[ -n "${GITHUB_STEP_SUMMARY:-}" ] && echo "$report" >> "$GITHUB_STEP_SUMMARY"

floor=$(awk -v n="$NAME" '$1 == n {print $2}' "$FLOORS")
if [ -z "$floor" ]; then
  echo "error: no floor for $NAME in $FLOORS" >&2
  exit 1
fi
if awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t < f) }'; then
  echo "error: coverage $NAME $total% is below the floor of $floor% ($FLOORS)" >&2
  exit 1
fi
if awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t >= f + 2) }'; then
  echo "note: coverage $NAME $total% is well above the floor of $floor%; raise it in $FLOORS"
fi
echo "coverage $NAME $total% (floor $floor%)"
