#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Publish changed releases as GitHub releases (ADR 0008), in two steps that
# run in two jobs with different rights (ADR 0015):
#
#   tools/ci/publish.sh plan              decide and stage; reads GitHub only
#   tools/ci/publish.sh upload [--dry-run]
#                                         carry out the plan with gh; runs no
#                                         Go and nothing else of the build
#
# plan runs in the job that ran the generator and its dependencies; its
# token can only read. It writes everything upload needs to .cache/ci/upload/
# (releases.tsv, per release the staged assets and the release notes,
# generator.env and the generator SBOM), which the pipeline hands over as
# an artifact. upload runs in the job that may write releases and request
# an attestation; it treats the plan as data: tags and file names are
# checked against fixed patterns, nothing in it is executed.
#
# One GitHub release per OPNsense release, tag opnsense-<release>, with the
# release view of dist/<release>/ as assets. A release is (re)published when
# its index entry differs from the previous run (.cache/ci/index.prev.json:
# new release, hotfix composition, new generator, new contract test result,
# status change), when its GitHub release is missing, or when its published
# files differ from the staged ones: SHA256SUMS or the list of asset names
# (a file a newer generator renamed or dropped). upload replaces assets in
# place and then deletes every asset of the release that the plan does not
# list, so the next run repairs what check-published.sh found. The tag
# keeps pointing at the commit that first published it; the body names the
# generator commit of the current assets.
#
# GitHub stores asset names in its own form: characters outside
# [A-Za-z0-9._+-] become ".", runs of dots collapse ("26.7.8..26.7.9.json"
# is stored as "26.7.8.26.7.9.json"). Assets are staged under that name
# first, so SHA256SUMS lists what consumers actually download.
#
# Every published release gets SHA256SUMS over its assets (also in
# .cache/ci/sums/<release>/ for the provenance attestation step); the list of
# published releases goes to .cache/ci/published.txt for
# tools/ci/check-published.sh.
#
# Also creates the tag v<generator version> if it does not exist yet. With
# SBOM=1 (repository variable SBOM_PUBLISH) the new tag also gets a GitHub
# release with the generator's SBOM (tools/ci/sbom.sh) and SHA256SUMS,
# in .cache/ci/generator/ for the provenance attestation step; the
# per-release SBOMs (opnsense-<release>.sbom.cdx.json, from the command
# sbom) are ordinary release files.
#
# Marked as pre-release while the current composition is not contract-tested
# (needs_verification). Only runs after all gates passed; it never decides
# anything itself.
set -euo pipefail
export LC_ALL=C # one sort order for asset names (comm, jq)

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DIST="$ROOT/dist"
CI="$ROOT/.cache/ci"
PREV="$CI/index.prev.json"
UP="$CI/upload"
SUMS="$CI/sums"
GEN="$CI/generator"
REPO="${GITHUB_REPOSITORY:-pipiobjo/opnsense-api-spec}"
# the only shapes upload accepts from the plan
TAG_RE='^opnsense-[0-9]+\.[0-9]+(\.[0-9]+)?(_[0-9]+)?$'
VTAG_RE='^v[0-9]+\.[0-9]+\.[0-9]+$'
ASSET_RE='^[A-Za-z0-9_+-][A-Za-z0-9._+-]*$'
SHA_RE='^[0-9a-f]{40}$'

# shellcheck source=tools/ci/assets.sh
. "$(dirname "${BASH_SOURCE[0]}")/assets.sh"

die() { echo "error: $*" >&2; exit 1; }

notes() { # notes RELEASE COMMIT -> markdown body
  local rel="$1" commit="$2" e repo_url generator
  repo_url="${GITHUB_SERVER_URL:-https://github.com}/$REPO"
  generator=$(jq -r .generator "$DIST/index.json")
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
against the previous release.$( [ -f "$DIST/$rel/opnsense-$rel.sbom.cdx.json" ] && printf '%s' " \`.sbom.cdx.json\`: software bill of materials (CycloneDX 1.6) of the OPNsense packages this description was derived from, with their checksums from the signed catalog." ) Limitations are marked in the spec itself
(\`x-opnsense-derivation\`, \`x-opnsense-verified\`).

Licence: BSD-2-Clause, see LICENSE and NOTICE in the repository. OPNsense is
a registered trademark of Deciso B.V.
EOF
}

# ------------------------------------------------------------ plan

plan() {
  [ -f "$DIST/index.json" ] || die "dist/index.json missing"
  [ -f "$PREV" ] || die "$PREV missing (run tools/ci/state.sh restore first)"
  local commit newest generator published existing rel tag now before stage pre latest action listed have
  commit="${GITHUB_SHA:-$(git -C "$ROOT" rev-parse HEAD)}"
  newest=$(jq -r '.releases[-1].release' "$DIST/index.json")
  generator=$(jq -r .generator "$DIST/index.json")
  # every release with its asset names, in one paginated read
  existing=$(gh api --paginate "repos/$REPO/releases?per_page=100" \
    --jq '.[] | {tag: .tag_name, assets: [.assets[].name] | sort}' | jq -sc 'map({(.tag): .assets}) | add // {}')

  rm -rf "$UP" "$SUMS" "$GEN"; mkdir -p "$UP" "$SUMS"
  : > "$CI/published.txt"; : > "$UP/releases.tsv"
  echo "$commit" > "$UP/commit"
  published=0
  for rel in $(jq -r '.releases[].release' "$DIST/index.json"); do
    tag="opnsense-$rel"
    now=$(jq -S -c --arg r "$rel" '.releases[] | select(.release == $r)' "$DIST/index.json")
    before=$(jq -S -c --arg r "$rel" '.releases[] | select(.release == $r)' "$PREV")
    stage="$UP/$rel/assets"
    stage_release "$DIST/$rel" "$stage" "$SUMS/$rel/SHA256SUMS"
    cp "$SUMS/$rel/SHA256SUMS" "$stage/SHA256SUMS"
    action=create
    if have=$(jq -ce --arg t "$tag" '.[$t]' <<<"$existing"); then
      action=update
      if [ "$now" = "$before" ]; then
        # unchanged entry: republish only when the published files differ
        # from these (repairs a wrong SHA256SUMS or a left-over asset
        # without a person)
        listed=$(find "$stage" -maxdepth 1 -type f -printf '%f\n' | jq -Rsc 'split("\n") | map(select(. != "")) | sort')
        if [ "$have" = "$listed" ] && [ "$(gh release download "$tag" -R "$REPO" -p SHA256SUMS -O - 2>/dev/null)" = "$(cat "$SUMS/$rel/SHA256SUMS")" ]; then
          rm -rf "${SUMS:?}/$rel" "${UP:?}/$rel"
          continue
        fi
        echo "$tag: published files differ from dist, republishing"
      fi
    fi
    pre=$(jq -r .needs_verification <<<"$now")
    latest=false; [ "$rel" = "$newest" ] && [ "$pre" = false ] && latest=true
    notes "$rel" "$commit" > "$UP/$rel/notes.md"
    printf '%s\t%s\t%s\t%s\n' "$rel" "$action" "$pre" "$latest" >> "$UP/releases.tsv"
    echo "$rel" >> "$CI/published.txt"
    echo "plan: $action $tag (pre-release=$pre, $(find "$stage" -maxdepth 1 -type f | wc -l) assets)"
    published=$((published + 1))
  done
  echo "plan: $published release(s) to publish"

  # The generator version named in the outputs must exist as a tag: upload
  # creates v<version> on this commit if it does not exist. Never moved.
  local vtag="v$generator" new_tag=0
  if gh api "repos/$REPO/git/ref/tags/$vtag" >/dev/null 2>&1; then
    echo "plan: tag $vtag exists"
  elif [ "$published" -gt 0 ]; then
    echo "plan: create tag $vtag at ${commit:0:12}"
    new_tag=1
  fi
  printf 'vtag=%s\nnew_tag=%s\nsbom_release=0\n' "$vtag" "$new_tag" > "$UP/generator.env"

  # The generator's SBOM, made at the tag (a local tag on this commit, so the
  # SBOM names exactly the generator version); built and checked in every
  # run with SBOM=1, published as the release of a new tag.
  if [ "${SBOM:-}" = 1 ]; then
    mkdir -p "$GEN"
    have=$(git -C "$ROOT" rev-parse -q --verify "refs/tags/$vtag^{commit}" || true)
    if [ -z "$have" ]; then
      git -C "$ROOT" tag "$vtag" "$commit"
    elif [ "$have" != "$(git -C "$ROOT" rev-parse "$commit^{commit}")" ]; then
      die "the local tag $vtag points to ${have:0:12}, not ${commit:0:12}; run in a fresh checkout"
    fi
    "$ROOT/tools/ci/sbom.sh" "$GEN/opnsense-api-spec-$vtag.cdx.json"
    local v; v=$(jq -r '.metadata.component.version' "$GEN/opnsense-api-spec-$vtag.cdx.json")
    [ "$v" = "$vtag" ] || die "generator SBOM names $v, not $vtag"
    (cd "$GEN" && sha256sum -- *.cdx.json > SHA256SUMS)
    if [ "$new_tag" = 1 ]; then
      echo "plan: create release $vtag (generator SBOM)"
      sed -i 's/^sbom_release=0$/sbom_release=1/' "$UP/generator.env"
    else
      rm -rf "$GEN" # nothing to publish; keep the attestation step idle
    fi
  fi
}

# ------------------------------------------------------------ upload

upload() {
  local dry="${1:-}" commit rel action pre latest tag stage f n vtag new_tag sbom_release
  run() { if [ -n "$dry" ]; then echo "dry-run: $*"; else "$@"; fi; }
  [ -f "$UP/releases.tsv" ] && [ -f "$UP/commit" ] && [ -f "$UP/generator.env" ] || die "no plan in $UP (run: $0 plan)"
  commit=$(cat "$UP/commit")
  [[ "$commit" =~ $SHA_RE ]] || die "plan: bad commit '$commit'"
  [ -z "${GITHUB_SHA:-}" ] || [ "$commit" = "$GITHUB_SHA" ] || die "plan is for $commit, this run is $GITHUB_SHA"

  local published=0
  while IFS=$'\t' read -r rel action pre latest; do
    tag="opnsense-$rel"
    [[ "$tag" =~ $TAG_RE ]] || die "plan: bad release '$rel'"
    [[ "$action/$pre/$latest" =~ ^(create|update)/(true|false)/(true|false)$ ]] || die "plan: bad line for $rel"
    stage="$UP/$rel/assets"
    local assets=() names=()
    for f in "$stage"/*; do
      n="${f##*/}"
      [ -f "$f" ] && [ ! -L "$f" ] && [[ "$n" =~ $ASSET_RE ]] || die "plan: bad asset '$n' of $rel"
      assets+=("$f"); names+=("$n")
    done
    [ -f "$stage/SHA256SUMS" ] || die "plan: $rel has no SHA256SUMS"
    if [ "$action" = update ]; then
      echo "update $tag (pre-release=$pre)"
      run gh release edit "$tag" -R "$REPO" --title "OPNsense $rel" --notes-file "$UP/$rel/notes.md" --prerelease="$pre" --latest="$latest"
      run gh release upload "$tag" -R "$REPO" --clobber "${assets[@]}"
      # assets the plan does not list (renamed or dropped by a newer
      # generator) would stay public for ever otherwise
      local current extra
      current=$(gh api "repos/$REPO/releases/tags/$tag" --jq '.assets[].name')
      for extra in $(comm -23 <(sort <<<"$current" | sed '/^$/d') <(printf '%s\n' "${names[@]}" | sort)); do
        echo "$tag: deleting asset $extra (not part of this release any more)"
        run gh release delete-asset "$tag" "$extra" -R "$REPO" --yes
      done
    else
      echo "create $tag (pre-release=$pre)"
      run gh release create "$tag" -R "$REPO" --target "$commit" --title "OPNsense $rel" --notes-file "$UP/$rel/notes.md" \
        --prerelease="$pre" --latest="$latest" "${assets[@]}"
    fi
    published=$((published + 1))
  done < "$UP/releases.tsv"
  echo "published $published release(s)"

  vtag=$(sed -n 's/^vtag=//p' "$UP/generator.env")
  new_tag=$(sed -n 's/^new_tag=//p' "$UP/generator.env")
  sbom_release=$(sed -n 's/^sbom_release=//p' "$UP/generator.env")
  [[ "$vtag" =~ $VTAG_RE ]] || die "plan: bad generator tag '$vtag'"
  if [ "$new_tag" = 1 ]; then
    if gh api "repos/$REPO/git/ref/tags/$vtag" >/dev/null 2>&1; then
      echo "tag $vtag exists"
    else
      echo "create tag $vtag at ${commit:0:12}"
      run gh api "repos/$REPO/git/refs" -f ref="refs/tags/$vtag" -f sha="$commit"
    fi
  fi
  if [ "$sbom_release" = 1 ]; then
    local repo_url="${GITHUB_SERVER_URL:-https://github.com}/$REPO" sbom="$GEN/opnsense-api-spec-$vtag.cdx.json"
    [ -f "$sbom" ] && [ -f "$GEN/SHA256SUMS" ] || die "plan: generator SBOM missing"
    echo "create release $vtag (generator SBOM)"
    run gh release create "$vtag" -R "$REPO" --verify-tag --prerelease --latest=false --title "Generator $vtag" \
      --notes "Generator version $vtag of opnsense-api-spec (commit [\`${commit:0:12}\`]($repo_url/commit/$commit)). Attached: its software bill of materials (CycloneDX 1.6) and SHA256SUMS, with a provenance attestation. The API descriptions are the releases opnsense-<OPNsense release>." \
      "$sbom" "$GEN/SHA256SUMS"
  fi
}

case "${1:-}" in
  plan) plan ;;
  upload) upload "$([ "${2:-}" = "--dry-run" ] && echo 1)" ;;
  *) echo "usage: $0 plan | upload [--dry-run]" >&2; exit 2 ;;
esac
