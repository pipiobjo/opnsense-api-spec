#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# The code gates, the same in ci.yml and in the pipeline's build job, and
# the command to run before every push:
#
#   tools/ci/gates.sh
#
# Formatting, vet, all Go tests with the real golden releases (fetched,
# GOLDEN_REQUIRED=1 so a missing input fails instead of skipping; any skip
# not on tools/ci/skip-allowlist.txt fails, tools/ci/skips.sh), and the
# script tests. Coverage of the Go tests per package, with the floor in
# tools/ci/coverage-floor.txt (report in coverage/).
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
tools/ci/golden-fetch.sh
unformatted=$(gofmt -l cmd internal)
[ -z "$unformatted" ] || { echo "$unformatted"; echo "::error::gofmt"; exit 1; }
go vet ./...
mkdir -p .cache/ci
# -v for the skip check: a skipped test proves nothing and is invisible in
# the short output (tools/ci/skips.sh, allowlist empty)
GOLDEN_REQUIRED=1 go test -count=1 -v -coverpkg=./... -coverprofile=.cache/ci/unit.out ./... > .cache/ci/unit.log 2>&1 \
  || { grep -E '^(\s*--- FAIL|FAIL|panic|\s+\S+_test\.go:[0-9]+:)' .cache/ci/unit.log; echo "::error::go test failed (log: .cache/ci/unit.log)"; exit 1; }
grep -E '^(ok|FAIL)\s' .cache/ci/unit.log
tools/ci/skips.sh .cache/ci/unit.log
tools/ci/coverage.sh unit .cache/ci/unit.out coverage
# The script tests run with the switches of a live run set, so a test that
# lets the job environment leak into a case fails here (promote, pull
# requests) and not first in the scheduled run (2026-11-25: LIVE=true
# turned the dry-run case of state_test.sh into a live one).
for t in alerts testvm parallel publish state catalog pins; do
  env LIVE=true STATE_ACCEPT_UNATTESTED=1 STATE_ALLOW_FEWER_VERIFIED=1 \
    CATALOG_LIVE=true "tools/ci/${t}_test.sh"
done
