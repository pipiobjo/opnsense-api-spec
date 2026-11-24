#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Build the capability data (docs/capabilities.md) and publish it:
#
#   catalog-v<format>-<data_id>  one release per data state (data_id is
#                                derived from the content); created as a
#                                draft, filled, then published, and never
#                                changed afterwards
#   catalog-v<format>            the pointer: a release without assets whose
#                                notes name the newest state in the line
#                                "data_id: <data_id>"; only the notes change
#                                (they stay editable under GitHub's Immutable
#                                Releases, the tag never moves)
#   catalog                      the newest state's assets, replaced in place
#                                (kept until the consumers read the pointer)
#
#   tools/ci/catalog.sh build            -> .cache/ci/catalog/{catalog.json,capabilities.tar.zst,SHA256SUMS}
#   tools/ci/catalog.sh changed          -> prints true/false (differs from the published one)
#   tools/ci/catalog.sh tag              -> catalog-v<format>-<data_id> of the build
#   tools/ci/catalog.sh pointer          -> catalog-v<format> of the build
#   tools/ci/catalog.sh published-id     -> data_id the pointer names (empty: none)
#   tools/ci/catalog.sh publish [--dry-run]
#
# build runs the generator (the pipeline's prepare job, read-only token);
# changed and publish need only gh and jq and the built files (publish runs
# in the upload job, which runs nothing of the build; ADR 0015).
#
# build is deterministic: the same dist/ gives byte-identical assets and
# the same data_id. publish creates the fixed release first and moves the
# pointer only when it is complete, so a consumer that reads the pointer
# and then downloads catalog-v1-<data_id> never sees a mixed set. A fixed
# release that already exists is compared with the build (SHA256SUMS) and
# left only when equal; a different one is an identity error and fails the
# run. tools/ci/check-published.sh catalog then checks the published state
# as a consumer, including the data_id recomputed from the content.
set -euo pipefail
shopt -s inherit_errexit # a failure inside $(...) ends the script too

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/.cache/ci/catalog"
LEGACY=catalog # the newest state's assets, replaced in place
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
# shellcheck source=tools/ci/gh-lib.sh
. "$ROOT/tools/ci/gh-lib.sh"
DIST="${DIST:-$ROOT/dist}"

build() {
  local work="$ROOT/.cache/ci/catalog.work"
  rm -rf "$OUT" "$work" "$work.2"
  mkdir -p "$OUT"
  (cd "$ROOT" && go build -o "$work.bin" ./cmd/opnsense-api-spec)
  "$work.bin" capabilities -dist "$DIST" -out "$work"
  # the data_id is only an identity if the build is deterministic: build
  # a second time and compare (map order once made it differ per run)
  "$work.bin" capabilities -dist "$DIST" -out "$work.2" >/dev/null
  if ! diff -rq "$work" "$work.2" >&2; then
    echo "error: two builds of the capability data differ; the build is not deterministic" >&2
    exit 1
  fi
  rm -rf "$work.2" "$work.bin"
  cp "$work/catalog.json" "$OUT/"
  # reproducible archive: fixed order, owners and times. Everything the
  # generator wrote except catalog.json (a separate asset): the data_id
  # covers all of it, so a file left out here would be in the identity but
  # not in the release (version-order.json was, in catalog-v1-386ea34c45d263ba)
  local entries
  mapfile -t entries < <(cd "$work" && LC_ALL=C ls -A | grep -vx catalog.json)
  tar -C "$work" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -cf - "${entries[@]}" | zstd -q -19 -o "$OUT/capabilities.tar.zst"
  (cd "$OUT" && sha256sum catalog.json capabilities.tar.zst > SHA256SUMS)
  rm -rf "$work"
  cat "$OUT/SHA256SUMS"
}

notes() {
  local generator commit repo_url
  generator=$(jq -r '.generator // "?"' "$OUT/catalog.json")
  commit="${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
  repo_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
  cat <<EOF
Capability data of all OPNsense releases, core and plugins: every resource,
field and action with availability per package version, and the contract
test results. Format and use: [docs/capabilities.md]($repo_url/blob/main/docs/capabilities.md).
Not an official OPNsense project.

| | |
|---|---|
| Generator | $generator, commit [\`${commit:0:12}\`]($repo_url/commit/$commit) |
| Verify | \`sha256sum -c SHA256SUMS\`; provenance: \`gh attestation verify <file> -R ${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec} --signer-workflow ${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}/.github/workflows/pipeline.yml --source-ref refs/heads/main\` |

| Data state | \`$(jq -r .data_id "$OUT/catalog.json")\`; fixed copy: release \`$(state_tag)\`; newest: release \`$(pointer_tag)\` |

Assets: \`catalog.json\` (releases, core versions, packages, \`data_id\`),
\`capabilities.tar.zst\` (one document per package, the JSON Schema and the
version-order test table),
\`SHA256SUMS\`. $1

Pre-release: the data format may still change before the provider that
uses it is released (docs/capabilities.md, stability).

Licence: BSD-2-Clause, see LICENSE and NOTICE in the repository. OPNsense is
a registered trademark of Deciso B.V.
EOF
}

built_id() {
  local id; id=$(jq -r '.data_id // ""' "$OUT/catalog.json")
  [[ "$id" =~ ^[0-9a-f]{16}$ ]] || { echo "error: catalog.json has no valid data_id" >&2; exit 1; }
  echo "$id"
}
pointer_tag() {
  local f; f=$(jq -r .format "$OUT/catalog.json")
  [[ "$f" =~ ^[1-9][0-9]*$ ]] || { echo "error: catalog.json has no valid format" >&2; exit 1; }
  echo "catalog-v$f"
}
# state_tag: the immutable release of the built data state
state_tag() { echo "$(pointer_tag)-$(built_id)"; }

# pointer_id TAG: the data_id the pointer release TAG names; empty when it
# does not exist; fails when it cannot be read or names nothing valid
pointer_id() {
  local status body id
  status=$(release_status "$REPO" "$1")
  case "$status" in
    404) return 0 ;;
    200) ;;
    *) echo "error: cannot read release $1 (HTTP $status)" >&2; return 1 ;;
  esac
  body=$(gh api "repos/$REPO/releases/tags/$1" --jq .body)
  id=$(sed -n 's/^data_id: \([0-9a-f]\{16\}\)\r\{0,1\}$/\1/p' <<<"$body")
  [ "$(grep -c . <<<"$id")" = 1 ] || { echo "error: release $1 names no single 'data_id: <16 hex>' line" >&2; return 1; }
  echo "$id"
}

# changed: the pointer does not name the built state, or the legacy
# release does not carry its files
changed() {
  [ -f "$OUT/SHA256SUMS" ] || { echo "error: run 'catalog.sh build' first" >&2; exit 1; }
  local id legacy
  id=$(pointer_id "$(pointer_tag)")
  legacy=$(legacy_sums)
  if [ "$id" = "$(built_id)" ] && [ "$legacy" = "$(cat "$OUT/SHA256SUMS")" ]; then echo false; else echo true; fi
}

# legacy_sums: SHA256SUMS of the release "catalog" (empty: none)
legacy_sums() {
  case "$(release_status "$REPO" "$LEGACY")" in
    404) return 0 ;;
    200) gh release download "$LEGACY" -R "$REPO" -p SHA256SUMS -O - ;;
    *) echo "error: cannot read release $LEGACY" >&2; return 1 ;;
  esac
}

publish() {
  local dry="${1:-}" fixed pointer id target body status have drafts d ch
  run() { if [ -n "$dry" ]; then echo "dry-run: $*"; else "$@"; fi; }
  # an assignment, so a read failure ends the script (inside [ ] it would not)
  ch=$(changed)
  if [ "$ch" = false ]; then
    echo "catalog unchanged"
    return
  fi
  fixed=$(state_tag); pointer=$(pointer_tag); id=$(built_id)
  target="${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
  body=$(mktemp)
  # 1. the fixed release of this state: a draft first (invisible, still
  # changeable; a draft left by an interrupted run is deleted), published
  # only when all assets are there
  status=$(release_status "$REPO" "$fixed")
  case "$status" in
    200)
      have=$(gh release download "$fixed" -R "$REPO" -p SHA256SUMS -O -)
      if [ "$have" != "$(cat "$OUT/SHA256SUMS")" ]; then
        echo "error: release $fixed exists with other files than this build of the same data_id; the identity is broken (docs/capabilities.md, Identity and releases)" >&2
        diff <(echo "$have") "$OUT/SHA256SUMS" >&2 || true
        exit 1
      fi
      echo "release $fixed exists with the same files; left as it is" ;;
    404)
      drafts=$(gh api --paginate "repos/$REPO/releases?per_page=100" --jq ".[] | select(.draft and .tag_name == \"$fixed\") | .id")
      for d in $drafts; do
        echo "deleting draft $d of $fixed (left by an interrupted run)"
        run gh api -X DELETE "repos/$REPO/releases/$d"
      done
      notes "This release is never changed; a new data state gets a new release. The newest state: release $pointer." > "$body"
      echo "create release $fixed (draft, then published)"
      run gh release create "$fixed" -R "$REPO" --draft --target "$target" --title "Capability data ${fixed#catalog-}" \
        --notes-file "$body" --prerelease --latest=false "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
      run gh release edit "$fixed" -R "$REPO" --draft=false ;;
    *) echo "error: cannot read release $fixed (HTTP $status)" >&2; exit 1 ;;
  esac
  # 2. the pointer, only after the fixed release is complete
  {
    echo "data_id: $id"
    echo
    echo "The newest capability data in format ${pointer#catalog-v}: release [\`$fixed\`](${GITHUB_SERVER_URL:-https://github.com}/$REPO/releases/tag/$fixed)."
    echo "This release has no files and its tag never moves; only the line"
    echo "\`data_id: ...\` above changes. Read it, then download the release"
    echo "\`$pointer-<data_id>\`, which never changes (docs/capabilities.md, Identity and releases)."
  } > "$body"
  case "$(release_status "$REPO" "$pointer")" in
    200)
      echo "update release $pointer -> $id"
      run gh release edit "$pointer" -R "$REPO" --notes-file "$body" ;;
    404)
      echo "create release $pointer -> $id"
      run gh release create "$pointer" -R "$REPO" --target "$target" --title "Capability data format ${pointer#catalog-v} (newest)" \
        --notes-file "$body" --prerelease --latest=false ;;
    *) echo "error: cannot read release $pointer" >&2; exit 1 ;;
  esac
  # 3. the legacy release with the newest files, replaced in place
  notes "This release is replaced in place when the data changes; pin $fixed for a fixed state, or read the pointer $pointer." > "$body"
  case "$(release_status "$REPO" "$LEGACY")" in
    200)
      echo "update release $LEGACY -> $fixed"
      run gh release edit "$LEGACY" -R "$REPO" --title "Capability data (newest)" --notes-file "$body" --prerelease --latest=false
      run gh release upload "$LEGACY" -R "$REPO" --clobber "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS" ;;
    404)
      echo "create release $LEGACY -> $fixed"
      run gh release create "$LEGACY" -R "$REPO" --target "$target" --title "Capability data (newest)" \
        --notes-file "$body" --prerelease --latest=false "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS" ;;
    *) echo "error: cannot read release $LEGACY" >&2; exit 1 ;;
  esac
  rm -f "$body"
}

case "${1:-}" in
  build) build ;;
  changed) changed ;;
  tag) state_tag ;;
  pointer) pointer_tag ;;
  published-id) pointer_id "catalog-v${2:-1}" ;;
  publish) publish "$([ "${2:-}" = "--dry-run" ] && echo 1)" ;;
  *) echo "usage: $0 build | changed | tag | pointer | published-id [FORMAT] | publish [--dry-run]" >&2; exit 2 ;;
esac
