#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/state.sh without GitHub: a copy of the script runs in a
# scratch tree with a fake gh. The fake answers the release status from
# $FAKE/status (200, 404, 502), the assets from $FAKE/assets/, the
# attestation check from $FAKE/attested (exists: verified), and records
# every call in $FAKE/calls.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
fails=0

check() { # check NAME OUTPUT PATTERN [absent]
  if [ "${4:-}" = absent ]; then
    if grep -qF -- "$3" <<<"$2"; then echo "FAIL $1: unexpected '$3'"; fails=$((fails + 1)); else echo "ok   $1"; fi
  else
    if grep -qF -- "$3" <<<"$2"; then echo "ok   $1"; else echo "FAIL $1: missing '$3'"; printf '     %s\n' "$2"; fails=$((fails + 1)); fi
  fi
}

mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF2'
#!/usr/bin/env bash
set -euo pipefail
F="$FAKE"; echo "gh $*" >> "$F/calls"
case "$1 $2" in
  "api -i")
    s=$(cat "$F/status"); echo "HTTP/2.0 $s X"; [ "$s" = 200 ] ;;
  "release download")
    shift 2; d=.; pats=()
    while [ $# -gt 0 ]; do case "$1" in -p) pats+=("$2"); shift ;; -D) d="$2"; shift ;; esac; shift; done
    for p in "${pats[@]}"; do cp "$F/assets/$p" "$d/"; done ;;
  "release upload"|"release create") ;;
  "attestation verify") [ -f "$F/attested" ] ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
EOF2
chmod +x "$T/bin/gh"

index() { # index N: an index.json with N releases that have a contract test record
  jq -cn --argjson n "$1" '{generator: "0.9.4", releases: [range(0; 3) | {release: "26.7.\(.)", verified: (if . < $n then {date: "x"} else null end)}]}'
}

# a scratch root with the script and a published state of 2 verified records
setup() {
  rm -rf "$T/root" "$T/fake"; mkdir -p "$T/root/tools/ci" "$T/fake/assets" "$T/src"
  cp "$HERE/state.sh" "$HERE/gh-lib.sh" "$T/root/tools/ci/"
  rm -rf "$T/src"/*; index 2 > "$T/src/index.json"
  tar -C "$T/src" -cf - . | zstd -q > "$T/fake/assets/dist.tar.zst"
  cp "$T/src/index.json" "$T/fake/assets/index.json"
  echo 200 > "$T/fake/status"; touch "$T/fake/attested" "$T/fake/calls"
}
st() { (cd "$T/root" && env PATH="$T/bin:$PATH" FAKE="$T/fake" GITHUB_REPOSITORY=example/spec "$@" 2>&1) || echo "state.sh exited $?"; }

setup
out=$(st tools/ci/state.sh restore)
check "restore: attested state is used" "$out" "state restored: 3 releases (2 with a contract test record)"
check "restore: provenance checked" "$(cat "$T/fake/calls")" "gh attestation verify"

setup; echo 404 > "$T/fake/status"
out=$(st tools/ci/state.sh restore)
check "restore 404: starts from scratch" "$out" "no published state"
check "restore 404: succeeds" "$out" "state.sh exited" absent

setup; echo 502 > "$T/fake/status"
out=$(st tools/ci/state.sh restore)
check "restore 5xx: not taken for 'no state'" "$out" "cannot read the release 'state' (HTTP 502)"
check "restore 5xx: fails" "$out" "state.sh exited 1"
check "restore 5xx: no empty previous index" "$(ls "$T/root/.cache/ci")" "index.prev.json" absent

setup; rm "$T/fake/attested"
out=$(st env LIVE=true tools/ci/state.sh restore)
check "unattested, live: refused" "$out" "has no attestation of pipeline.yml on main"
check "unattested, live: fails" "$out" "state.sh exited 1"
check "unattested, live: dist untouched" "$(ls "$T/root")" "dist" absent
out=$(st env LIVE=true STATE_ACCEPT_UNATTESTED=1 tools/ci/state.sh restore)
check "unattested, live, accepted on request: used" "$out" "accepted on request"
setup; rm "$T/fake/attested"
out=$(st tools/ci/state.sh restore)
check "unattested, dry run: warning only" "$out" "used anyway because this run publishes nothing"
check "unattested, dry run: restored" "$out" "state restored"

# pack: the shrinking guard
setup; st tools/ci/state.sh restore >/dev/null
index 1 > "$T/root/dist/index.json"
out=$(st tools/ci/state.sh pack)
check "pack: fewer contract test records refused" "$out" "refusing to save a state that lost contract test history"
check "pack: fails" "$out" "state.sh exited 1"
out=$(st env STATE_ALLOW_FEWER_VERIFIED=1 tools/ci/state.sh pack)
check "pack: fewer allowed on request" "$out" "allowed on request"
index 3 > "$T/root/dist/index.json"
out=$(st tools/ci/state.sh pack)
check "pack: more records are fine" "$out" "3 releases with a contract test record, before 2"

# save: the replaced state is kept as previous-*
: > "$T/fake/calls"
out=$(st tools/ci/state.sh save)
check "save: keeps the replaced state" "$(cat "$T/fake/calls")" "previous-dist.tar.zst"
check "save: then uploads the new one" "$(tail -1 "$T/fake/calls")" "/.cache/ci/state/dist.tar.zst"
echo 502 > "$T/fake/status"
out=$(st tools/ci/state.sh save)
check "save 5xx: fails, creates nothing" "$out" "state.sh exited 1"
check "save 5xx: no release created" "$(cat "$T/fake/calls")" "release create" absent

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
