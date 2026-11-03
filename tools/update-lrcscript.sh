#!/usr/bin/env bash
# Vendor a reviewed lrcscript commit without copying binaries or build caches.
set -euo pipefail
cd -- "$(dirname -- "$0")/.."
source_repo="${1:-../lrcscript}"
if [[ -n "$(git -C "$source_repo" status --porcelain)" ]]; then
  echo 'Commit lrcscript changes before updating the vendored copy.' >&2
  exit 1
fi
reference="$(git -C "$source_repo" rev-parse HEAD)"
destination=third_party/lrcscript
mkdir -p "$destination/engine" "$destination/licenses"
cp "$source_repo/lrcscript.py" "$source_repo/LICENSE" "$source_repo/NOTICE" "$destination/"
cp "$source_repo/engine/Cargo.toml" "$source_repo/engine/Cargo.lock" "$destination/engine/"
# Removing just the previously vendored sources prevents retaining deleted files.
rm -rf -- "$destination/engine/src"
cp -R "$source_repo/engine/src" "$destination/engine/src"
cp "$source_repo"/licenses/* "$destination/licenses/"
printf '%s\n' "$reference" > "$destination/REVISION"
