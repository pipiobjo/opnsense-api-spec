#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/pins.sh against a local web server that plays go.dev
# and proxy.golang.org (files under a scratch directory).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T=$(mktemp -d)
fails=0
check() { # check NAME OUTPUT PATTERN [absent]
  if [ "${4:-}" = absent ]; then
    if grep -qF -- "$3" <<<"$2"; then echo "FAIL $1: unexpected '$3'"; fails=$((fails + 1)); else echo "ok   $1"; fi
  else
    if grep -qF -- "$3" <<<"$2"; then echo "ok   $1"; else echo "FAIL $1: missing '$3'"; printf '     %s\n' "$2"; fails=$((fails + 1)); fi
  fi
}

W="$T/www"; mkdir -p "$W/golang.org/toolchain/@v" "$W/golang.org/x/vuln/@latest.d" "$W/example.com/!tool/@latest.d"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
(cd "$W" && exec python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) & srv=$!
trap 'kill $srv 2>/dev/null; rm -rf "$T"' EXIT
for _ in $(seq 1 50); do curl -s -o /dev/null "http://127.0.0.1:$port/" && break; sleep 0.1; done
rmdir "$W/golang.org/x/vuln/@latest.d" "$W/example.com/!tool/@latest.d"

godl() { echo "$1" > "$W/dl.json"; }
latest() { echo "{\"Version\":\"$2\",\"Time\":\"$3\"}" > "$W/$1/@latest"; }
tc() { echo "{\"Version\":\"v0.0.1-go$1.linux-amd64\",\"Time\":\"$2\"}" > "$W/golang.org/toolchain/@v/v0.0.1-go$1.linux-amd64.info"; }
cat > "$T/pins.env" <<'PINS'
GO_IMAGE=docker.io/library/golang:1.27.4-bookworm@sha256:00
GOVULNCHECK=golang.org/x/vuln/cmd/govulncheck@v1.9.0
TOOL=example.com/Tool/cmd/tool@v2.0.0
PINS
echo 'go = "1.27"  # comment' > "$T/mise.toml"
run() { env PINS_NOW="$(date -u -d 2026-12-01 +%s)" PINS_GODL="http://127.0.0.1:$port/dl.json" PINS_PROXY="http://127.0.0.1:$port" \
  "$HERE/pins.sh" check "$T/pins.env" "$T/mise.toml" 2>&1 || echo "pins.sh exited $?"; }

godl '[{"version":"go1.27.4","stable":true},{"version":"go1.26.11","stable":true}]'
latest golang.org/x/vuln v1.9.0 2026-10-01T00:00:00Z
latest 'example.com/!tool' v2.0.0 2026-10-01T00:00:00Z
out=$(run)
check "current: nothing outdated" "$out" "[]"

godl '[{"version":"go1.27.5","stable":true},{"version":"go1.26.12","stable":true}]'
tc 1.27.5 2026-11-28T00:00:00Z
out=$(run)
check "Go patch 3 days old: not yet" "$out" "[]"
tc 1.27.5 2026-11-20T00:00:00Z
out=$(run)
check "Go patch 11 days old: reported" "$out" '"pin":"GO_IMAGE","have":"1.27.4","want":"1.27.5"'

godl '[{"version":"go1.29.0","stable":true},{"version":"go1.28.3","stable":true}]'
out=$(run)
check "Go minor unsupported: reported" "$out" "this Go minor is no longer supported"

godl '[{"version":"go1.27.4","stable":true}]'
echo 'go = "1.28"' > "$T/mise.toml"
out=$(run)
check "image and mise.toml differ: reported" "$out" "different Go minor versions"
echo 'go = "1.27"' > "$T/mise.toml"

latest golang.org/x/vuln v1.10.0 2026-11-17T00:00:00Z
out=$(run)
check "tool 14 days newer: not yet" "$out" "[]"
latest golang.org/x/vuln v1.10.0 2026-10-20T00:00:00Z
latest 'example.com/!tool' v2.1.0 2026-10-20T00:00:00Z
out=$(run)
check "tool 42 days newer: reported" "$out" '"pin":"GOVULNCHECK","have":"v1.9.0","want":"v1.10.0"'
check "module path with upper case found" "$out" '"pin":"TOOL","have":"v2.0.0","want":"v2.1.0"'

rm "$W/dl.json"
out=$(run)
check "unreadable source: fails, not 'up to date'" "$out" "pins.sh exited"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
