#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Unpack the hand-over archive of the prepare job (publish-input.tar) for
# the jobs that hold write rights (upload, roundtrip; ADR 0015):
#
#   tools/ci/unpack-input.sh ARCHIVE
#
# prepare runs the build and every Go dependency, so its archive is
# untrusted input. Only plain files and directories below the hand-over
# paths are accepted; anything else (a path outside .cache/ci, "..", an
# absolute path, a link, a device) refuses the whole archive. Otherwise a
# member such as tools/ci/publish.sh would replace the script this job runs
# next with its token.
set -euo pipefail

ARCHIVE="${1:?usage: unpack-input.sh ARCHIVE}"
ALLOWED='^\.cache/ci/(upload|sums|state|catalog|generator)(/.*)?$|^\.cache/ci/published\.txt$|^\.cache/ci/?$|^\.cache/?$'

bad=0
while IFS= read -r line; do
  type="${line:0:1}"
  # GNU tar -tv: "<mode> <owner> <size> <date> <time> <name>"
  name=$(sed -E 's/^([^ ]+ +){5}//' <<<"$line")
  case "$type" in
    -|d) ;;
    *) echo "error: publish-input: '$name' is not a plain file or directory" >&2; bad=1; continue ;;
  esac
  if [[ "$name" == /* ]] || [[ "/$name/" == */../* ]] || ! grep -qE "$ALLOWED" <<<"$name"; then
    echo "error: publish-input: '$name' is outside the hand-over paths" >&2; bad=1
  fi
done < <(tar -tvf "$ARCHIVE" --quoting-style=literal --full-time)
[ "$bad" = 0 ] || exit 1
tar -xf "$ARCHIVE" --no-same-owner --no-same-permissions
