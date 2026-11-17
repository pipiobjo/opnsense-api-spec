#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# The code gates, the same in ci.yml and in the pipeline's build job, and
# the command to run before every push:
#
#   tools/ci/gates.sh
#
# Formatting, vet, all Go tests with the real golden releases (fetched,
# GOLDEN_REQUIRED=1 so a missing input fails instead of skipping), and the
# script tests.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
tools/ci/golden-fetch.sh
unformatted=$(gofmt -l cmd internal)
[ -z "$unformatted" ] || { echo "$unformatted"; echo "::error::gofmt"; exit 1; }
go vet ./...
GOLDEN_REQUIRED=1 go test -count=1 ./...
tools/ci/alerts_test.sh
