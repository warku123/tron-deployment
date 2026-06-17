#!/usr/bin/env bash
# db_cp <src> <dst>
#
# Copy a java-tron LevelDB/RocksDB snapshot directory efficiently:
#   - Non-.sst files (LOCK, CURRENT, MANIFEST, OPTIONS, …) are copied normally
#     so each destination gets an independent inode.
#   - .sst files are hard-linked (same data blocks, zero extra disk usage).
#
# Usage as a one-liner:
#   db_cp /data/tron/database /data/tron/database-backup-$(date +%Y%m%d-%H%M%S)
#
# Or source this file and call db_cp directly:
#   source scripts/db_cp.sh
#   db_cp /src /dst

db_cp() {
  local SRC="$1"
  local DST="$2"

  if [ -z "$SRC" ] || [ -z "$DST" ]; then
    echo "Usage: db_cp <src> <dst>" >&2
    return 1
  fi
  if [ ! -d "$SRC" ]; then
    echo "src $SRC does not exist or is not a directory" >&2
    return 1
  fi
  if [ -e "$DST" ]; then
    echo "dst $DST already exists, delete it first." >&2
    return 1
  fi

  # Step 1: copy directory structure + non-.sst files (independent inodes)
  rsync -a --exclude="*.sst" "$SRC/" "$DST/"

  # Step 2: hard-link .sst files (no data blocks copied)
  find "$SRC" -name "*.sst" -type f | while IFS= read -r f; do
    local rel="${f#${SRC}/}"
    mkdir -p "$DST/$(dirname "$rel")"
    ln "$f" "$DST/$rel"
  done

  echo "db_cp done: $SRC -> $DST"
}

# Run directly if not being sourced
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  db_cp "$@"
fi
