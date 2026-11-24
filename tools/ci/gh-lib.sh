#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Shared GitHub reads of the CI scripts (sourced, not run):
#
#   release_status REPO TAG   prints the HTTP status of the release TAG:
#                             200 exists, 404 does not; anything else
#                             (5xx, rate limit, no network: 000) is a read
#                             failure the caller must not take for "absent"
#
# "gh release view" exits 1 for a missing release and for every transient
# fault alike; taking a fault for "no release" once threw away the whole
# verification history (finding REL-2), so absence is decided by the status
# code only.

release_status() {
  local out
  out=$(gh api -i "repos/$1/releases/tags/$2" 2>/dev/null | head -1) || true
  out=$(awk '{print $2}' <<<"$out")
  [[ "$out" =~ ^[0-9]{3}$ ]] || out=000
  echo "$out"
}
