#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Pipeline state (ADR 0008): the complete dist/ of the last successful run,
# published as dist.tar.zst on the rolling GitHub release "state". With it a
# CI run behaves like a local one: unchanged releases are skipped, hotfix
# history, fragments and contract test results are kept.
#
#   tools/ci/state.sh restore   download + unpack into dist/; the previous
#                               index goes to .cache/ci/index.prev.json
#   tools/ci/state.sh save      pack dist/ and upload it (publish job only)
#
# Without a published state, restore starts from an empty dist/ and says so:
# everything is rebuilt and no release counts as verified.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TAG="state"
CI="$ROOT/.cache/ci"
mkdir -p "$CI"

case "${1:-}" in
  restore)
    rm -f "$CI/dist.tar.zst"
    if ! gh release view "$TAG" >/dev/null 2>&1; then
      echo "::warning::no published state (release '$TAG'): building from scratch, nothing counts as verified"
      echo '{"releases":[]}' > "$CI/index.prev.json"
      exit 0
    fi
    gh release download "$TAG" -p dist.tar.zst -D "$CI"
    [ -d "$ROOT/dist" ] && [ -n "$(ls -A "$ROOT/dist")" ] && { echo "error: dist/ is not empty" >&2; exit 1; }
    mkdir -p "$ROOT/dist"
    zstd -dc "$CI/dist.tar.zst" | tar -C "$ROOT/dist" -xf -
    cp "$ROOT/dist/index.json" "$CI/index.prev.json"
    echo "state restored: $(jq '.releases | length' "$CI/index.prev.json") releases, generator $(jq -r .generator "$CI/index.prev.json")"
    ;;
  save)
    [ -f "$ROOT/dist/index.json" ] || { echo "error: dist/index.json missing" >&2; exit 1; }
    tar -C "$ROOT/dist" --sort=name -cf - . | zstd -q -19 -T0 > "$CI/dist.tar.zst"
    if ! gh release view "$TAG" >/dev/null 2>&1; then
      gh release create "$TAG" --latest=false --title "Pipeline state" \
        --notes "Internal: complete dist/ of the last successful pipeline run (input of the next run). Use the opnsense-<release> releases instead."
    fi
    gh release upload "$TAG" --clobber "$CI/dist.tar.zst" "$ROOT/dist/index.json"
    echo "state saved ($(du -h "$CI/dist.tar.zst" | cut -f1))"
    ;;
  *) sed -n '4,15p' "$0"; exit 2 ;;
esac
