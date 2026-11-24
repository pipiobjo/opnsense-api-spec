#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Pins outside Dependabot (tools/pins.env): which are outdated.
#
#   tools/ci/pins.sh check [PINS] [MISE]   JSON list of outdated pins,
#                                          [{pin, have, want, since, why}]
#
# Outdated means (the watchdog reports them, tools/ci/alerts.sh "pins"):
#   - the Go minor of GO_IMAGE differs from the one in mise.toml (at once);
#   - that Go minor is no longer supported upstream (go.dev lists only the
#     two newest minors; vulncheck.sh would then report stdlib fixes that
#     exist only in newer minors);
#   - a newer Go patch release of that minor is 7 days old (Go patch
#     releases are mostly security fixes);
#   - a tool (NAME=module/path@version) has a newer release that is 30
#     days old (tools change behaviour, not data; no hurry).
# "since" is when the newer version appeared. Every read is from public
# sources (go.dev, proxy.golang.org); a read failure fails the command, so
# the watchdog counts it as unread instead of "up to date".
set -euo pipefail
shopt -s inherit_errexit

PINS="${2:-tools/pins.env}"
MISE="${3:-mise.toml}"
NOW="${PINS_NOW:-$(date -u +%s)}"
GODL="${PINS_GODL:-https://go.dev/dl/?mode=json}"
PROXY="${PINS_PROXY:-https://proxy.golang.org}"
GO_DAYS=7
TOOL_DAYS=30

get() { curl -fsSL --retry "${PINS_RETRIES:-3}" --retry-all-errors --retry-delay 5 -m 60 "$1"; }
days_since() { echo $(( (NOW - $(date -u -d "$1" +%s)) / 86400 )); }
# module path as the proxy wants it: upper case letters as !lower
escape() { sed -E 's/([A-Z])/!\L\1/g' <<<"$1"; }

# latest PATH: {Version, Time} of the newest release of the module that
# contains PATH (tries PATH, then shorter prefixes)
latest() {
  local p="$1" code
  while [[ "$p" == */* ]]; do
    code=$(curl -s -o /dev/null -w '%{http_code}' -m 60 "$PROXY/$(escape "$p")/@latest") || code=000
    case "$code" in
      200) get "$PROXY/$(escape "$p")/@latest"; return ;;
      404|410) p="${p%/*}" ;;
      *) echo "error: $PROXY: HTTP $code for $p" >&2; return 1 ;;
    esac
  done
  echo "error: no module found for $1" >&2; return 1
}

check() {
  [ -f "$PINS" ] || { echo "error: $PINS missing" >&2; return 1; }
  local out="[]" line name val image minor mise_minor have newest t info path ver
  image=$(sed -n 's/^GO_IMAGE=//p' "$PINS")
  have=$(sed -nE 's|.*/golang:([0-9]+\.[0-9]+\.[0-9]+)-.*|\1|p' <<<"$image")
  [ -n "$have" ] || { echo "error: GO_IMAGE in $PINS has no golang:<x.y.z>- tag" >&2; return 1; }
  minor="${have%.*}"
  mise_minor=$(sed -nE 's/^go *= *"([0-9]+\.[0-9]+)(\.[0-9]+)?".*/\1/p' "$MISE")
  if [ "$mise_minor" != "$minor" ]; then
    out=$(jq -c --arg h "$have" --arg w "$mise_minor" '. + [{pin: "GO_IMAGE", have: $h, want: "Go \($w).x (mise.toml)", since: "", why: "the image and mise.toml use different Go minor versions"}]' <<<"$out")
  fi
  newest=$(get "$GODL" | jq -r --arg m "go$minor." '[.[] | select(.stable) | .version | select(startswith($m))] | first // ""')
  if [ -z "$newest" ]; then
    out=$(jq -c --arg h "$have" '. + [{pin: "GO_IMAGE", have: $h, want: "a supported Go minor (go.dev/dl)", since: "", why: "this Go minor is no longer supported; mise.toml and GO_IMAGE need the next one"}]' <<<"$out")
  elif [ "$newest" != "go$have" ]; then
    t=$(get "$PROXY/golang.org/toolchain/@v/v0.0.1-$newest.linux-amd64.info" | jq -r .Time)
    if [ "$(days_since "$t")" -ge "$GO_DAYS" ]; then
      out=$(jq -c --arg h "$have" --arg w "${newest#go}" --arg t "$t" '. + [{pin: "GO_IMAGE", have: $h, want: $w, since: $t, why: "a newer Go patch release (security fixes) is 7 days or more old"}]' <<<"$out")
    fi
  fi
  while IFS= read -r line; do
    name="${line%%=*}"; val="${line#*=}"
    [[ "$val" == *@v* ]] || continue
    path="${val%@*}"; ver="${val##*@}"
    info=$(latest "$path")
    t=$(jq -r .Time <<<"$info"); newest=$(jq -r .Version <<<"$info")
    if [ "$newest" != "$ver" ] && [ "$(days_since "$t")" -ge "$TOOL_DAYS" ]; then
      out=$(jq -c --arg n "$name" --arg h "$ver" --arg w "$newest" --arg t "$t" '. + [{pin: $n, have: $h, want: $w, since: $t, why: "a newer release is 30 days or more old"}]' <<<"$out")
    fi
  done < <(grep -E '^[A-Z_]+=' "$PINS" | grep -v '^GO_IMAGE=')
  echo "$out"
}

case "${1:-}" in
  check) check ;;
  *) sed -n '4,20p' "$0"; exit 2 ;;
esac
