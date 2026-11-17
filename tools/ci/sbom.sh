#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Software bill of materials (CycloneDX 1.6, JSON) of this Go module, with
# the official CycloneDX generator for Go (the same script as
# tools/ci/sbom.sh in opnsense-api-go):
#
#   tools/ci/sbom.sh OUT [cyclonedx-gomod arguments]
#                    default arguments: app -main cmd/opnsense-api-spec
#                    (the generator as built, with its dependencies)
#
# The generator is a third-party program, so it runs isolated: rootless
# Podman, no capabilities, no new privileges, the repository mounted
# read-only, network only for module downloads (checked against the Go
# checksum database). Tool version and image are pinned below. The result
# is checked before it is used: CycloneDX 1.6, the main component is this
# module, and the Go standard library is listed with its version
# (vulnerability scanners match Go releases through it).
set -euo pipefail

OUT="${1:?usage: sbom.sh OUT [cyclonedx-gomod arguments]}"
shift
ARGS=("$@")
[ ${#ARGS[@]} -gt 0 ] || ARGS=(app -main cmd/opnsense-api-spec)
GOMOD_TOOL="github.com/CycloneDX/cyclonedx-gomod/cmd/cyclonedx-gomod@v1.12.0"
GO_IMAGE="docker.io/library/golang:1.27.4-bookworm@sha256:fb7cceb528b47141eccd9b60bf3cbd27d5ef5f8cf5b7d338bec866da495b50dc"

ROOT="$(git rev-parse --show-toplevel)"
MODULE=$(cd "$ROOT" && go list -m)
mkdir -p "$(dirname "$OUT")"
outdir=$(cd "$(dirname "$OUT")" && pwd)
name=$(basename "$OUT")

# the repository's .git must be inside the mount for the version
[ -d "$ROOT/.git" ] || { echo "error: $ROOT/.git is not a directory (a worktree?); the module version comes from git" >&2; exit 1; }

podman run --rm --userns=keep-id --cap-drop=all --security-opt=no-new-privileges --network=slirp4netns \
  -v "$ROOT:/work:ro,Z" -v "$outdir:/out:Z" -v sbom-gomod:/gomod:U -w /work \
  -e HOME=/tmp -e GOPATH=/tmp/go -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e GOFLAGS=-mod=mod \
  -e GIT_CONFIG_COUNT=1 -e GIT_CONFIG_KEY_0=safe.directory -e GIT_CONFIG_VALUE_0=/work \
  "$GO_IMAGE" go run "$GOMOD_TOOL" "${ARGS[@]}" -json -std -licenses -output "/out/$name" .

jq -e --arg m "$MODULE" '
  .bomFormat == "CycloneDX" and .specVersion == "1.6"
  and .metadata.component.name == $m
  and ([.components[]? | select(.name == "std") | .version] | length == 1)
' "$OUT" >/dev/null || { echo "error: $OUT is not a CycloneDX 1.6 SBOM of $m with the Go standard library" >&2; exit 1; }
echo "SBOM $OUT: $MODULE $(jq -r '.metadata.component.version // "(no version)"' "$OUT"), $(jq '[.components[]?] | length' "$OUT") components ($(jq -r '[.components[]? | "\(.name) \(.version)"] | join(", ")' "$OUT"))"
