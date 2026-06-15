# replay

Streams mainnet historical transactions from TronGrid HTTP API to a private chain. Pure-Go external HTTP client — no java-tron source dependency.

Useful for:
- Stress harness with real mainnet traffic
- Snapshot validation (replay TXs after a `db fork` snapshot to verify the cloned state)

## Install

```bash
# From the tron-deployment repo root
make build-replay        # → bin/replay
make install-replay      # → $(GOBIN)/replay
```

Requires Go 1.21+. No external dependencies (stdlib only). Produces an ~8 MB single binary you can `scp` to any target host.

The codebase is split by module: `main.go` / `state.go` / `trongrid.go` / `private.go` / `filter.go` / `logger.go` / `replayer.go`. `go build .` compiles the entire package.

---

## Prerequisites

1. **Private chain node is running** — built from a `relay_skip_signature`-style branch (see below); at least one SR producing blocks.
2. **Private chain has mainnet state imported** — typically via `Toolkit.jar db fork` to trim a mainnet snapshot down to the private chain's SR set.
3. **TronGrid API key** — get one free at [trongrid.io](https://www.trongrid.io/). The free tier (~15 QPS / 100k req/day) is more than enough.

---

## Required java-tron patches

Mainnet transactions carry `ref_block_hash` / `expiration` / `timestamp` fields baked against mainnet time and block numbers. None of these are valid on a private chain. To let `broadcast` succeed, your private chain node must skip 4 checks.

The patches ship as ready-to-apply unified diffs under
`tools/replay/patches/`. Two ways to use them:

### Recommended: declare via `trond build.patches`

Put the patches in your intent and let `trond apply` handle build
isolation, deterministic cache keys, and rebuilds:

```yaml
nodes:
  - type: fullnode
    install_path: /tmp/trond-dev/replay-target
    build:
      source: ../java-tron
      gradle_task: ":framework:buildFullNodeJar"
      patches:
        - ../tron-deployment/tools/replay/patches/01-skip-tx-expiration.patch
        - ../tron-deployment/tools/replay/patches/02-skip-tapos-validation.patch
        - ../tron-deployment/tools/replay/patches/03-skip-network-expiration.patch
        - ../tron-deployment/tools/replay/patches/04-fast-proposals.patch
```

Then:

```bash
trond apply --intent replay-target.yaml -o json
```

trond opens a fresh `git worktree` of your `java-tron` checkout,
applies the 4 patches there, runs gradle, and stages the JAR — your
primary source tree is never touched. Same intent on a teammate's
laptop produces the same `cache_key` and (after the first build) hits
the same cache slot.

### Manual: apply with `git apply` then build

If you prefer not to involve trond's build pipeline:

```bash
cd /path/to/java-tron
git apply /path/to/tron-deployment/tools/replay/patches/*.patch
./gradlew :framework:buildFullNodeJar
```

The 4 patches are documented inline below for review purposes (the
files in `patches/` are the source of truth — these snippets may drift).


### Patch 1 — `TransactionCapsule.checkExpiration`

File: `chainbase/src/main/java/org/tron/core/capsule/TransactionCapsule.java`

```java
// clear method body to skip expiration check
public void checkExpiration(long nextSlotTime) throws TransactionExpirationException {
  // Replay mode: skip expiration check.
  // Mainnet tx expiration is signed against mainnet time; private chain
  // time will never line up. One patch here covers all three callers:
  // Wallet, Manager, TransactionsMsgHandler.
}
```

### Patch 2 — `Manager.java` expiration-window check

File: `framework/src/main/java/org/tron/core/db/Manager.java`

There are two expiration checks in function `processTransaction`; both must be disabled.
**TaPos / refBlockHash validator**
**Expiration vs head block time validator**

```java
// validateTapos(trxCap);
// validateCommon(trxCap);
```

### Patch 3 — `TransactionsMsgHandler` network-layer expiration check

File: `framework/src/main/java/org/tron/core/net/messagehandler/TransactionsMsgHandler.java`

Comment out the `checkExpiration` call and its catch arm below:

```java
//  trx.getTransactionCapsule().checkExpiration(chainBaseManager.getNextBlockSlotTime());
// ...
// catch (TransactionExpirationException e) {
//   logger.warn("Transaction expiration, transaction id: {}, error: {}",
//       trx.getTransactionCapsule().getTransactionId(), e.getMessage());
//   return new ResponseMessage(false, e.getMessage());
// }
```

### Patch 4 — `ProposalCapsule.hasExpired` (optional for faster proposal testing)

Mainnet proposals are designed to expire 3 days after creation. For private-chain testing that's far too slow — every protocol change needs to wait 3 days before it can activate. Force the check to always return `true` so the next maintenance window processes every pending proposal immediately.

File: `chainbase/src/main/java/org/tron/core/capsule/ProposalCapsule.java`

```java
public boolean hasExpired(long time) {
  return true; // private chain test mode — process at next maintenance regardless
}
```

After above patches applied, rebuild and restart and java-tron nodes:

```bash
./gradlew clean build -x test
# restart all FullNode / SR processes
```

Then, broadcast logs should no longer contain `Transaction expiration time is X, but next slot time is Y` or `Transaction expiration, transaction expiration time is X, but headBlockTime is Y`. Any remaining failures (`account does not exist`, `balance is not sufficient`, etc.) are state-layer issues unrelated to these patches.

---

## Usage

### First run — pass `--start` explicitly

```bash
export TRONGRID_API_KEY=<your-key> # skip if the free tier (~15 QPS / 100k req/day) is enough

./replay \
    --private-node http://private_node_ip:8090 \
    --start 82510633     # = snapshot's last mainnet block + 1
    --end 82510700       # optional; default = start + 10 (safety cap)
```

`--start` is **mandatory on the first run**. Private chain `getnowblock` returns the private chain's own block number, which decouples from mainnet's block number as soon as the chain produces its first block. The only reliable answer to "where do we resume from?" is the operator providing the value explicitly.

How to figure out the right `--start`:

```bash
# Immediately after importing the snapshot (before private chain produces any block),
# the private chain head equals the snapshot's mainnet block.
curl -s -X POST http://private_node_ip:8090/wallet/getnowblock \
    | jq .block_header.raw_data.number
# e.g. 82510632 → use --start 82510633

# Don't use this command after the private chain has been running.
```

### Subsequent runs — resume from state file

```bash
./replay --private-node http://private_node_ip:8090
```

On restart without `--start`, `replay-state.json`'s `last_mainnet_block` is read and replay continues from `+1`.

### Replay an explicit range

```bash
./replay \
    --private-node http://private_node_ip:8090 \
    --start 75630273 \
    --end 75637473
```

### Include Vote / Witness / Withdraw txs (off by default)

```bash
./replay \
    --private-node http://private_node_ip:8090 \
    --include-all
```

By default, these contract types are skipped because they're guaranteed to fail on a private chain:

- `VoteWitnessContract`
- `WitnessUpdateContract`
- `WitnessCreateContract`
- `WithdrawBalanceContract`

Use `--include-all` to forward them anyway — useful when observing failure modes (records land in `replay-failures.jsonl`).

### All flags

```
--trongrid-url       TronGrid API base URL (default: https://api.trongrid.io)
--trongrid-key       TronGrid API key (or env TRONGRID_API_KEY)
--trongrid-qps       TronGrid request rate limit (default: 1)
--tps-multiplier     Private chain TPS as a multiplier of mainnet TPS.
                     pace_sec = 3 / multiplier.
                     Default 0.333 (~1/3 mainnet TPS, 9 s/block — matches 3 SR
                     private vs 27 SR mainnet). 1 → 3 s/block (mainnet speed),
                     0.5 → 6 s/block (half speed), 2 → 1.5 s/block (double speed).
--private-node       Private chain HTTP API base, e.g. http://10.0.0.1:8090 (required)
--start              Start MAINNET block (inclusive). 0 = resume from state file
                     (required on first run; do NOT use private chain head).
--end                End block (inclusive). 0 = start + 10 (safety default).
--state-file         Resume state file (default: ./replay-state.json)
--fail-log           Failed broadcasts log, JSONL (default: ./replay-failures.jsonl)
--skip-log           Skipped txs log, JSONL (default: ./replay-skips.jsonl)
--include-all        Do not skip Vote/Witness/Withdraw contracts
```

State is flushed to `replay-state.json` after **every transaction** so the resume point is precise to a single tx (covers SIGKILL / OOM / power-loss). Block completion does an atomic flush that advances `last_mainnet_block` and clears `in_progress_block` / `in_progress_tx_index` together — no grey state where txs were re-broadcast but the block counter hasn't moved.

---

## Pacing algorithm

`--tps-multiplier` controls the private chain's TPS relative to mainnet:

```
pace_sec = 3 / multiplier             # mainnet block period is 3 s
slots    = max(1, floor(pace_sec))    # ~1 slot per second
```

| multiplier | pace | slots | use case |
|---|---|---|---|
| `1.0` | 3 s | 3 | replay at mainnet speed |
| `0.5` | 6 s | 6 | half speed |
| **`0.333`** (default) | **9 s** | **9** | **3 SR private vs 27 SR mainnet** |
| `2.0` | 1.5 s | 1 | 2× speedup |

Within each block, transactions are split into `slots` batches. Each batch fires its share of txs back-to-back, then waits for the absolute slot boundary. For a 150-tx block at `multiplier=0.333`:

```
slot 0 (0..1s):  tx[0..16]    fire 17 back-to-back  → wait until 1.0 s
slot 1 (1..2s):  tx[17..33]   fire 17 back-to-back  → wait until 2.0 s
slot 2 (2..3s):  tx[34..50]   fire 17 back-to-back  → wait until 3.0 s
...
slot 8 (8..9s):  tx[134..149] fire 16 back-to-back  → wait until 9.0 s
                                                       → advance to next block
```

Distribution: each slot gets `floor(n/slots)` txs; the first `n % slots` slots get one extra. Total always equals `n`.

**Why per-second batching instead of per-tx pacing**: 150 individual `time.After` channels per block is wasteful; per-second batching collapses that to 9. The trade-off — txs are bursty within each second — has no impact at the private chain's 3 s block granularity.

Absolute slot deadlines (`blockStart + slotDuration * (s+1)`) prevent drift: an over-long broadcast doesn't accumulate into the next slot. Empty blocks advance immediately rather than waste full `paceTotal`.

---

## Output files

All three files below are **generated at runtime** and grow as `replay`
runs. They are intentionally `.gitignore`d — they contain run-specific
data and should not be committed.

### `replay-state.json` — resume state

Written after every **transaction** for SIGKILL-grade resume precision.
`last_mainnet_block` is the **mainnet** block we've replayed up to, not
the private chain head (the two diverge as soon as the private chain
produces its own blocks).

`in_progress_block` and `in_progress_tx_index` are the per-tx checkpoint:
both are `0` (or absent) when no block is in flight; non-zero
`in_progress_block` means a previous run was killed midway and the next
start will resume from `in_progress_tx_index` of that block.

```json
{
  "last_mainnet_block": 82510650,
  "in_progress_block": 82510651,
  "in_progress_tx_index": 87,
  "total_fetched": 1234,
  "total_broadcast_ok": 1180,
  "total_broadcast_fail": 30,
  "total_skipped": 24
}
```

### `replay-failures.jsonl` — failed broadcasts

One JSON object per line, appended in order. `reason` is the node's
response code plus the hex-decoded `message` field.

```jsonl
{"block":82510633,"txID":"93d1a857da5c354b...","reason":"CONTRACT_VALIDATE_ERROR: balance is not sufficient"}
{"block":82510633,"txID":"91924a2f6bc44cfe...","reason":"DUP_TRANSACTION_ERROR"}
{"block":82510634,"txID":"a8331f4123d0302c...","reason":"NETWORK_ERROR: Post \"http://10.0.0.1:8090/wallet/broadcasttransaction\": dial tcp: i/o timeout"}
```

### `replay-skips.jsonl` — skipped transactions

Same structure as failures, but for txs filtered out before broadcast.
`reason` is `skip_type:<ContractType>` for the default-skipped types, or
`parse_error` / `no_contract` for malformed input.

```jsonl
{"block":82510651,"txID":"a501305be664a25a...","reason":"skip_type:VoteWitnessContract"}
{"block":82510653,"txID":"221db85dfae01009...","reason":"skip_type:WithdrawBalanceContract"}
{"block":82510657,"txID":"491bcabf8d372252...","reason":"skip_type:WithdrawBalanceContract"}
```

## Monitoring

```bash
# state (last_mainnet_block = mainnet block number we've replayed up to)
cat replay-state.json

# is the private chain itself advancing ~3 s/block? (unrelated to last_mainnet_block)
watch -n 1 'curl -s -X POST http://private_node_ip:8090/wallet/getnowblock \
    | jq .block_header.raw_data.number'

# top failure reasons
jq -r .reason replay-failures.jsonl | sort | uniq -c | sort -rn

# skip stats
jq -r .reason replay-skips.jsonl | sort | uniq -c
```

## Interrupt and resume

All three interrupt scenarios resume cleanly. In every case, restart without `--start` and replay reads the state file automatically.

| Scenario | State at interrupt | Resume point |
|---|---|---|
| `Ctrl+C` (SIGINT/SIGTERM) | Current tx finishes, state already flushed | The next tx in the in-progress block |
| `kill -9` / OOM / power loss | State is flushed after every tx, so the most recent durable checkpoint is `in_progress_block` + `in_progress_tx_index` | `in_progress_tx_index` of `in_progress_block` (at most one tx may be re-broadcast → typically a single `DUP_TRANSACTION_ERROR` in the fail log) |
| All-fail block aborts | `in_progress_block` preserved, `in_progress_tx_index` reset to `0` | After the operator fixes the underlying issue, replay restarts the aborted block from **tx 0** |

How it works:
- Every processed tx (broadcast / failed / skipped) flushes state, so the resume point is precise to a single tx.
- Block completion is a single atomic flush that advances `last_mainnet_block` and clears `in_progress_*` together. There's no grey state where txs were re-broadcast but the block counter hasn't moved.
- A resumed block sends the remaining txs back-to-back (the slot algorithm depends on the block-start wallclock, which is no longer meaningful after a restart). Pacing returns to normal at the next block.

---

## Common failure modes

Reasons that show up in `replay-failures.jsonl`:

| Reason | Cause | Fix |
|---|---|---|
| `CONTRACT_VALIDATE_ERROR: balance is not sufficient` | Private chain account doesn't have enough TRX | Top up balance during `db fork`, or skip txs from this account |
| `CONTRACT_VALIDATE_ERROR: account does not exist` | Mainnet account isn't in the private chain snapshot | Re-fork including that account, or ignore |
| `DUP_TRANSACTION_ERROR` | Same tx already replayed | Normal during resume — ignore |
| `BAD_TRANSACTION_ERROR: validateSignature` | Node hasn't applied the patches above | Recompile the node from a `relay_skip_signature` branch |
| `BAD_TRANSACTION_ERROR: too big` | Tx exceeds size limit | Raise `block.maxTimeUntilExpiration` in node config |
| `NETWORK_ERROR: ...` | Private chain node unreachable | Check process state + HTTP port |

---

## How this compares to `tron-docker/stress_test`

`tron-docker/tools/stress_test` provides similar functionality via a different architecture:

| Dimension | `tron-docker/stress_test` | `replay` |
|---|---|---|
| Relation to java-tron | embedded fork of FullNode | external HTTP client |
| Compile dependency on java-tron | yes (BroadcastNode embeds java-tron source) | none |
| Artifact | jar + JDK runtime | single ~8 MB Go binary |
| Transport | gRPC + in-process broadcast | HTTP / JSON |
| Data flow | two-stage: generate → CSV → broadcast | single-stage streaming |
| Resume | none | per-block via `state.json` |
| Pre-filter known-failing contract types | no | yes (default-on; `--include-all` opts out) |
| Failure log | unstructured / via node logs | structured JSONL |
| All-fail block detection | no | yes (auto-aborts) |
| Rate control | fixed `tpsLimit` | `tps-multiplier` derived from SR ratio |

The reverse direction also holds: `stress_test` covers synthetic transaction generation (TRX / TRC10 / TRC20) and multi-target broadcast, which `replay` does not implement.
