#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Check what a live run just published, the way a consumer gets it: every
# asset downloaded without credentials, compared with SHA256SUMS, and
# SHA256SUMS checked against its build provenance attestation.
#
#   tools/ci/check-published.sh releases   the releases in .cache/ci/published.txt
#                                          (written by tools/ci/publish.sh plan)
#   tools/ci/check-published.sh catalog [DATA_ID]
#                                          the capability data: the state the
#                                          pointer catalog-v1 names (or, before
#                                          the pointer exists, the release
#                                          "catalog"), its fixed release
#                                          catalog-v1-<data_id> with the data_id
#                                          recomputed from the content, and
#                                          "catalog" carrying the same state
#
# "releases" runs in the upload job (gh, curl, shell; ADR 0015). "catalog"
# runs the generator's data-id command (DATA_ID, default go run), so it
# runs in a job with a read-only token after the upload job, in every run:
# a dry run checks what is public now.
#
# The releases are checked before the pipeline saves its state: a failure
# fails the run without recording it, so the next scheduled run publishes
# the same releases again (and publish.sh deletes assets that do not
# belong).
set -euo pipefail
shopt -s inherit_errexit # a failure inside $(...) ends the script too

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
BASE="${GITHUB_SERVER_URL:-https://github.com}/$REPO/releases/download"
LIST="$ROOT/.cache/ci/published.txt"
# shellcheck source=tools/ci/gh-lib.sh
. "$ROOT/tools/ci/gh-lib.sh"
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT

fetch() { # fetch TAG FILE: anonymous download into $WORK/TAG/
  mkdir -p "$WORK/$1"
  env -u GH_TOKEN -u GITHUB_TOKEN curl -fsSL --retry 5 --retry-all-errors --retry-delay 5 -o "$WORK/$1/$2" "$BASE/$1/$2"
}

check_release() { # check_release TAG
  local tag="$1" f
  fetch "$tag" SHA256SUMS
  while read -r _ f; do fetch "$tag" "$f"; done < "$WORK/$tag/SHA256SUMS"
  (cd "$WORK/$tag" && sha256sum --quiet -c SHA256SUMS)
  # the asset list must be exactly SHA256SUMS plus what it lists: an asset
  # GitHub renamed, or one left over from an earlier upload, fails here
  local listed published
  listed=$( (awk '{print $2}' "$WORK/$tag/SHA256SUMS"; echo SHA256SUMS) | sort)
  # (the listing through the API with the workflow token: anonymous API
  # calls are limited to 60 per hour and runner)
  published=$(gh api "repos/$REPO/releases/tags/$tag" --jq '.assets[].name' | sort)
  if [ "$listed" != "$published" ]; then
    echo "error: assets of $tag differ from SHA256SUMS:" >&2
    diff <(echo "$listed") <(echo "$published") >&2 || true
    return 1
  fi
  # only attestations from the publishing pipeline on main count (not a
  # dry run from another branch or workflow)
  gh attestation verify "$WORK/$tag/SHA256SUMS" -R "$REPO" \
    --signer-workflow "$REPO/.github/workflows/pipeline.yml" --source-ref refs/heads/main >/dev/null
  echo "ok $tag: $(wc -l < "$WORK/$tag/SHA256SUMS") assets, checksums and attestation"
}

# check_data TAG ID: the archive of TAG (already fetched) unpacked next to
# its catalog.json gives data_id ID, and catalog.json declares ID
check_data() {
  local d="$WORK/$1/data" got
  mkdir -p "$d"
  cp "$WORK/$1/catalog.json" "$d/"
  zstd -dc "$WORK/$1/capabilities.tar.zst" | tar -C "$d" --no-same-owner -xf -
  got=$(cd "$ROOT" && ${DATA_ID:-go run ./cmd/opnsense-api-spec data-id} "$d") \
    || { echo "error: $1: the content does not have the data_id its catalog.json declares (got ${got:-?})" >&2; return 1; }
  [ "$got" = "$2" ] || { echo "error: $1 has data_id $got, the pointer names $2" >&2; return 1; }
  echo "ok $1: data_id $got recomputed from the content"
}

check_catalog() { # [DATA_ID]: check that state instead of the pointer's (by hand)
  local id="${1:-}" legacy=0 draft
  if [ -n "$id" ]; then
    check_release "catalog-v1-$id"; check_data "catalog-v1-$id" "$id"; n=$((n + 1)); return
  fi
  id=$("$ROOT/tools/ci/catalog.sh" published-id 1)
  case "$(release_status "$REPO" catalog)" in
    200) legacy=1 ;;
    404) ;;
    *) echo "error: cannot read release catalog" >&2; return 1 ;;
  esac
  if [ -z "$id" ]; then
    # before the pointer exists: the legacy release names the state
    if [ "$legacy" = 0 ]; then
      echo "no capability data published"
      return 0
    fi
    fetch catalog catalog.json
    id=$(jq -r '.data_id // ""' "$WORK/catalog/catalog.json")
    [[ "$id" =~ ^[0-9a-f]{16}$ ]] || { echo "error: catalog/catalog.json has no valid data_id" >&2; return 1; }
    echo "pointer catalog-v1 not published yet; the release catalog names $id"
  fi
  check_release "catalog-v1-$id"
  draft=$(gh api "repos/$REPO/releases/tags/catalog-v1-$id" --jq '.draft')
  [ "$draft" = false ] || { echo "error: catalog-v1-$id is a draft" >&2; return 1; }
  check_data "catalog-v1-$id" "$id"
  n=$((n + 1))
  if [ "$legacy" = 1 ]; then
    check_release catalog
    cmp -s "$WORK/catalog/SHA256SUMS" "$WORK/catalog-v1-$id/SHA256SUMS" \
      || { echo "error: release catalog does not carry the files of catalog-v1-$id" >&2; return 1; }
    n=$((n + 1))
  fi
}

n=0
case "${1:-}" in
  releases)
    if [ -s "$LIST" ]; then
      while read -r rel; do check_release "opnsense-$rel"; n=$((n + 1)); done < "$LIST"
    fi ;;
  catalog) check_catalog "${2:-}" ;;
  *) echo "usage: $0 releases | catalog" >&2; exit 2 ;;
esac
echo "checked $n release(s) as an anonymous consumer"
