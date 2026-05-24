# dbfork testdata

Fixtures for the dbfork equivalence test (Task #152).

## nile-fixture/

A real java-tron Nile testnet DB snapshot. **Not checked into git** — it's
~10-30 GB depending on snapshot kind, and the equivalence test fetches
it on-demand via a fixed upstream URL pinned by SHA.

### Why a real DB, not a synthetic one

The whole point of Task #152 is to prove byte-for-byte equivalence
between the Go DbFork port and `tron-docker/tools/toolkit`'s Java
DbFork against **realistic on-disk state** — millions of accounts with
the full proto field surface (votes, frozen balances, permissions,
asset maps), real witness rotations, real TRC20 contracts with
non-trivial storage layouts. A synthetic fixture would let a regression
in any of those slip through.

### How to (re)generate

```bash
# Reproducible (CI / release gate): pin to a specific upstream backup.
NILE_BACKUP=backup20260520 ./scripts/build-nile-fixture.sh

# Ad-hoc local testing only: use whatever's latest on the mirror.
./scripts/build-nile-fixture.sh
```

The script wraps `trond snapshot download --network nile --type lite`,
then walks each of the 8 dbfork-relevant store dirs and computes a
deterministic SHA256 over the sorted file list. Output:

- `nile-fixture/database/{account,witness,...}` — actual store dirs
  (gitignored).
- `nile-fixture-meta.json` — manifest with block height, backup ID,
  per-store SHAs, generation timestamp.

Re-running with the same `NILE_BACKUP` env var and an existing fixture
is a no-op for the download step and just refreshes the manifest. Pass
`FORCE=true` to wipe and re-download.

### How Task #152 consumes the fixture

The equivalence test reads `nile-fixture-meta.json`, looks for
`nile-fixture/database/` at the expected path, verifies each store's
SHA256 matches the manifest, then:

1. Copies the fixture to a scratch dir (mutations must not modify
   the cached snapshot).
2. Runs the Go `MutateApply` against the scratch copy with a
   canonical `fork.conf`.
3. Runs Java DbFork against an independent copy of the same scratch
   data with the same `fork.conf`.
4. Diffs the two resulting DB states byte-for-byte (proto-aware for
   account/contract stores; raw bytes for storage-row).

If the fixture is missing, the equivalence test SKIPs with an
instruction to run `scripts/build-nile-fixture.sh`. This keeps `go
test ./...` fast on machines that haven't generated the fixture yet.

### Storage convention (proposal)

The intent is to host the canonical fixture as a GitHub Release
artifact (or S3 bucket) keyed by `NILE_BACKUP` value, so CI can fetch
without re-running a full snapshot download. The manifest's
`upstream_backup` field is the lookup key. **This URL hosting is not
set up yet — Phase 1 PoC ships with the workflow only; the upload
step lands when the equivalence test (#152) is wired into CI.**

### Why `lite` not `full`

The dbfork mutations only touch 8 stores (witness, witness_schedule,
account, account-asset, asset-issue-v2, properties, contract,
storage-row). The lite snapshot includes all of these. `full` adds
the historical block + transaction stores, ~10x the size, with no
additional coverage for equivalence purposes.
