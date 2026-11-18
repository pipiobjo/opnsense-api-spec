#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Build the capability data (docs/capabilities.md) and publish it: one
# immutable pre-release per data state, tagged catalog-v<format>-<data_id>
# (data_id is derived from the content, see docs/capabilities.md), and the
# rolling release "catalog" that always carries the newest state.
#
#   tools/ci/catalog.sh build            -> .cache/ci/catalog/{catalog.json,capabilities.tar.zst,SHA256SUMS}
#   tools/ci/catalog.sh changed          -> prints true/false (differs from the published one)
#   tools/ci/catalog.sh publish [--dry-run]
#
# build runs the generator (the pipeline's prepare job, read-only token);
# changed and publish need only gh and jq and the built files (publish runs
# in the upload job, which runs nothing of the build; ADR 0015).
#
# build is deterministic: the same dist/ gives byte-identical assets and
# the same data_id, so publish compares SHA256SUMS with the published one
# and uploads only when something changed. Consumers that need a fixed
# state (a provider that embeds the data) pin the tag catalog-v1-<data_id>;
# its assets are never replaced. "catalog" is a pointer for the newest.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/.cache/ci/catalog"
TAG=catalog # rolling pointer to the newest state
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

| Data state | \`$(jq -r .data_id "$OUT/catalog.json")\`; fixed copy: release \`$(state_tag)\` |

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

# state_tag: the immutable release of the built data state
state_tag() {
  local f id
  f=$(jq -r .format "$OUT/catalog.json"); id=$(jq -r '.data_id // ""' "$OUT/catalog.json")
  [[ "$id" =~ ^[0-9a-f]{16}$ ]] || { echo "error: catalog.json has no valid data_id" >&2; exit 1; }
  echo "catalog-v$f-$id"
}

changed() {
  [ -f "$OUT/SHA256SUMS" ] || { echo "error: run 'catalog.sh build' first" >&2; exit 1; }
  local prev
  prev=$(gh release download "$TAG" -p SHA256SUMS -O - 2>/dev/null || true)
  if [ "$prev" = "$(cat "$OUT/SHA256SUMS")" ]; then echo false; else echo true; fi
}

publish() {
  local dry="${1:-}" fixed target body
  run() { if [ -n "$dry" ]; then echo "dry-run: $*"; else "$@"; fi; }
  if [ "$(changed)" = false ]; then
    echo "catalog unchanged"
    return
  fi
  fixed=$(state_tag)
  target="${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
  body=$(mktemp)
  # 1. the immutable release of this state: created once, never edited
  if gh release view "$fixed" >/dev/null 2>&1; then
    echo "release $fixed exists (same data_id, same content); left as it is"
  else
    notes "This release is never changed; a new data state gets a new release." > "$body"
    echo "create release $fixed"
    run gh release create "$fixed" --target "$target" --title "Capability data ${fixed#catalog-}" \
      --notes-file "$body" --prerelease --latest=false "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
  fi
  # 2. the rolling pointer to the newest state
  notes "This release is replaced in place when the data changes; pin $fixed for a fixed state." > "$body"
  if gh release view "$TAG" >/dev/null 2>&1; then
    echo "update release $TAG -> $fixed"
    run gh release edit "$TAG" --title "Capability data (newest)" --notes-file "$body" --prerelease --latest=false
    run gh release upload "$TAG" --clobber "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
  else
    echo "create release $TAG -> $fixed"
    run gh release create "$TAG" --target "$target" --title "Capability data (newest)" \
      --notes-file "$body" --prerelease --latest=false "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
  fi
  rm -f "$body"
}

case "${1:-}" in
  build) build ;;
  changed) changed ;;
  tag) state_tag ;;
  publish) publish "$([ "${2:-}" = "--dry-run" ] && echo 1)" ;;
  *) echo "usage: $0 build | changed | tag | publish [--dry-run]" >&2; exit 2 ;;
esac
