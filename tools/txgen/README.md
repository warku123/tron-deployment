# txgen

A TRON stress-test transaction generator. Pure-Go HTTP client — no java-tron source dependency, no JDK runtime.

Functional: three subcommands (`generate` / `broadcast` / `statistic`), TRX / TRC10 / TRC20 transaction types, CSV intermediate format, and a broadcast report that matches the upstream layout so existing dashboards keep working.

Useful for:
- Loading a private chain to its TPS ceiling with shaped traffic mixes.
- Snapshot validation paired with a `db fork` clone.
- Benchmarking node patches (mempool, EVM, signature verification) under reproducible load.

## Install

```bash
# From the tron-deployment repo root
make build-txgen        # → bin/txgen
make install-txgen      # → $(GOBIN)/txgen
```

Requires Go 1.21+. Builds a single ~10 MB static binary.

The HTTP broadcast layer lives in `tools/common/broadcast/` and is shared with `tools/replay/`.

---

## Quickstart — generate and broadcast to a private chain

End-to-end flow against a private chain at `private_node_ip:8090`. Two commands.

### 1. Write a config

Save this as `txgen.json` next to the binary:

```json
{
  "node": "http://private_node_ip:8090",

  "generate": {
    "totalTxCount": 100000,
    "receiverAddressCount": 10000,
    "concurrency": 16,
    "privateKey": "<64-hex-char sender key>",
    "outputDir": "txgen-output",
    "txType": { "transfer": 100, "transferTrc10": 0, "transferTrc20": 0 },
    "transferAmount": 1
  },

  "broadcast": {
    "inputDir": "txgen-output",
    "tpsLimit": 2000,
    "saveTxId": true
  }
}
```

The sender (`generate.privateKey`) must hold enough TRX on the private chain to cover all activations + transfer amounts. Pre-fund it with `Toolkit.jar db fork` if needed.

### 2. Generate signed transactions

```bash
txgen generate -c txgen.json
```

`generate` first builds `receiverAddressCount` fresh secp256k1 receivers (dumped to `txgen-output/receivers.csv` for auditability or `db fork` pre-funding), then builds `totalTxCount` signed TRX transfers fanning out across those receivers and splits them into `txgen-output/generate-tx-NNNN.csv`. Workers issue one HTTP round-trip per tx to the node (`/wallet/createtransaction`) for the unsigned form, then sign locally with secp256k1.

**Time-sensitive:** the node stamps each tx with `expiration = head_block_time + 60s` by default. Broadcast within a minute, or raise `block.maxTimeUntilExpiration` in the node's `config.conf` first.

### 3. Broadcast to the private chain

```bash
txgen broadcast -c txgen.json
```

Streams every `generate-tx-*.csv` through `/wallet/broadcasttransaction` at `tpsLimit` (token-bucket throttled). Captures the head block before and after, then walks that range to compute the actual on-chain TPS. Output:

- `broadcast-txid.csv` — txIDs the node accepted into its pool
- `broadcast-report.txt` — final report (TPS, on-chain rate, block size stats)

Sample report:

```
Stress test report:
broadcast tps limit:        2000
statistic block range:      startBlock: 67926067, endBlock: 67926133
total generate tx count:    100000
total broadcast ok:         98214
total broadcast fail:       1786
tx on chain rate:           0.96810
cost time:                  0.85 minutes
max block size:             9615
min block size:             3001
tps:                        1933.65
miss block rate:            0.000000
```

### 4. (Optional) Re-measure TPS for any block range

```bash
# Edit statistic.startBlock / endBlock in txgen.json, then:
txgen statistic -c txgen.json
```

Useful for tightening a wider broadcast window to just the active span, or re-running the math from a node that synced later.

---

## Mixing TRX, TRC10, TRC20

Change `generate.txType` weights (must sum to 100) and add the token identifiers:

```json
{
  "generate": {
    ... 
    "txType": {
      "transfer": 60,
      "transferTrc10": 10,
      "transferTrc20": 30
    },
    "trc10Id": "1000001",
    "trc20Address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    "trc20FeeLimit": 100000000
  }
}
```

The sender must hold both the TRC10 balance and TRC20 balance — pre-fund via `db fork` or send a few warm-up txs first.

---

## Post-quantum (PQ / 抗量子) transactions

For nodes built with java-tron's post-quantum signature support, txgen can sign
some fraction of transactions with a PQ scheme instead of secp256k1. A PQ tx
carries a `pq_auth_sig` envelope (`{scheme, public_key, signature}`) rather than
the ECDSA `signature`; the signed digest is unchanged (`txID = sha256(raw_data)`),
so the rest of the generate → broadcast → statistic flow is identical.

Enable it with a `generate.pq` block and set `ratio` to the percent of
transactions that should be PQ-signed (the rest are ECDSA-signed):

```json
{
  "generate": {
    "...": "...",
    "privateKey": "<64-hex-char sender key>",
    "txType": { "transfer": 100, "transferTrc10": 0, "transferTrc20": 0 },
    "pq": {
      "enabled": true,
      "scheme": "ML_DSA_44",
      "seed": "<64-hex-char (32-byte) seed>",
      "ratio": 30
    }
  }
}
```

- **Ratio (mixed signing).** `pq.ratio` is the percent of generated txs that are
  PQ-signed; the remaining `100 - ratio` percent are ECDSA-signed. It is an
  independent roll from the `txType` contract-type split, so each contract type
  gets roughly the same PQ/ECDSA mix. `ratio` defaults to `100` when omitted (all
  PQ); to turn PQ off entirely set `pq.enabled = false` rather than `ratio = 0`.
- **Two senders.** Every tx's owner address must match the key that signs it, so
  PQ txs are sent from the PQ-derived address and ECDSA txs from the `privateKey`
  address. When `ratio < 100`, **both** `privateKey` and `pq.seed` are required
  (when `ratio == 100`, `privateKey` is not needed). The PQ sender is derived from
  `pq.seed` as `0x41 ‖ Keccak-256(public_key)[12..32]`. `txgen generate` logs both
  sender addresses at startup.
- **Scheme.** Only `ML_DSA_44` (FIPS 204 ML-DSA-44 / CRYSTALS-Dilithium-2) is
  supported. It interoperates with the node's BouncyCastle verifier. Falcon-512
  (`FN_DSA_512`) is **not** supported: java-tron uses a BouncyCastle/EIP-8052
  specific wire encoding that no Go library reproduces, so a Go-signed Falcon
  signature would be rejected on-chain.
- **Account provisioning (required).** The node only accepts a PQ signature when
  the PQ sender account's permission already contains this PQ public key with
  enough weight. Install it out-of-band (e.g. via `AccountPermissionUpdate`)
  before generating; txgen does not perform the permission update. The same
  `seed` always derives the same keypair/address, so provision once and reuse.

---

## Prerequisites

1. **Private chain node is reachable** at the `node` URL (default `http://127.0.0.1:8090`).
2. **Sender account is funded.** `generate.privateKey` must hold enough TRX for activation fees plus the transferred amount, plus any TRC10 / TRC20 balance you use.
3. **Receivers exist or get activated.** Fresh addresses generated inline by `txgen generate` are valid — the first transfer to each pays a one-time activation fee out of the sender's TRX.

Common helpers:
- `Toolkit.jar db fork` with `<outputDir>/receivers.csv` to pre-fund receivers in one shot.
- Raise `block.maxTimeUntilExpiration` in node config if your generate→broadcast gap is longer than 60s.

---

## Subcommands (reference)

### `generate` — build receivers + sign synthetic txs

```bash
txgen generate -c txgen.json
```

Step 1: build `generate.receiverAddressCount` fresh secp256k1 keypairs and dump them to `<outputDir>/receivers.csv`. Receivers are valid TRON addresses — they get activated by the first transfer (pays a one-time activation fee out of the sender's TRX). Keys are included in the sidecar so the same addresses can be pre-funded via `Toolkit.jar db fork` if you'd rather skip the activation fees.

Step 2: for each of `generate.totalTxCount` transactions:

1. Pick a tx type by weighted random sample.
2. Pick a receiver at random from the in-memory list.
3. POST to the node (`/wallet/createtransaction`, `/wallet/transferasset`, or `/wallet/triggersmartcontract`) to get an unsigned tx + `raw_data_hex` + `txID`.
4. Sign `txID` locally with secp256k1 (canonical low-S; `[r || s || v]` 65-byte layout).
5. Attach the signature and append to a `generate-tx-NNNN.csv` file.

Output is sharded across multiple CSV files (task size auto-derived from `totalTxCount` and `concurrency`); `concurrency` workers consume the task queue.

### `broadcast` — replay CSV at a target TPS

```bash
txgen broadcast -c txgen.json
```

Streams CSVs through `/wallet/broadcasttransaction`, throttled by a token bucket (100 ms refill). Worker count scales with `tpsLimit / 50`, clamped to `[4, 256]`. Failed broadcasts log the first 20 verbatim, then every 10,000th, so stderr stays readable without hiding structural issues (bad signature, dead node).

### `statistic` — compute on-chain TPS for any range

```bash
txgen statistic -c txgen.json
```

Walks `[statistic.startBlock, statistic.endBlock]` on the node and emits a TPS report.

---

## Output files

All files land under `generate.outputDir` (default `txgen-output/`), except the broadcast/statistic reports which are written to the paths configured in the `broadcast` / `statistic` sections.

| File | Contents |
|---|---|
| `receivers.csv` | Inline-generated receiver addresses + private keys. Useful for `db fork` pre-funding. |
| `generate-tx-NNNN.csv` | Signed transactions, `txID,signed_tx_json` per row. |
| `broadcast-txid.csv` | TxIDs the node accepted into its pool (one per line). |
| `broadcast-report.txt` | Final report (TPS, on-chain rate, block size stats). |
| `tps-statistic.txt` | `statistic` subcommand output. |

---

## Configuration reference

txgen reads a single JSON file (default `./txgen.json`, override with `-c` / `--config`).

### Full example

```json
{
  "node": "http://private_node_ip:8090",

  "generate": {
    "totalTxCount": 600000,
    "receiverAddressCount": 100000,
    "concurrency": 16,
    "privateKey": "aab926e86a17f0f46b4d22e61725edd5770a5b0fbdabb04b0f46ee499b1e34f2",
    "outputDir": "txgen-output",
    "txType": { "transfer": 60, "transferTrc10": 10, "transferTrc20": 30 },
    "trc10Id": "1000001",
    "trc20Address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
    "transferAmount": 1,
    "trc20FeeLimit": 100000000
  },

  "broadcast": {
    "inputDir": "txgen-output",
    "tpsLimit": 3000,
    "saveTxId": true,
    "txIdFile": "broadcast-txid.csv",
    "reportFile": "broadcast-report.txt"
  },

  "statistic": {
    "startBlock": 0,
    "endBlock": 0,
    "outputFile": "tps-statistic.txt"
  }
}
```

### Schema

| Section | Key | Default | Description |
|---|---|---|---|
| top-level | `node` | `http://127.0.0.1:8090` | TRON HTTP API endpoint (used by every subcommand). |
| `generate` | `totalTxCount` | — (required) | Total signed txs to build. |
| `generate` | `receiverAddressCount` | `1000` | Fresh receiver addresses to generate in-memory and dump to `<outputDir>/receivers.csv`. |
| `generate` | `concurrency` | `8` | Worker goroutines (each issues node round-trips serially). Task size (rows per CSV file) is auto-derived so each worker gets ~4 tasks, clamped to [1000, 100000]. |
| `generate` | `privateKey` | — (required unless `pq.enabled` with `pq.ratio` 100) | Sender secp256k1 key, hex (64 chars). Signs the ECDSA share of txs. |
| `generate` | `outputDir` | `txgen-output` | Directory for `receivers.csv` and `generate-tx-NNNN.csv` files. |
| `generate` | `txType.transfer` | — | TRX share, in percent. |
| `generate` | `txType.transferTrc10` | — | TRC10 share, in percent. |
| `generate` | `txType.transferTrc20` | — | TRC20 share, in percent. The three weights must sum to 100. |
| `generate` | `trc10Id` | — | TRC10 token id (numeric string). Required iff `transferTrc10 > 0`. |
| `generate` | `trc20Address` | — | TRC20 contract address (base58 or hex). Required iff `transferTrc20 > 0`. |
| `generate` | `transferAmount` | `1` | Amount per tx in the smallest unit (SUN for TRX, raw token units otherwise). |
| `generate` | `trc20FeeLimit` | `100000000` | Fee limit (SUN) on TRC20 calls. 100 TRX is plenty for a vanilla `transfer`. |
| `generate` | `pq.enabled` | `false` | Mix in post-quantum signing (`pq_auth_sig`). See [Post-quantum transactions](#post-quantum-pq--抗量子-transactions). |
| `generate` | `pq.scheme` | `ML_DSA_44` | PQ scheme. Only `ML_DSA_44` is supported. |
| `generate` | `pq.seed` | — (required iff `pq.enabled`) | 32-byte hex (64 chars) seed; derives the PQ keypair and sender address. |
| `generate` | `pq.ratio` | `100` | Percent of txs PQ-signed (1–100); the rest are ECDSA-signed. Only used when `pq.enabled`. |
| `broadcast` | `inputDir` | = `generate.outputDir` | Where to find `generate-tx-*.csv`. |
| `broadcast` | `tpsLimit` | `1000` | Token-bucket cap, txs/second. |
| `broadcast` | `saveTxId` | `false` | Append accepted txIDs to `txIdFile`. |
| `broadcast` | `txIdFile` | `broadcast-txid.csv` | One txID per line. |
| `broadcast` | `reportFile` | `broadcast-report.txt` | Final report path. |
| `statistic` | `startBlock` | — | Block range start (inclusive). |
| `statistic` | `endBlock` | — | Block range end (inclusive). |
| `statistic` | `outputFile` | `tps-statistic.txt` | Report path. |

---

## Common failure modes

| Failure | Cause | Fix |
|---|---|---|
| `generate: probe node` failure | Node URL wrong or unreachable | Check `node` URL and that the HTTP port (8090 by default) is open. |
| `generate.privateKey is required` | `txType` weights set but no sender key | Set `generate.privateKey` to a funded account's secp256k1 hex. |
| `generate: balance is not sufficient` (in node logs) | Sender ran out of TRX mid-run | Top up the sender, or shrink `totalTxCount`. |
| `broadcast fail: SIGERROR: ...` | Signature didn't verify | Confirm the sender key matches the address that owns the source funds. Re-run `generate`. |
| `broadcast fail: DUP_TRANSACTION_ERROR` | Same CSV was broadcast twice | Normal on a re-run. Move the old CSV out of `inputDir`. |
| `broadcast fail: ... transaction expiration ...` | CSV is older than the node's expiration window | Re-run `generate`, then `broadcast` within ~60s, or raise `block.maxTimeUntilExpiration` in node config. |
| `statistic: fetch block X` | Block doesn't exist yet, or RPC is filtered | Tighten the range, or wait until the chain catches up. |

---

## Roadmap

- **Local proto-built unsigned txs.** Drop the per-tx HTTP round-trip in `generate` by serializing `Transaction.raw` locally. Would push generate-phase throughput up by ~10×.
- **Multi-target broadcast.** Round-robin across a list of `broadcastUrl`s instead of one `node`.
- **Synthetic blend.** Mix synthetic txs with replayed mainnet txs (paired with `tools/replay`).
- **Prometheus metrics.** Expose generate / broadcast counters for Grafana panels.
