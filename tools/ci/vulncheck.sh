#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Known vulnerabilities in code this module actually calls, with govulncheck
# (the Go team's scanner, database vuln.go.dev; the same script as
# tools/ci/vulncheck.sh in opnsense-api-go):
#
#   tools/ci/vulncheck.sh
#
# Fails when a vulnerability is reachable from this module's code (govulncheck
# finds a call path to the affected function) and a fixed version exists:
# then an update fixes it. A reachable one without a fix yet is a warning,
# because nothing can be done until upstream releases one. Findings in
# modules or packages that are present but not called are only counted.
#
# The standard library is judged for the Go release this repository builds
# with (go env GOVERSION, from mise.toml), not the image's own: the
# container switches to that release (verified by the Go checksum
# database). Runs isolated like tools/ci/sbom.sh: rootless Podman, no
# capabilities, repository read-only, digest-pinned image.
set -euo pipefail
export LC_ALL=C

# GOVULNCHECK, GO_IMAGE: tools/pins.env
# shellcheck source=tools/pins.env
. "$(dirname "${BASH_SOURCE[0]}")/../pins.env"
GOVULN="$GOVULNCHECK"

ROOT="$(git rev-parse --show-toplevel)"
GOV=$(cd "$ROOT" && go env GOVERSION)
out=$(mktemp)
trap 'rm -f "$out"' EXIT

podman run --rm --userns=keep-id --cap-drop=all --security-opt=no-new-privileges --network=slirp4netns \
  -v "$ROOT:/work:ro,Z" -v sbom-gomod:/gomod:U -w /work \
  -e HOME=/tmp -e GOPATH=/tmp/go -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e GOFLAGS=-mod=mod \
  -e GOTOOLCHAIN="$GOV" \
  "$GO_IMAGE" go run "$GOVULN" -format json ./... > "$out"

scanned=$(jq -r 'select(.config) | .config.go_version' "$out")
[ "$scanned" = "$GOV" ] || { echo "error: scanned with $scanned, not $GOV" >&2; exit 1; }

# one line per vulnerability: id, fixed version, reachable (a function in
# the trace) or only present
summary=$(jq -r 'select(.finding) | .finding | [.osv, (.fixed_version // ""), (if .trace[0].function then "reachable" else "present" end)] | @tsv' "$out" \
  | sort -u | awk -F'\t' '{k=$1; fix[k]=$2; if ($3=="reachable") r[k]=1; else if (!(k in r)) r[k]=0} END {for (k in r) print k "\t" fix[k] "\t" (r[k] ? "reachable" : "present")}' | sort)

fail=$(awk -F'\t' '$3=="reachable" && $2!=""' <<<"$summary")
wait=$(awk -F'\t' '$3=="reachable" && $2==""' <<<"$summary")
present=$(awk -F'\t' '$3=="present"' <<<"$summary" | grep -c . || true)

report=$(
  echo "### Vulnerabilities (govulncheck, $GOV)"
  echo
  if [ -z "$fail$wait" ]; then
    echo "No known vulnerability in called code. Present but not called: $present."
  else
    echo "| ID | Fixed in | |"
    echo "|---|---|---|"
    [ -n "$fail" ] && awk -F'\t' '{print "| " $1 " | " $2 " | reachable: update |"}' <<<"$fail"
    [ -n "$wait" ] && awk -F'\t' '{print "| " $1 " | no fix yet | reachable: warning |"}' <<<"$wait"
    echo
    echo "Present but not called: $present. Details: https://pkg.go.dev/vuln/<ID>"
  fi
)
echo "$report"
[ -n "${GITHUB_STEP_SUMMARY:-}" ] && echo "$report" >> "$GITHUB_STEP_SUMMARY"
if [ -n "$wait" ]; then
  awk -F'\t' '{print "::warning::" $1 " is reachable and has no fix yet"}' <<<"$wait"
fi
if [ -n "$fail" ]; then
  echo "error: reachable vulnerabilities with a fix: $(cut -f1 <<<"$fail" | paste -sd' ')" >&2
  exit 1
fi
