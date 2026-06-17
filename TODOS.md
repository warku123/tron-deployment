# TODOS

Deferred work captured during reviews. Each item carries enough context to pick
up cold.

## Public `trond snapshot clone` verb

- **What:** Expose the `internal/fsclone` copy-on-write clone primitive as a
  user-facing `trond snapshot clone <src> <dst>` CLI verb.
- **Why:** Lets agents / operators build a warm pool of fixtures interactively —
  clone a cached, pinned snapshot to a fresh isolated dir in seconds instead of
  re-downloading or re-copying 30–90 GB.
- **Pros:** Completes the warm-pool story as a first-class capability; useful for
  the "a new agent just uses trond" workflow.
- **Cons:** Speculative public API with only one consumer today (the equivalence
  test). Adds CLI surface, docs, and a test matrix for a capability nobody is
  asking for interactively yet.
- **Context:** Deferred during the 2026-06-13 `/plan-eng-review` (Issue 1). The
  reusable primitive lives in `internal/fsclone` (`x/sys/unix` clonefile/FICLONE
  with byte-copy fallback). The outside-voice (Codex) Net agreed: don't broaden
  to a public verb until a second real consumer exists AND CI proves CoW is
  available where it matters.
- **Depends on / blocked by:** `internal/fsclone` primitive landing (this PR);
  a concrete second consumer (e.g. an agent warm-pool flow or `apply --snapshot`).

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
