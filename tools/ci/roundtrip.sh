#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Publishing round trip without publishing (promote.yml, ADR 0012): the
# files of one release are uploaded to a draft release with the same code
# that publishes (assets.sh), downloaded again from GitHub, and checked:
# the asset list equals SHA256SUMS plus SHA256SUMS itself, and every file
# matches its checksum. The draft is deleted in every case. A draft is
# never visible to anyone but the repository's writers.
#
#   tools/ci/roundtrip.sh DIST [RELEASE]     (default: newest release in DIST)
#
# This tests GitHub's real behaviour (asset names, uploads), which a fake
# cannot: GitHub renames some asset names ("a..b.json" becomes "a.b.json"),
# and the checksums must name what was actually stored (ADR 0012).
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=tools/ci/assets.sh
. "$HERE/assets.sh"
DIST="${1:?usage: roundtrip.sh DIST [RELEASE]}"
REL="${2:-$(jq -r '.releases[-1].release' "$DIST/index.json")}"
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
TAG="roundtrip-${GITHUB_RUN_ID:-local-$(date -u +%s)}"
WORK=$(mktemp -d)
cleanup() {
  gh release delete "$TAG" -R "$REPO" --yes --cleanup-tag >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

stage_release "$DIST/$REL" "$WORK/stage" "$WORK/SHA256SUMS"
cp "$WORK/SHA256SUMS" "$WORK/stage/SHA256SUMS"
mapfile -t files < <(find "$WORK/stage" -maxdepth 1 -type f | sort)
gh release create "$TAG" -R "$REPO" --draft --title "Round trip (deleted automatically)" \
  --notes "Temporary draft of promote.yml; deleted at the end of the job." "${files[@]}" >/dev/null
echo "draft $TAG: uploaded ${#files[@]} files of $REL"

# what GitHub stored, by name
published=$(gh api "repos/$REPO/releases?per_page=100" \
  --jq ".[] | select(.draft and .tag_name == \"$TAG\") | .assets[].name" | sort)
listed=$( (awk '{print $2}' "$WORK/SHA256SUMS"; echo SHA256SUMS) | sort)
if [ "$listed" != "$published" ]; then
  echo "error: GitHub stored other asset names than SHA256SUMS lists:" >&2
  diff <(echo "$listed") <(echo "$published") >&2 || true
  exit 1
fi
mkdir "$WORK/down"
gh release download "$TAG" -R "$REPO" -D "$WORK/down" >/dev/null
(cd "$WORK/down" && sha256sum --quiet -c SHA256SUMS)
echo "ok: $REL round trip, ${#files[@]} assets, names and checksums as published"
