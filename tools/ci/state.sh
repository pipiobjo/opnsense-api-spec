#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Pipeline state (ADR 0008): the complete dist/ of the last successful run,
# published as dist.tar.zst on the rolling GitHub release "state". With it a
# CI run behaves like a local one: unchanged releases are skipped, hotfix
# history, fragments and contract test results are kept.
#
#   tools/ci/state.sh restore   download, check provenance, unpack into dist/;
#                               the previous index goes to
#                               .cache/ci/index.prev.json
#   tools/ci/state.sh pack      pack dist/ into .cache/ci/state/ (the job
#                               that built it; needs no rights); refuses a
#                               state with fewer contract test records
#   tools/ci/state.sh save      upload .cache/ci/state/ (the publishing job,
#                               which runs nothing of the build), keeping
#                               the state it replaces as previous-*
#
# Only a state release that does not exist (HTTP 404) starts from an empty
# dist/: everything is rebuilt and no release counts as verified. Any other
# read failure fails the run; taking it for "no state" would throw away
# every contract test result (finding REL-2).
#
# Provenance: the upload job attests dist.tar.zst and index.json before it
# saves them; restore accepts dist.tar.zst only with an attestation of
# pipeline.yml on main. Without one, a live run (LIVE=true) fails unless
# STATE_ACCEPT_UNATTESTED=1 (workflow_dispatch input, once, for a state
# saved before attestation existed or repaired by hand); other runs warn,
# they publish nothing.
#
# Shrinking guard: pack fails when dist/ has fewer releases with a contract
# test record (index.json "verified") than the restored state, unless
# STATE_ALLOW_FEWER_VERIFIED=1 (workflow_dispatch input). A new generator
# makes records stale, it does not remove them; losing them means the
# history was lost, and saving that would make it permanent.
#
# Repair: the release keeps the state it replaced as previous-dist.tar.zst
# and previous-index.json (still attested: an attestation is bound to the
# content). Uploading them as dist.tar.zst and index.json rolls back one run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TAG="state"
CI="$ROOT/.cache/ci"
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
mkdir -p "$CI"
# shellcheck source=tools/ci/gh-lib.sh
. "$ROOT/tools/ci/gh-lib.sh"

# verified_records INDEX: releases with a contract test record
verified_records() { jq '[.releases[] | select(.verified != null)] | length' "$1"; }

attested() { # FILE: attested by pipeline.yml on main
  gh attestation verify "$1" -R "$REPO" \
    --signer-workflow "$REPO/.github/workflows/pipeline.yml" --source-ref refs/heads/main >/dev/null
}

case "${1:-}" in
  restore)
    rm -f "$CI/dist.tar.zst"
    status=$(release_status "$REPO" "$TAG")
    case "$status" in
      404)
        echo "::warning::no published state (release '$TAG' does not exist): building from scratch, nothing counts as verified"
        echo '{"releases":[]}' > "$CI/index.prev.json"
        exit 0 ;;
      200) ;;
      *) echo "error: cannot read the release '$TAG' (HTTP $status); not taking that for 'no state'" >&2; exit 1 ;;
    esac
    gh release download "$TAG" -R "$REPO" -p dist.tar.zst -D "$CI"
    if attested "$CI/dist.tar.zst"; then
      echo "state provenance: attested by pipeline.yml on main"
    elif [ "${STATE_ACCEPT_UNATTESTED:-}" = 1 ]; then
      echo "::warning::state has no attestation of pipeline.yml on main; accepted on request (STATE_ACCEPT_UNATTESTED)"
    elif [ "${LIVE:-}" = true ]; then
      echo "error: the state (release '$TAG', dist.tar.zst) has no attestation of pipeline.yml on main; a live run does not build on it (docs/operations.md, 'State')" >&2
      exit 1
    else
      echo "::warning::state has no attestation of pipeline.yml on main; used anyway because this run publishes nothing"
    fi
    [ -d "$ROOT/dist" ] && [ -n "$(ls -A "$ROOT/dist")" ] && { echo "error: dist/ is not empty" >&2; exit 1; }
    mkdir -p "$ROOT/dist"
    zstd -dc "$CI/dist.tar.zst" | tar -C "$ROOT/dist" --no-same-owner -xf -
    cp "$ROOT/dist/index.json" "$CI/index.prev.json"
    echo "state restored: $(jq '.releases | length' "$CI/index.prev.json") releases ($(verified_records "$CI/index.prev.json") with a contract test record), generator $(jq -r .generator "$CI/index.prev.json")"
    ;;
  pack)
    [ -f "$ROOT/dist/index.json" ] || { echo "error: dist/index.json missing" >&2; exit 1; }
    [ -f "$CI/index.prev.json" ] || { echo "error: $CI/index.prev.json missing (run: $0 restore)" >&2; exit 1; }
    before=$(verified_records "$CI/index.prev.json"); now=$(verified_records "$ROOT/dist/index.json")
    if [ "$now" -lt "$before" ]; then
      if [ "${STATE_ALLOW_FEWER_VERIFIED:-}" = 1 ]; then
        echo "::warning::state shrinks from $before to $now releases with a contract test record; allowed on request (STATE_ALLOW_FEWER_VERIFIED)"
      else
        echo "error: the new state has $now releases with a contract test record, the restored one $before; refusing to save a state that lost contract test history (dispatch with allow-fewer-verified if this is intended)" >&2
        exit 1
      fi
    fi
    rm -rf "$CI/state"; mkdir -p "$CI/state"
    tar -C "$ROOT/dist" --sort=name -cf - . | zstd -q -19 -T0 > "$CI/state/dist.tar.zst"
    cp "$ROOT/dist/index.json" "$CI/state/index.json"
    echo "state packed ($(du -h "$CI/state/dist.tar.zst" | cut -f1), $now releases with a contract test record, before $before)"
    ;;
  save)
    [ -f "$CI/state/dist.tar.zst" ] && [ -f "$CI/state/index.json" ] || { echo "error: nothing packed in $CI/state (run: $0 pack)" >&2; exit 1; }
    status=$(release_status "$REPO" "$TAG")
    case "$status" in
      404)
        gh release create "$TAG" -R "$REPO" --latest=false --title "Pipeline state" \
          --notes "Internal: complete dist/ of the last successful pipeline run (input of the next run), with the state before it as previous-*. Use the opnsense-<release> releases instead." ;;
      200)
        # keep what is replaced, for a rollback by hand
        old=$(mktemp -d)
        gh release download "$TAG" -R "$REPO" -p dist.tar.zst -p index.json -D "$old"
        mv "$old/dist.tar.zst" "$old/previous-dist.tar.zst"; mv "$old/index.json" "$old/previous-index.json"
        gh release upload "$TAG" -R "$REPO" --clobber "$old/previous-dist.tar.zst" "$old/previous-index.json"
        rm -rf "$old" ;;
      *) echo "error: cannot read the release '$TAG' (HTTP $status)" >&2; exit 1 ;;
    esac
    gh release upload "$TAG" -R "$REPO" --clobber "$CI/state/dist.tar.zst" "$CI/state/index.json"
    echo "state saved ($(du -h "$CI/state/dist.tar.zst" | cut -f1))"
    ;;
  *) sed -n '4,40p' "$0"; exit 2 ;;
esac
