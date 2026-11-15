#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Publish changed releases as GitHub releases (ADR 0008).
#
#   tools/ci/publish.sh [--dry-run]
#
# One GitHub release per OPNsense release, tag opnsense-<release>, with the
# release view of dist/<release>/ as assets. A release is (re)published when
# its index entry differs from the previous run (.cache/ci/index.prev.json:
# new release, hotfix composition, new generator, new contract test result,
# status change) or when its GitHub release is missing. Assets are replaced
# in place; the tag keeps pointing at the commit that first published it,
# the body names the generator commit of the current assets.
#
# Also creates the tag v<generator version> if it does not exist yet.
#
# Marked as pre-release while the current composition is not contract-tested
# (needs_verification). Only runs after all gates passed; it never decides
# anything itself.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIST="$ROOT/dist"
PREV="$ROOT/.cache/ci/index.prev.json"
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY=1

run() { if [ -n "$DRY" ]; then echo "dry-run: $*"; else "$@"; fi; }

[ -f "$DIST/index.json" ] || { echo "error: dist/index.json missing" >&2; exit 1; }
[ -f "$PREV" ] || { echo "error: $PREV missing (run tools/ci/state.sh restore first)" >&2; exit 1; }

commit="${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
repo_url="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
existing=$(gh release list --limit 1000 --json tagName --jq '.[].tagName')
newest=$(jq -r '.releases[-1].release' "$DIST/index.json")
generator=$(jq -r .generator "$DIST/index.json")

notes() { # notes RELEASE -> markdown body
  local rel="$1" e
  e=$(jq -c --arg r "$rel" '.releases[] | select(.release == $r)' "$DIST/index.json")
  cat <<EOF
API description (OpenAPI 3.1) of **OPNsense $rel**, core and all plugins,
generated from the signed release packages. Not an official OPNsense project.

| | |
|---|---|
| Composition | \`$(jq -r .current <<<"$e")\` |
| Status | $(jq -r 'if .status == "final" then "final (a newer release exists; this catalog state no longer changes)" else "provisional (newest release of its series; hotfixes may still change the catalog)" end' <<<"$e") |
| Generator | $generator, commit [\`${commit:0:12}\`]($repo_url/commit/$commit) |
| Source | $(jq -r 'if .source_unavailable then "**packages no longer available on the mirror** (since \(.source_unavailable.since)); these files are from the last build that had them" else "signed release packages, available on the mirror" end' <<<"$e") |
| Contract test | $(jq -r 'if .needs_verification then "not verified for this composition" + (if .verified then " (last run: test instance \(.verified.instance), \(.verified.date), \(.verified.failed) failed, \(.verified.untested_packages // [] | length) packages untested)" else "" end) else "verified against test instance \(.verified.instance), \(.verified.date), 0 failed" end' <<<"$e") |

Assets: \`opnsense-$rel.openapi.json\` (the spec), \`.ir.json\` (intermediate
representation), \`.coverage.md\` (what is typed and what is not),
\`.verified.json\` (contract test results, if any), \`composition.json\`
(packages and fragments of this catalog state), diff and quality report
against the previous release. Limitations are marked in the spec itself
(\`x-opnsense-derivation\`, \`x-opnsense-verified\`).

Licence: BSD-2-Clause, see LICENSE and NOTICE in the repository. OPNsense is
a registered trademark of Deciso B.V.
EOF
}

published=0
for rel in $(jq -r '.releases[].release' "$DIST/index.json"); do
  tag="opnsense-$rel"
  now=$(jq -S -c --arg r "$rel" '.releases[] | select(.release == $r)' "$DIST/index.json")
  before=$(jq -S -c --arg r "$rel" '.releases[] | select(.release == $r)' "$PREV")
  if [ "$now" = "$before" ] && grep -qx "$tag" <<<"$existing"; then
    continue
  fi
  pre=$(jq -r .needs_verification <<<"$now")
  latest=false; [ "$rel" = "$newest" ] && [ "$pre" = false ] && latest=true
  mapfile -t assets < <(find "$DIST/$rel" -maxdepth 1 -type f | sort)
  [ "${#assets[@]}" -gt 0 ] || { echo "error: no files in dist/$rel" >&2; exit 1; }
  body=$(mktemp); notes "$rel" > "$body"
  if grep -qx "$tag" <<<"$existing"; then
    echo "update $tag (pre-release=$pre)"
    run gh release edit "$tag" --title "OPNsense $rel" --notes-file "$body" --prerelease="$pre" --latest="$latest"
    run gh release upload "$tag" --clobber "${assets[@]}"
  else
    echo "create $tag (pre-release=$pre)"
    run gh release create "$tag" --target "$commit" --title "OPNsense $rel" --notes-file "$body" \
      --prerelease="$pre" --latest="$latest" "${assets[@]}"
  fi
  rm -f "$body"
  published=$((published + 1))
done
echo "published $published release(s)"

# The generator version named in the outputs must exist as a tag: create
# v<version> on the first commit that publishes it. Never move it.
vtag="v$generator"
if gh api "repos/{owner}/{repo}/git/ref/tags/$vtag" >/dev/null 2>&1; then
  echo "tag $vtag exists"
elif [ "$published" -gt 0 ]; then
  echo "create tag $vtag at ${commit:0:12}"
  run gh api "repos/{owner}/{repo}/git/refs" -f ref="refs/tags/$vtag" -f sha="$commit" >/dev/null
fi
