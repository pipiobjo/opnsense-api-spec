#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Check what a live run just published, the way a consumer gets it: every
# asset downloaded without credentials, compared with SHA256SUMS, and
# SHA256SUMS checked against its build provenance attestation.
#
#   tools/ci/check-published.sh
#
# Input: .cache/ci/published.txt (releases published by tools/ci/publish.sh)
# and, if the capability data was published, the release "catalog". A
# failure fails the run; the next scheduled run publishes again.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
BASE="${GITHUB_SERVER_URL:-https://github.com}/$REPO/releases/download"
LIST="$ROOT/.cache/ci/published.txt"
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT

fetch() { # fetch TAG FILE: anonymous download into $WORK/TAG/
  mkdir -p "$WORK/$1"
  env -u GH_TOKEN -u GITHUB_TOKEN curl -fsSL --retry 3 -o "$WORK/$1/$2" "$BASE/$1/$2"
}

check_release() { # check_release TAG
  local tag="$1" f
  fetch "$tag" SHA256SUMS
  while read -r _ f; do fetch "$tag" "$f"; done < "$WORK/$tag/SHA256SUMS"
  (cd "$WORK/$tag" && sha256sum --quiet -c SHA256SUMS)
  gh attestation verify "$WORK/$tag/SHA256SUMS" -R "$REPO" >/dev/null
  echo "ok $tag: $(wc -l < "$WORK/$tag/SHA256SUMS") assets, checksums and attestation"
}

n=0
if [ -s "$LIST" ]; then
  while read -r rel; do check_release "opnsense-$rel"; n=$((n + 1)); done < "$LIST"
fi
if [ "${CATALOG_LIVE:-}" = true ]; then
  check_release catalog; n=$((n + 1))
fi
echo "checked $n release(s) as an anonymous consumer"
