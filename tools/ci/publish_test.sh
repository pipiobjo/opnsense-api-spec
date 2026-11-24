#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Tests of tools/ci/publish.sh without GitHub: a copy of the script runs in
# a scratch tree with a small dist/ and a fake gh that answers from files
# and records every call. Writes are only printed (upload --dry-run).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
fails=0
SHA=0123456789abcdef0123456789abcdef01234567

check() { # check NAME OUTPUT PATTERN [absent]
  if [ "${4:-}" = absent ]; then
    if grep -qF -- "$3" <<<"$2"; then echo "FAIL $1: unexpected '$3'"; fails=$((fails + 1)); else echo "ok   $1"; fi
  else
    if grep -qF -- "$3" <<<"$2"; then echo "ok   $1"; else echo "FAIL $1: missing '$3'"; printf '     %s\n' "$2"; fails=$((fails + 1)); fi
  fi
}

# fake gh: releases from $T/fake/releases.json ([{tag_name, assets: [{name}]}]),
# published checksums from $T/fake/<tag>.SHA256SUMS, existing tags in
# $T/fake/tags; every call is appended to $T/fake/calls
mkdir -p "$T/bin" "$T/fake"
cat > "$T/bin/gh" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
F="$FAKE"; echo "gh $*" >> "$F/calls"
jqarg() { local prev="" a; for a in "$@"; do [ "$prev" = --jq ] && { echo "$a"; return; }; prev="$a"; done; echo .; }
case "$1 $2" in
  "api --paginate") jq -c "$(jqarg "$@")" "$F/releases.json" ;;
  "release download") [ -f "$F/$3.SHA256SUMS" ] && cat "$F/$3.SHA256SUMS" || exit 1 ;;
  "api repos/"*)
    case "$2" in
      */git/ref/tags/*) grep -qx "${2##*/}" "$F/tags" 2>/dev/null ;;
      */releases/tags/*) jq -c --arg t "${2##*/}" '.[] | select(.tag_name == $t)' "$F/releases.json" | jq -r "$(jqarg "$@")" ;;
      *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
    esac ;;
  *) echo "fake gh: unexpected $*" >&2; exit 1 ;;
esac
EOF
chmod +x "$T/bin/gh"

# a scratch repository root with the scripts under test
setup() {
  rm -rf "$T/root" "$T/fake/"*; mkdir -p "$T/root/tools/ci" "$T/root/dist/26.7.9" "$T/root/.cache/ci"
  cp "$HERE/publish.sh" "$HERE/assets.sh" "$T/root/tools/ci/"
  echo '{"openapi":"3.1.0"}' > "$T/root/dist/26.7.9/opnsense-26.7.9.openapi.json"
  echo '{}' > "$T/root/dist/26.7.9/26.7.8..26.7.9.json"
  cat > "$T/root/dist/index.json" <<'JSON'
{"generator":"0.9.3","releases":[{"release":"26.7.9","current":"c1","status":"provisional","needs_verification":false,"verified":{"instance":"26.7.9","date":"2026-11-18","failed":0}}]}
JSON
  cp "$T/root/dist/index.json" "$T/root/.cache/ci/index.prev.json"
  echo v0.9.3 > "$T/fake/tags"
  # what is published: exactly this dist, under GitHub's names
  local sums
  sums=$(cd "$T/root/dist/26.7.9" && printf '%s  %s\n' "$(sha256sum < 26.7.8..26.7.9.json | cut -d' ' -f1)" 26.7.8.26.7.9.json \
    "$(sha256sum < opnsense-26.7.9.openapi.json | cut -d' ' -f1)" opnsense-26.7.9.openapi.json)
  echo "$sums" > "$T/fake/opnsense-26.7.9.SHA256SUMS"
  published '"26.7.8.26.7.9.json","opnsense-26.7.9.openapi.json","SHA256SUMS"'
}
published() { echo "[{\"tag_name\":\"opnsense-26.7.9\",\"assets\":[$(sed -E 's/"([^"]+)"/{"name":"\1"}/g' <<<"$1")]}]" > "$T/fake/releases.json"; }
pub() { (cd "$T/root" && env PATH="$T/bin:$PATH" FAKE="$T/fake" GITHUB_REPOSITORY=example/spec GITHUB_SHA=$SHA tools/ci/publish.sh "$@" 2>&1) || echo "publish.sh exited $?"; }

setup
out=$(pub plan)
check "unchanged and identical: nothing to publish" "$out" "plan: 0 release(s) to publish"
check "the stored name is used (a..b becomes a.b)" "$(cat "$T/fake/calls")" "gh release download opnsense-26.7.9"
out=$(pub upload --dry-run)
check "nothing planned: no write" "$out" "dry-run:" absent

setup
published '"26.7.8.26.7.9.json","opnsense-26.7.9.openapi.json","SHA256SUMS","26.7.8..26.7.9.json"'
out=$(pub plan)
check "left-over asset with matching SHA256SUMS: republished" "$out" "plan: update opnsense-26.7.9"
out=$(pub upload --dry-run)
check "left-over asset: deleted after the upload" "$out" "dry-run: gh release delete-asset opnsense-26.7.9 26.7.8..26.7.9.json -R example/spec --yes"
check "left-over asset: listed assets are kept" "$out" "delete-asset opnsense-26.7.9 opnsense-26.7.9.openapi.json" absent
check "left-over asset: assets uploaded first" "$(grep -n 'release upload\|delete-asset' <<<"$out" | head -1)" "release upload opnsense-26.7.9"

setup
published '"opnsense-26.7.9.openapi.json","SHA256SUMS"'
out=$(pub plan)
check "missing asset: republished" "$out" "plan: update opnsense-26.7.9"

setup
echo "0000  opnsense-26.7.9.openapi.json" > "$T/fake/opnsense-26.7.9.SHA256SUMS"
out=$(pub plan)
check "other checksums: republished" "$out" "published files differ from dist"

setup
sed -i 's/"current":"c1"/"current":"c2"/' "$T/root/dist/index.json"
out=$(pub plan)
check "changed index entry: updated" "$out" "plan: update opnsense-26.7.9"

# a plugin the test VM could not install (ADR 0008): published as a
# pre-release that names it
setup
jq '.releases[0] += {current: "c2", needs_verification: true, verified: (.releases[0].verified + {untested_packages: ["os-caddy"], not_installable: ["os-caddy"]})}' \
  "$T/root/dist/index.json" > "$T/root/dist/index.json.new" && mv "$T/root/dist/index.json.new" "$T/root/dist/index.json"
out=$(pub plan)
check "not installable: published as a pre-release" "$out" "plan: update opnsense-26.7.9 (pre-release=true"
check "not installable: the notes name it" "$(cat "$T/root/.cache/ci/upload/26.7.9/notes.md")" "1 packages untested, not installable on the test VM: os-caddy)"

setup
echo '[]' > "$T/fake/releases.json"
out=$(pub plan)
check "no release yet: created" "$out" "plan: create opnsense-26.7.9"
out=$(pub upload --dry-run)
check "no release yet: create with the commit" "$out" "dry-run: gh release create opnsense-26.7.9 -R example/spec --target $SHA"
check "no release yet: checksums uploaded" "$out" "/26.7.9/assets/SHA256SUMS"
: > "$T/fake/tags"
out=$(pub plan)
check "new generator version: tag planned" "$(cat "$T/root/.cache/ci/upload/generator.env")" "new_tag=1"
out=$(pub upload --dry-run)
check "new generator version: tag created on the commit" "$out" "dry-run: gh api repos/example/spec/git/refs -f ref=refs/tags/v0.9.3 -f sha=$SHA"

# upload treats the plan as data
setup
sed -i 's/"current":"c1"/"current":"c2"/' "$T/root/dist/index.json"
pub plan >/dev/null
cp -r "$T/root/.cache/ci/upload" "$T/plan"
sed -i 's/^26.7.9\t/..\/..\/x\t/' "$T/root/.cache/ci/upload/releases.tsv"
out=$(pub upload --dry-run)
check "plan: a release name that is not one is refused" "$out" "error: plan: bad release"
rm -rf "$T/root/.cache/ci/upload"; cp -r "$T/plan" "$T/root/.cache/ci/upload"
touch "$T/root/.cache/ci/upload/26.7.9/assets/-x --clobber"
out=$(pub upload --dry-run)
check "plan: an asset name that is not one is refused" "$out" "error: plan: bad asset"
rm -rf "$T/root/.cache/ci/upload"; cp -r "$T/plan" "$T/root/.cache/ci/upload"
ln -s /etc/passwd "$T/root/.cache/ci/upload/26.7.9/assets/passwd"
out=$(pub upload --dry-run)
check "plan: a link is refused" "$out" "error: plan: bad asset"
rm -rf "$T/root/.cache/ci/upload"; cp -r "$T/plan" "$T/root/.cache/ci/upload"
echo 1111111111111111111111111111111111111111 > "$T/root/.cache/ci/upload/commit"
out=$(pub upload --dry-run)
check "plan: made for another commit is refused" "$out" "plan is for 1111111111111111111111111111111111111111"
rm -rf "$T/root/.cache/ci/upload"
out=$(pub upload --dry-run)
check "no plan: refused" "$out" "error: no plan"

# unpack-input.sh: the hand-over archive of prepare may only add files
# below .cache/ci (a build dependency must not replace a script that the
# write job runs next; ADR 0015)
U="$T/unpack"; mkdir -p "$U/src/.cache/ci/upload/26.7.9" "$U/src/.cache/ci/sums" "$U/src/tools/ci"
echo x > "$U/src/.cache/ci/upload/26.7.9/a"; echo y > "$U/src/.cache/ci/published.txt"; echo evil > "$U/src/tools/ci/publish.sh"
(cd "$U/src" && tar -cf ../good.tar .cache/ci/upload .cache/ci/published.txt && tar -cf ../script.tar .cache/ci/upload tools/ci/publish.sh)
ln -s /etc/passwd "$U/src/.cache/ci/sums/link"; (cd "$U/src" && tar -cf ../link.tar .cache/ci/sums)
(cd "$U/src" && tar -cf ../dots.tar --transform 's,^\.cache/ci/upload,.cache/ci/upload/../../tools,' .cache/ci/upload)
(cd "$U/src" && tar -cf ../other.tar --transform 's,^\.cache/ci/upload,.cache/ci/other,' .cache/ci/upload)
unpack() { rm -rf "$U/out"; mkdir -p "$U/out"; (cd "$U/out" && "$HERE/unpack-input.sh" "$U/$1.tar" 2>&1) || echo "unpack exited $?"; }
out=$(unpack good); check "unpack: hand-over files" "$out" "exited" absent
if [ -f "$U/out/.cache/ci/upload/26.7.9/a" ]; then echo "ok   unpack: file is there"; else echo "FAIL unpack: file missing"; fails=$((fails + 1)); fi
out=$(unpack script); check "unpack: a script is refused" "$out" "'tools/ci/publish.sh' is outside the hand-over paths"
if [ ! -e "$U/out/tools" ]; then echo "ok   unpack: nothing written when refused"; else echo "FAIL unpack: wrote despite refusal"; fails=$((fails + 1)); fi
out=$(unpack link); check "unpack: a link is refused" "$out" "is not a plain file or directory"
out=$(unpack dots); check "unpack: .. is refused" "$out" "outside the hand-over paths"
out=$(unpack other); check "unpack: another .cache/ci path is refused" "$out" "'.cache/ci/other/' is outside the hand-over paths"

echo
if [ "$fails" -gt 0 ]; then echo "$fails failed"; exit 1; fi
echo "all passed"
