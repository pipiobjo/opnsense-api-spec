#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/catalog.sh publish without GitHub: a copy of the script
# runs in a scratch tree with a built state and a fake gh. Per tag the fake
# answers the status from $FAKE/status/<tag> (missing: 404), SHA256SUMS
# from $FAKE/sums/<tag>, the notes from $FAKE/body/<tag>, drafts from
# $FAKE/drafts (one "<id> <tag>" per line), and records every call in
# $FAKE/calls.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
fails=0
ID=0123456789abcdef
OLD=fedcba9876543210

check() { # check NAME OUTPUT PATTERN [absent]
  if [ "${4:-}" = absent ]; then
    if grep -qF -- "$3" <<<"$2"; then echo "FAIL $1: unexpected '$3'"; fails=$((fails + 1)); else echo "ok   $1"; fi
  else
    if grep -qF -- "$3" <<<"$2"; then echo "ok   $1"; else echo "FAIL $1: missing '$3'"; printf '     %s\n' "$2"; fails=$((fails + 1)); fi
  fi
}
# before NAME A B: line A comes before line B in the calls
before() {
  local a b
  a=$(grep -nF -- "$2" "$T/fake/calls" | head -1 | cut -d: -f1); b=$(grep -nF -- "$3" "$T/fake/calls" | head -1 | cut -d: -f1)
  if [ -n "$a" ] && [ -n "$b" ] && [ "$a" -lt "$b" ]; then echo "ok   $1"; else echo "FAIL $1: '$2' (line ${a:-none}) not before '$3' (line ${b:-none})"; fails=$((fails + 1)); fi
}

mkdir -p "$T/bin"
cat > "$T/bin/gh" <<'EOF2'
#!/usr/bin/env bash
set -euo pipefail
F="$FAKE"; echo "gh $*" >> "$F/calls"
status() { cat "$F/status/$1" 2>/dev/null || echo 404; }
case "$1 $2" in
  "api -i") t="${3##*/}"; s=$(status "$t"); echo "HTTP/2.0 $s X"; [ "$s" = 200 ] ;;
  "api --paginate")
    # drafts as the release list: the script selects by its own --jq
    while read -r id tag; do printf '{"id":%s,"draft":true,"tag_name":"%s"}\n' "$id" "$tag"; done < "$F/drafts" \
      | jq -s -c . | jq -r ".[] | select(.draft and .tag_name == \"$(sed -n 's/.*tag_name == "\([^"]*\)".*/\1/p' <<<"$*")\") | .id" ;;
  "api repos/"*) t="${2##*/}"; cat "$F/body/$t" ;;
  "api -X") ;;
  "release download") cat "$F/sums/$3" ;;
  "release create"|"release edit"|"release upload") ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
EOF2
chmod +x "$T/bin/gh"

setup() {
  rm -rf "$T/root" "$T/fake"; mkdir -p "$T/root/tools/ci" "$T/root/.cache/ci/catalog" "$T/fake/status" "$T/fake/sums" "$T/fake/body"
  cp "$HERE/catalog.sh" "$HERE/gh-lib.sh" "$T/root/tools/ci/"
  local o="$T/root/.cache/ci/catalog"
  echo "{\"format\":1,\"data_id\":\"$ID\",\"generator\":\"0.9.4\"}" > "$o/catalog.json"
  echo data > "$o/capabilities.tar.zst"
  (cd "$o" && sha256sum catalog.json capabilities.tar.zst > SHA256SUMS)
  : > "$T/fake/calls"; : > "$T/fake/drafts"
}
published() { # published TAG: as built
  echo 200 > "$T/fake/status/$1"; cp "$T/root/.cache/ci/catalog/SHA256SUMS" "$T/fake/sums/$1"
}
pointer() { echo 200 > "$T/fake/status/catalog-v1"; printf 'data_id: %s\n\nThe newest ...\n' "$1" > "$T/fake/body/catalog-v1"; }
cat_sh() { (cd "$T/root" && env PATH="$T/bin:$PATH" FAKE="$T/fake" GITHUB_REPOSITORY=example/spec GITHUB_SHA=0123456789abcdef0123456789abcdef01234567 tools/ci/catalog.sh "$@" 2>&1) || echo "catalog.sh exited $?"; }

setup
out=$(cat_sh publish)
check "first publish: fixed release created as a draft" "$(cat "$T/fake/calls")" "gh release create catalog-v1-$ID -R example/spec --draft"
check "first publish: then published" "$(cat "$T/fake/calls")" "gh release edit catalog-v1-$ID -R example/spec --draft=false"
check "first publish: pointer created" "$(cat "$T/fake/calls")" "gh release create catalog-v1 -R example/spec"
check "first publish: legacy release created" "$(cat "$T/fake/calls")" "gh release create catalog -R example/spec"
before "first publish: fixed published before the pointer" "--draft=false" "release create catalog-v1 -R"
before "first publish: pointer before the legacy release" "release create catalog-v1 -R" "release create catalog -R"
check "first publish: the pointer has no files" "$(grep 'release create catalog-v1 -R' "$T/fake/calls")" "capabilities.tar.zst" absent

setup; published "catalog-v1-$ID"; published catalog; pointer "$ID"
out=$(cat_sh publish)
check "unchanged: nothing written" "$out" "catalog unchanged"
check "unchanged: no write call" "$(cat "$T/fake/calls")" "release edit" absent
out=$(cat_sh published-id)
check "published-id: the pointer's data_id" "$out" "$ID"

setup; published "catalog-v1-$ID"; pointer "$OLD"
out=$(cat_sh publish)
check "fixed exists, same files: left" "$out" "exists with the same files; left as it is"
check "fixed exists: not created again" "$(cat "$T/fake/calls")" "release create catalog-v1-$ID" absent
check "fixed exists: pointer moved" "$(cat "$T/fake/calls")" "gh release edit catalog-v1 -R example/spec --notes-file"
check "fixed exists: legacy release created" "$(cat "$T/fake/calls")" "gh release create catalog -R"

setup; published "catalog-v1-$ID"; echo "0000  catalog.json" > "$T/fake/sums/catalog-v1-$ID"; pointer "$OLD"
out=$(cat_sh publish)
check "fixed exists, other files: identity error" "$out" "the identity is broken"
check "fixed exists, other files: fails" "$out" "catalog.sh exited 1"
check "fixed exists, other files: pointer not moved" "$(cat "$T/fake/calls")" "release edit catalog-v1 " absent

setup; echo "77 catalog-v1-$ID" > "$T/fake/drafts"; echo "78 catalog-v1-other" >> "$T/fake/drafts"
out=$(cat_sh publish)
check "left-over draft: deleted" "$(cat "$T/fake/calls")" "gh api -X DELETE repos/example/spec/releases/77"
check "left-over draft: only its own" "$(cat "$T/fake/calls")" "releases/78" absent
before "left-over draft: deleted before the new one" "releases/77" "release create catalog-v1-$ID"

setup; echo 502 > "$T/fake/status/catalog-v1"
out=$(cat_sh publish)
check "pointer unreadable: fails" "$out" "catalog.sh exited 1"
check "pointer unreadable: nothing written" "$(cat "$T/fake/calls")" "release create" absent

setup; echo 200 > "$T/fake/status/catalog-v1"; printf 'no line\n' > "$T/fake/body/catalog-v1"
out=$(cat_sh published-id)
check "pointer without a data_id line: error" "$out" "names no single 'data_id: <16 hex>' line"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
