#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Build the capability data (docs/capabilities.md) and publish it as the
# rolling GitHub release "catalog".
#
#   tools/ci/catalog.sh build            -> .cache/ci/catalog/{catalog.json,capabilities.tar.zst,SHA256SUMS}
#   tools/ci/catalog.sh changed          -> prints true/false (differs from the published one)
#   tools/ci/catalog.sh publish [--dry-run]
#
# build is deterministic: the same dist/ gives byte-identical assets, so
# publish compares SHA256SUMS with the published one and uploads only when
# something changed. Consumers pin the hash of SHA256SUMS.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
OUT="$ROOT/.cache/ci/catalog"
TAG=catalog
DIST="${DIST:-$ROOT/dist}"

build() {
  local work="$ROOT/.cache/ci/catalog.work"
  rm -rf "$OUT" "$work"
  mkdir -p "$OUT"
  (cd "$ROOT" && go run ./cmd/opnsense-api-spec capabilities -dist "$DIST" -out "$work")
  cp "$work/catalog.json" "$OUT/"
  # reproducible archive: fixed order, owners and times
  tar -C "$work" --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
    -cf - capabilities capability.schema.json | zstd -q -19 -o "$OUT/capabilities.tar.zst"
  (cd "$OUT" && sha256sum catalog.json capabilities.tar.zst > SHA256SUMS)
  rm -rf "$work"
  cat "$OUT/SHA256SUMS"
}

notes() {
  local generator commit repo_url
  generator=$(jq -r .generator "$DIST/index.json" 2>/dev/null || echo "?")
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
| Verify | \`sha256sum -c SHA256SUMS\`; provenance: \`gh attestation verify <file> -R ${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}\` |

Assets: \`catalog.json\` (releases, core versions, packages),
\`capabilities.tar.zst\` (one document per package and the JSON Schema),
\`SHA256SUMS\`. This release is replaced in place when the data changes.

Licence: BSD-2-Clause, see LICENSE and NOTICE in the repository. OPNsense is
a registered trademark of Deciso B.V.
EOF
}

changed() {
  [ -f "$OUT/SHA256SUMS" ] || { echo "error: run 'catalog.sh build' first" >&2; exit 1; }
  local prev
  prev=$(gh release download "$TAG" -p SHA256SUMS -O - 2>/dev/null || true)
  if [ "$prev" = "$(cat "$OUT/SHA256SUMS")" ]; then echo false; else echo true; fi
}

publish() {
  local dry="${1:-}"
  run() { if [ -n "$dry" ]; then echo "dry-run: $*"; else "$@"; fi; }
  if [ "$(changed)" = false ]; then
    echo "catalog unchanged"
    return
  fi
  local body; body=$(mktemp); notes > "$body"
  if gh release view "$TAG" >/dev/null 2>&1; then
    echo "update release $TAG"
    run gh release edit "$TAG" --title "Capability data" --notes-file "$body" --prerelease=false --latest=false
    run gh release upload "$TAG" --clobber "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
  else
    echo "create release $TAG"
    run gh release create "$TAG" --target "${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}" --title "Capability data" \
      --notes-file "$body" --latest=false "$OUT/catalog.json" "$OUT/capabilities.tar.zst" "$OUT/SHA256SUMS"
  fi
  rm -f "$body"
}

case "${1:-}" in
  build) build ;;
  changed) changed ;;
  publish) publish "$([ "${2:-}" = "--dry-run" ] && echo 1)" ;;
  *) echo "usage: $0 build | changed | publish [--dry-run]" >&2; exit 2 ;;
esac
