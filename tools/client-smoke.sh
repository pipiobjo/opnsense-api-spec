#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Consumer smoke test (ADR 0007): generate a Go client from a release spec
# with a pinned oapi-codegen, compile it together with the provider-style
# scenario, and optionally run the scenario against the throwaway test VM.
#
#   tools/client-smoke.sh 26.7          generate + vet + build (blocking gate)
#   tools/client-smoke.sh 26.7 --run    ... and run the scenario against the
#                                       VM (tools/testvm.sh up 26.7 && apikey)
#
# Runs in a rootless Podman container; only the repository is mounted. The
# generated client (test/consumer-go/opnsense/) is not committed.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RELEASE="${1:?release, e.g. 26.7}"
RUN="${2:-}"
OAPI_CODEGEN="github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.8.2"
GO_IMAGE="docker.io/library/golang:1.27.4-bookworm@sha256:fb7cceb528b47141eccd9b60bf3cbd27d5ef5f8cf5b7d338bec866da495b50dc" # pinned by digest; Go security fixes need a new pin (Dependabot does not see it)
SPEC="dist/$RELEASE/opnsense-$RELEASE.openapi.json"
URL="https://127.0.0.1:${PORT:-18443}"

[ -f "$ROOT/$SPEC" ] || { echo "error: $SPEC missing; build the release first" >&2; exit 1; }

net=slirp4netns # module downloads only
env=()
if [ "$RUN" = "--run" ]; then
  [ -f "$ROOT/.cache/vm/apikey.json" ] || { echo "error: no test VM API key; run tools/testvm.sh up $RELEASE && tools/testvm.sh apikey" >&2; exit 1; }
  net=host # to reach the VM's port forward on 127.0.0.1 (the scenario refuses anything else)
  env=(-e "OPN_URL=$URL" -e "OPN_KEY=$(jq -r .key "$ROOT/.cache/vm/apikey.json")" -e "OPN_SECRET=$(jq -r .secret "$ROOT/.cache/vm/apikey.json")")
fi

podman run --rm --userns=keep-id --cap-drop=all --security-opt=no-new-privileges --network="$net" \
  -v "$ROOT:/work:Z" -v opnsense-api-spec-gomod:/gomod:U -w /work/test/consumer-go \
  -e HOME=/tmp -e GOPATH=/tmp/go -e GOMODCACHE=/gomod -e GOCACHE=/tmp/gocache -e GOFLAGS=-mod=mod \
  "${env[@]}" "$GO_IMAGE" sh -euc "
    mkdir -p opnsense
    go run $OAPI_CODEGEN -config oapi-codegen.yaml /work/$SPEC
    go mod tidy >/dev/null
    go vet ./...
    echo \"client: \$(grep -c '^func (c \\*Client)' opnsense/client.gen.go) methods, scenario builds\"
    if [ '$RUN' = '--run' ]; then go run ./scenario; fi
  "
