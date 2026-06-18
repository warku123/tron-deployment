# TODOS

Deferred work captured during reviews. Each item carries enough context to pick
up cold.

## `trond snapshot clone --from-node <name>` (resolve a node's DB dir)

- **What:** Add a `--from-node <name>` source to `snapshot clone` that resolves a
  managed node's chain-DB directory from state, for BOTH docker and jar runtimes,
  and refuses to clone unless the node is stopped.
- **Why:** Lets an agent fork a live rig's DB by node name instead of hand-typing
  the on-disk path.
- **Cons / why it was deferred from B3:** the existing `destFromNode`
  (`cmd/snapshot/download.go`) is jar-only (rejects docker) and returns
  `install_path`, NOT the chain-DB dir — so it can't be reused as-is. Docker
  storage paths live in render logic, not `state.json`, so this needs a new
  runtime-aware DB-path resolver. And `fsclone.CloneDir`'s contract requires a
  *quiescent* source, so `--from-node` must hard-refuse a running node (or stop
  it) to avoid a corrupt point-in-time clone.
- **Context:** Deferred during the 2026-06-17 `/plan-eng-review` of B3 (the
  outside-voice/Codex caught that the originally-planned `--from-node` was
  jar-only + unsafe-on-running). B3 shipped positional-paths-only
  (`clone <src> <dst>`); this is the by-node convenience layer on top.
- **Depends on / blocked by:** B3 (`snapshot clone`) landing first; a
  runtime-aware DB-path resolver (docker storage path is not in state today).

## Reconcile `scripts/db_cp.sh` with `trond snapshot clone`

- **What:** There are now TWO "fast local chain-DB copy" mechanisms: the
  Go `trond snapshot clone` verb (copy-on-write via APFS clonefile / Linux
  FICLONE, byte-copy fallback) added in #192, and `scripts/db_cp.sh`
  (rsync for metadata + hard-link for `.sst`) added in #194 (bladehan1).
  Pick one story and converge.
- **Why:** two overlapping capabilities drift and confuse — an agent/operator
  shouldn't have to guess which to use for a warm-pool fixture.
- **Options:** (a) teach `snapshot clone` a hard-link tier (between CoW and
  full byte copy) so `.sst` files share inodes when CoW is unavailable, then
  retire `db_cp.sh`; or (b) have `db_cp.sh` shell out to `trond snapshot
  clone`; or (c) explicitly document them as distinct (CoW clone vs
  hard-link backup) if the use cases really differ.
- **Context:** flagged 2026-06-17 after #194 merged. `internal/fsclone`
  owns the CoW primitive; `db_cp.sh` is a standalone shell script referenced
  in `knowledge/snapshots.md`. Hard-links and CoW have different semantics
  (hard-link shares the inode → a later in-place write to one copy corrupts
  the other; CoW is independent), so reconciling needs care, not a blind
  merge.
- **Depends on / blocked by:** nothing; both already merged.

## Emit a flag-aware `chain_id` in `status` (TVM CHAINID match)

- **What:** `status -o json` now emits `genesis_block_id` (the block-0 TRON
  block id — see `internal/apply/probe.go`). The original ask also wanted a
  `chain_id` that matches what on-chain contracts see via the TVM `CHAINID`
  opcode. That was deferred because the derivation is **flag-dependent**.
- **The verified semantics (don't re-derive wrong):** java-tron
  `Program.getChainId()` starts from `getBlockByNum(0).getBlockId()` and
  returns the **full 32-byte block id**, truncating to the **last 4 bytes
  ONLY** when `allowTvmCompatibleEvm` OR `allowOptimizedReturnValueOfChainId`
  is active. Both are off by default on a fresh private net. So a naive
  "last 4 bytes" `chain_id` is wrong for the default rig.
- **How to do it right:** probe `getchainparameters` for those two flags,
  then derive `chain_id` = full `genesis_block_id` (flags off) or its last 4
  bytes (flags on). Ideally verify once against a deployed contract reading
  CHAINID. Surface in `status`; bump SchemaVersion (additive → PATCH).
- **Cons / why deferred:** marginal value (TRON tx signing uses
  `ref_block_hash`, not a chain id; `genesis_block_id` already distinguishes
  rigs), plus the flag-probe + verification is more than a one-field add.
- **Context:** deferred during the 2026-06-18 `/plan-eng-review`; the
  outside-voice (Codex, web-verified) caught the flag-dependency that would
  have shipped a wrong `chain_id`. `genesis_block_id` shipped as the
  unambiguous anchor.

## Rename `network-create`'s `network` output field

- **What:** `network create -o json` returns `network` = the network's intent
  NAME, while `apply`/`status`/`list`/`inspect` return `network` = the chain
  kind (`mainnet|nile|private`, matching the intent's `network:` field). Rename
  network-create's field (e.g. → `name` / `network_name`) so `network` means
  one thing across the CLI.
- **Why:** an agent that learned `network` from one command misreads it in
  another. C1 widened the collision by adding `network`=chain-kind to
  apply/status.
- **Cons:** breaking change to network-create.schema.json — needs a major
  schema bump + agent migration, so it can't ride along in a feature PR.
- **Context:** flagged by the 2026-06 eng-review of the C1 private-net PR
  (Issue 4 / Codex #7). Documented in AGENTS.md's safety section meanwhile.
- **Depends on:** a deliberate schema-version major bump.
