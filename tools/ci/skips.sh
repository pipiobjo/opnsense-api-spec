#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# A skipped test proves nothing, and a skip in a green run is easy to miss
# (the same script as tools/ci/skips.sh in opnsense-api-go):
#
#   tools/ci/skips.sh LOG...
#
# Reads the output of `go test -v` and fails when a test or subtest
# skipped that is not on the allowlist tools/ci/skip-allowlist.txt (or
# SKIP_ALLOWLIST). Allowlist lines: the full test name, then the reason;
# lines starting with '#' are comments (test names may contain '#'). An entry without a reason fails too. Allowed skips
# are printed with their reason, so they stay visible.
set -euo pipefail

LIST="${SKIP_ALLOWLIST:-$(dirname "$0")/skip-allowlist.txt}"
[ "$#" -gt 0 ] || { echo "usage: $0 LOG..." >&2; exit 2; }

declare -A reason=()
bad=0
while IFS= read -r line; do
  [[ "$line" =~ ^[[:space:]]*# ]] && continue
  read -r name why <<<"$line" || true
  [ -n "${name:-}" ] || continue
  if [ -z "${why:-}" ]; then
    echo "::error::$LIST: $name has no reason"
    bad=1
  fi
  reason["$name"]="${why:-}"
done < "$LIST"

skipped=$(sed -nE 's/^[[:space:]]*--- SKIP: ([^ ]+) \(.*/\1/p' "$@" | sort -u)
n=0
while IFS= read -r name; do
  [ -n "$name" ] || continue
  n=$((n + 1))
  if [ -n "${reason[$name]+x}" ]; then
    echo "skip allowed: $name (${reason[$name]})"
  else
    echo "::error::$name skipped and is not on the allowlist ($LIST): make it run, make it fail, or add it with a reason"
    bad=1
  fi
done <<<"$skipped"
echo "skips: $n"
exit "$bad"
