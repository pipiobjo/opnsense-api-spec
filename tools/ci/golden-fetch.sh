#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Fetch and unpack the releases the golden files describe, so the golden and
# determinism tests have their inputs (run them with GOLDEN_REQUIRED=1).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
for f in internal/assemble/testdata/golden/*.json; do
  rel=$(basename "$f" .json)
  go run ./cmd/opnsense-api-spec fetch -release "$rel" -plugins
done
