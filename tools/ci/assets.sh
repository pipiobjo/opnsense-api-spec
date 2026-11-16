#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 opnsense-api-spec contributors
# SPDX-License-Identifier: BSD-2-Clause
# Shared by publish.sh and roundtrip.sh (sourced): how the files of one
# release become GitHub release assets.

# asset_name NAME: the name GitHub stores an uploaded asset under:
# characters outside [A-Za-z0-9._+-] become ".", runs of dots collapse
# ("26.7.8..26.7.9.json" is stored as "26.7.8.26.7.9.json"), a leading dot
# is dropped.
asset_name() { sed -E 's/[^A-Za-z0-9._+-]/./g; s/\.{2,}/./g; s/^\.//' <<<"$1"; }

# stage_release SRC STAGE SUMS: link the files of SRC (one release
# directory, no subdirectories) into STAGE under their GitHub names and
# write SHA256SUMS over those names to SUMS. Fails when two files map to
# one name.
stage_release() {
  local src="$1" stage="$2" sums="$3" a n
  rm -rf "$stage"; mkdir -p "$stage" "$(dirname "$sums")"
  local files
  mapfile -t files < <(find "$src" -maxdepth 1 -type f | sort)
  [ "${#files[@]}" -gt 0 ] || { echo "error: no files in $src" >&2; return 1; }
  for a in "${files[@]}"; do
    n=$(asset_name "${a##*/}")
    [ ! -e "$stage/$n" ] || { echo "error: two assets of $src map to the name $n" >&2; return 1; }
    ln "$a" "$stage/$n" 2>/dev/null || cp "$a" "$stage/$n"
  done
  mapfile -t files < <(find "$stage" -maxdepth 1 -type f | sort)
  (cd "$stage" && sha256sum -- "${files[@]##*/}") > "$sums"
}
