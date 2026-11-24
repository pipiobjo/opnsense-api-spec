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
# NAME in tools/ci/coverage-floor.txt, unless COVERAGE_ENFORCE=0 (then the
# result is only reported).
#
# Floors (decision of 2026-11-24): the lowest measurement minus a margin,
# the third column of the floor file. Unit profiles measure code and are
# deterministic: margin 1, enforced everywhere. Profiles measured against
# a VM (contract test) vary with the instance: margin 3, reported in every
# run and enforced only where code changes are tested (promote.yml), never
# on the unattended publishing path. Raise a floor when the lowest
# measurement rose (the script says when); never lower one to make a red
# run green without a reason in the commit message.
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
margin=$(awk -v n="$NAME" '$1 == n {print $3}' "$FLOORS")
if [ -z "$floor" ] || [ -z "$margin" ]; then
  echo "error: no floor and margin for $NAME in $FLOORS" >&2
  exit 1
fi
if awk -v t="$total" -v f="$floor" 'BEGIN { exit !(t < f) }'; then
  if [ "${COVERAGE_ENFORCE:-1}" = 0 ]; then
    echo "::warning::coverage $NAME $total% is below the floor of $floor% (reported only here; enforced in promote.yml)"
    exit 0
  fi
  echo "error: coverage $NAME $total% is below the floor of $floor% ($FLOORS)" >&2
  exit 1
fi
if awk -v t="$total" -v f="$floor" -v m="$margin" 'BEGIN { exit !(t >= f + m + 2) }'; then
  echo "note: coverage $NAME $total% is more than $margin+2 points above the floor of $floor%; if that is the lowest measurement now, raise the floor in $FLOORS"
fi
echo "coverage $NAME $total% (floor $floor%)"
