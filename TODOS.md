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

## Emit `chain_id` in `status` / `inspect` -o json

- **What:** The ai-ops A1 ask lists `chain_id` in the per-node `status
  --json` shape. The 2026-06 A1 increment shipped `healthy`,
  `container_id`, and the `logs` locator but deferred `chain_id`.
- **Why deferred:** TRON has no single cheap RPC that returns a tidy chain
  id. It's derived from the genesis block (the genesis block ID / its
  trailing bytes), so producing it means a `/wallet/getblockbynum?num=0`
  probe + a documented derivation — more than the additive-field A1 scope.
  The operational A1 definition ("expose rpc_endpoint + node-log paths")
  does not require it; tron-toolkit/mcp-logs are unblocked without it.
- **How to add:** in `internal/apply` add a best-effort probe that fetches
  block 0 and derives the chain id (match java-tron's
  `Wallet.getChainId()` / the genesis block-id convention), surface it in
  `LiveStatus` or a sibling, wire into status/inspect + schemas, bump
  SchemaVersion (additive → MINOR per the C1/A1 precedent).
- **Context:** flagged while implementing A1 (2026-06). For a private net
  the value is deterministic from the genesis the intent pins, so it
  doubles as a B1 "echo the resolved rig identity" signal.

## `--require-private` on the multi-node mutators (chaos + network ops)

- **What:** Extend the `--require-private` / `TROND_REQUIRE_PRIVATE` gate to
  the chaos primitives (`disconnect` / `partition` / `connect` / `heal`) and
  the network ops (`network add` / `destroy` / `upgrade`).
- **Why:** These mutate a rig too, so an airtight "an agent can't touch a
  non-private net" boundary needs them. The per-node mutators
  (start/stop/restart/remove/rollback/upgrade) + apply + network-create are
  already gated via `internal/guard`.
- **Cons / why deferred:** they operate on MULTIPLE nodes or a whole network,
  so it isn't the per-node one-liner — it needs an "are ALL involved nodes
  private?" check (chaos spans 2+ nodes; `network destroy`/`add` span the
  network's node set). Different guard shape from the single-node path.
- **Context:** deferred during the 2026-06-17 `/plan-eng-review` of the
  per-node `--require-private` rollout. The shared predicate + error live in
  `internal/guard` (`Enforce`/`EnforceArg`/`Requested`); a multi-node guard
  would iterate the involved nodes and call `guard.Enforce` per node.
- **Depends on / blocked by:** the per-node rollout landing (this PR).

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
