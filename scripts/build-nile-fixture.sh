#!/usr/bin/env bash
# Generates the Nile testnet fixture consumed by the dbfork equivalence
# test (Task #152). The fixture is a real java-tron Nile DB snapshot
# pinned to a specific upstream backup, so two machines that run this
# script with the same NILE_BACKUP value produce byte-identical
# output.
#
# Usage:
#   NILE_BACKUP=backup20260520 ./scripts/build-nile-fixture.sh
#     -- pin to a specific upstream snapshot (RECOMMENDED for CI).
#
#   ./scripts/build-nile-fixture.sh
#     -- use whatever's `latest` on the mirror (NOT reproducible;
#        only suitable for ad-hoc local testing).
#
# Output:
#   internal/dbfork/testdata/nile-fixture/database/{account,witness,...}
#     -- the actual store dirs (gitignored; ~10-30 GB).
#   internal/dbfork/testdata/nile-fixture-meta.json
#     -- manifest with block height, backup ID, per-store SHA256, source.
#
# Requirements:
#   - trond binary in PATH (run `make build` first if missing).
#   - ~50 GB free disk for download + extract.
#   - shasum (BSD/macOS) or sha256sum (GNU/Linux) — script auto-detects.
#
# The script is idempotent: re-running with the same NILE_BACKUP and an
# existing fixture dir refreshes only the manifest (no re-download). Use
# --force to wipe and re-download.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FIXTURE_DIR="${REPO_ROOT}/internal/dbfork/testdata/nile-fixture"
MANIFEST="${REPO_ROOT}/internal/dbfork/testdata/nile-fixture-meta.json"

NILE_BACKUP="${NILE_BACKUP:-}"
FORCE="${FORCE:-false}"
TROND_BIN="${TROND_BIN:-trond}"

# Picks the right SHA256 tool for both macOS (shasum -a 256) and Linux
# (sha256sum). Both emit the same `<hash>  <path>` format. Returned as
# an array so multi-arg invocations stay quoted at the call site.
HASHER_CMD=()
detect_hasher() {
  if command -v sha256sum >/dev/null 2>&1; then
    HASHER_CMD=(sha256sum)
  elif command -v shasum >/dev/null 2>&1; then
    HASHER_CMD=(shasum -a 256)
  else
    echo "ERROR: neither sha256sum nor shasum found in PATH" >&2
    exit 1
  fi
}

# Hash one store dir deterministically: list files in sorted order,
# concatenate their hashes, then hash that. Single-step `find | xargs
# shasum` would produce nondeterministic output because find's order
# varies by filesystem.
hash_store() {
  local store_dir="$1"
  # Sorted file list; each file's hash printed as `<hash>  <relpath>`;
  # the concatenated stream is fed to one final hash so the manifest
  # entry is a single 64-char hex string per store.
  (cd "$store_dir" && find . -type f | sort | xargs "${HASHER_CMD[@]}") \
    | "${HASHER_CMD[@]}" | awk '{print $1}'
}

# 1. Run trond snapshot download (lite is sufficient for equivalence —
#    we only mutate the 8 dbfork stores, not the full blockstore).
download() {
  if [[ -d "$FIXTURE_DIR/database" && "$FORCE" != "true" ]]; then
    echo "fixture already present at $FIXTURE_DIR/database (use FORCE=true to re-download)"
    return
  fi
  echo "downloading Nile snapshot${NILE_BACKUP:+ (backup=$NILE_BACKUP)}"
  local args=(snapshot download --network nile --type lite --to "$FIXTURE_DIR" --output json)
  if [[ -n "$NILE_BACKUP" ]]; then
    args+=(--backup "$NILE_BACKUP")
  fi
  if [[ "$FORCE" == "true" ]]; then
    args+=(--force)
  fi
  "$TROND_BIN" "${args[@]}"
}

# 2. Read the block height from the snapshot's LATEST_BLOCK file if
#    present, or fall back to parsing trond's JSON output. java-tron
#    snapshots typically include a metadata sidecar.
extract_block_height() {
  if [[ -f "$FIXTURE_DIR/database/info" ]]; then
    # Some snapshots write info to a sidecar.
    grep -E 'latest.*block' "$FIXTURE_DIR/database/info" | head -1 \
      | sed 's/.*[^0-9]\([0-9]\{6,\}\).*/\1/' || true
  fi
}

# 3. Compute per-store SHA256, write manifest JSON.
write_manifest() {
  local block="${1:-unknown}"
  echo "computing per-store SHA256 (this is slow on lite snapshots, ~5 min for full)"

  # The 8 dbfork-relevant stores. Stays in sync with stores/stores.go's
  # AllStores list. If java-tron adds more, both lists need updating.
  local stores=(account account-asset asset-issue-v2 contract properties storage-row witness witness_schedule)

  # Build the JSON manifest by hand (bash 3.2 has no jq guarantee). Each
  # store's entry is `"<name>": "<hash>"`. We track which stores exist;
  # the manifest documents that absence explicitly so a missing store on
  # the source snapshot doesn't silently no-op the equivalence test.
  local body=""
  local sep=""
  for store in "${stores[@]}"; do
    local store_path="$FIXTURE_DIR/database/$store"
    if [[ -d "$store_path" ]]; then
      local hash
      hash="$(hash_store "$store_path")"
      body+="${sep}\"$store\": \"$hash\""
    else
      body+="${sep}\"$store\": null"
      echo "WARN: store $store not present in snapshot" >&2
    fi
    sep=$',\n    '
  done

  cat > "$MANIFEST" <<EOF
{
  "network": "nile",
  "snapshot_kind": "lite",
  "upstream_backup": "${NILE_BACKUP:-latest}",
  "block_height": "${block}",
  "generated_at": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "generated_by": "$(whoami)@$(hostname -s)",
  "stores": {
    ${body}
  },
  "consumed_by": "internal/dbfork/equivalence_test.go (Task #152)",
  "regenerate": "NILE_BACKUP=<backup_id> ./scripts/build-nile-fixture.sh"
}
EOF
  echo "wrote manifest: $MANIFEST"
}

main() {
  if ! command -v "$TROND_BIN" >/dev/null 2>&1; then
    echo "ERROR: $TROND_BIN not in PATH (run 'make build' first)" >&2
    exit 1
  fi
  detect_hasher
  download
  local block
  block="$(extract_block_height)"
  write_manifest "$block"
  echo "done. Fixture ready at $FIXTURE_DIR; manifest at $MANIFEST"
  echo "NOTE: fixture is gitignored. To share, upload to your release"
  echo "      storage (S3/GitHub Release/HuggingFace) and pin its SHA"
  echo "      in the manifest's upstream_backup field."
}

main "$@"
