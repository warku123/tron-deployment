# Shadow-fork PoC walkthrough

End-to-end demo of the dbfork toolchain: take a real Nile testnet
snapshot, replace the witness set with one you control, launch a
private chain off the modified state, and watch it produce blocks.

This is the verification path that proves the four prior
deliverables (mutation engine, fork.conf loader, CLI surface,
equivalence test) actually compose into a usable shadow-fork
workflow. Everything is automated by `scripts/poc-shadow-fork.sh`;
this doc explains what each step does + how to verify success.

## Prereqs

- `trond` binary built: `make build`
- Docker + docker-compose running
- ~30 GB free disk (lite Nile snapshot is ~10-20 GB; extraction +
  scratch overhead)
- Python 3 with `tronpy` for the witness keypair generation:
  `pip install tronpy`
  - OR provide your own via `SHADOW_FORK_WITNESS_KEY` (hex private
    key) and `SHADOW_FORK_WITNESS_ADDRESS` (Base58Check TRON
    address) env vars.

## Quickstart

```bash
./scripts/poc-shadow-fork.sh all
```

Runs setup + mutate + apply + observe in sequence. Total time ~30
minutes (dominated by the snapshot download). The script PASSes
once it observes 5 block-height increments on the shadow-fork
chain's JSON-RPC.

Or run the four phases individually for inspection:

```bash
./scripts/poc-shadow-fork.sh setup     # ~30 min  — snapshot + keypair + templates
./scripts/poc-shadow-fork.sh mutate    # ~10-30s  — fork.conf → snapshot mutation
./scripts/poc-shadow-fork.sh apply     # ~30s     — docker compose up
./scripts/poc-shadow-fork.sh observe   # ~30s     — poll JSON-RPC for blocks
./scripts/poc-shadow-fork.sh teardown  # ~5s      — docker compose down
```

## What each phase does

### setup

1. Downloads the latest Nile lite snapshot via `trond snapshot
   download --network nile --type lite --to ./shadow-fork-data`.
   Idempotent — re-runs detect the existing `output-directory/`
   and skip.
2. Generates a fresh secp256k1 witness keypair via `tronpy`,
   stashing it in `.shadow-fork-witness.env` (gitignored). The
   address is what fork.conf installs as the active witness; the
   private key is what the trond intent's witness_key resolves
   from at deploy time.
3. Expands the two templates (`examples/shadow-fork/{fork.conf,
   intent.yaml}.template`) into repo-root copies with the
   generated address + the current timestamp substituted in.

### mutate

```bash
trond shadow-fork mutate \
  -d ./shadow-fork-data/output-directory \
  -c shadow-fork.conf \
  --output json
```

Applies the mutation to the snapshot in place. Output JSON
reports counters from `dbfork.Result`:

- `witnesses_written: 1` — the new witness landed in `witness/`.
- `active_witnesses: 1` — the active slate in `witness_schedule/`
  is the single new address (concat of 21 bytes).
- `accounts_modified: 1` — the witness account's balance was set.
- `properties_updated: 3` — `latestBlockHeaderTimestamp`,
  `maintenanceTimeInterval`, and `nextMaintenanceTime` all
  written (MutateProperties writes any non-zero field; the
  template sets all three).

### apply

```bash
SHADOW_FORK_WITNESS_KEY=<hex> trond apply --intent shadow-fork-intent.yaml
```

Launches a single-witness java-tron node via docker-compose with
the mutated snapshot bind-mounted as `output-directory/`. The
node boots, reads the modified witness store, sees itself as the
only active SR, and starts producing blocks 3 seconds later.

**`apply` returning ≠ chain ready.** `trond apply` returns once
docker-compose reports the container up, but java-tron still
needs ~30 seconds for DB warmup before the first produced block.
The `observe` step below is the actual readiness check.

**Security note — rendered HOCON contains the private key.**
trond inlines the witness's hex private key into the per-node
HOCON config at `~/.trond/deployments/shadow-fork-poc/shadow-fork-poc.conf`
(java-tron requires the key in-config for production). Mode
defaults to 0644. Treat that file like the keypair stash —
remove with the `teardown` recipe at the bottom of this doc
once finished.

### observe

Polls `eth_blockNumber` on the node's JSON-RPC every 5s. PASSes
once 5 height increments are observed (~25s after first poll).
FAILs after 5 minutes if the chain doesn't advance — typical
causes are:

- Witness private key doesn't match the address in fork.conf
  (re-run setup to regenerate the pair).
- Snapshot wasn't actually mutated (re-run mutate; check the
  JSON output's counters are non-zero).
- Container failed to start (`trond logs shadow-fork-poc`
  shows the java-tron stack trace).

### teardown

`trond remove shadow-fork-poc --confirm shadow-fork-poc`.

The mutated snapshot at `./shadow-fork-data/` is PRESERVED — you
can re-`apply` without re-downloading or re-mutating. To start
fully fresh, `rm -rf ./shadow-fork-data shadow-fork.conf
shadow-fork-intent.yaml .shadow-fork-witness.env`.

## Verifying byte-equivalence with java DbFork

The shadow-fork chain produces blocks → mutation works
operationally. To prove byte-equivalence with the canonical java
DbFork (Task #152 release gate):

```bash
# After scripts/poc-shadow-fork.sh setup runs, you have both:
#   - ./shadow-fork-data/output-directory  (a copy ready to mutate)
#   - shadow-fork.conf                     (the fork.conf to apply)

# Build the java toolkit jar (one-time):
cd ../tron-docker/tools/toolkit && ./gradlew shadowJar
# Produces build/libs/toolkit-x.y.z-all.jar

# Set the equivalence-test env vars. DBFORK_NILE_FIXTURE points
# at the PARENT of database/ (equivalence_test.go does
# filepath.Join(fixturePath, "database")), so the value is the
# snapshot's output-directory subdir, not the shadow-fork-data
# root.
export DBFORK_NILE_FIXTURE=$(pwd)/../../mydev/tron-deployment/shadow-fork-data/output-directory
export DBFORK_JAVA_TOOLKIT=$(pwd)/build/libs/toolkit-*-all.jar
export DBFORK_FORK_CONF=$(pwd)/../../mydev/tron-deployment/shadow-fork.conf

# Run the equivalence gate:
cd ../../mydev/tron-deployment
go test -v -run TestEquivalence_GoVsJava ./internal/dbfork/
```

The test stands up two scratch copies of the snapshot, applies
the same fork.conf via Go and Java, and diffs all 8 dbfork stores.
A pass means the Go port and java DbFork produce byte-identical
state from the same input.

## Host architecture: amd64 required for the apply phase

The `mutate` phase is architecture-portable (the dbfork engine is pure
Go and reads/writes goleveldb on whatever the host CPU is). The `apply`
phase, however, runs java-tron in a docker container and java-tron has
a hard arm64 limitation:

```
WARN [db](Storage.java:180) Arm64 architecture detected, using RocksDB
as db engine, ignore config.
TronError: Cannot open LEVELDB database with ROCKSDB engine.
```

On arm64, java-tron auto-switches to the RocksDB engine regardless of
`storage.db.engine` config. The standard Nile snapshot is LevelDB-format,
so the container crash-loops with `Shutting down with code: ROCKSDB_INIT(1)`
13+ times before docker gives up. Verified empirically on AWS Graviton2
during the Phase 1 PoC test run.

**For the `apply` phase, use an amd64 host.** mutate-only runs (proving
the engine works against real data) are fine on arm64.

A RocksDB-format Nile snapshot exists at
`snapshots.nileex.io/rocksdb/backup<date>/FullNode_output-directory.tgz`
and dbfork's RocksDB engine is implemented via grocksdb under the
`rocksdb` build tag. See `internal/dbfork/db/rocksdb_enabled.go` for
the full build prereqs — short version, grocksdb v1.10.8 is hard-
coupled to RocksDB 10.10.1, so build via the bundled script:

```bash
GROCKSDB=$(go env GOMODCACHE)/github.com/linx!gnu/grocksdb@v1.10.8
cd "$GROCKSDB" && make libs   # ~10-15 min, builds RocksDB + deps statically

cd /path/to/tron-deployment
export CGO_CFLAGS="-I$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/include"
export CGO_LDFLAGS="-L$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/lib -lrocksdb -lstdc++ -lm -lz -lbz2 -llz4 -lzstd -lsnappy"
go build -tags rocksdb -o bin/trond .
```

Then point trond at the RocksDB snapshot the same way as LevelDB —
DetectKind reads java-tron's per-store `engine.properties` and routes
automatically. Release-binary support (CI, goreleaser, cross-compile)
is tracked in Task #163.

## Phase 1 caveats

- **Single witness, no finality**: the demo chain produces blocks
  every 3s but the per-block SR confirmation count stays at 1
  (well below the 19/27 threshold for finality). Good enough to
  prove the mutation worked; not sufficient for testing finality-
  dependent contract logic. Real shadow-fork testing uses 27
  witnesses for production parity.
- **Lite snapshot pruning**: lite snapshots include the 8 dbfork-
  relevant stores but exclude historical block + transaction
  data. The shadow-fork chain starts producing blocks AFTER the
  snapshot block; queries against pre-snapshot block ranges will
  return empty.
- **Network isolation**: the intent uses `network: nile` (required —
  the snapshot's genesis hash is Nile's; a `private` network would
  crash-loop with "Genesis block modify"). Isolation from real Nile
  peers comes from `node.p2p.version: 99999` (any real Nile node
  treats us as a foreign chain version) + empty `seed.node.ip.list`
  (we never reach out). For multi-node shadow-fork testing, add
  additional fullnodes to the intent and pin them to the witness
  via the same p2p-version + seed-list isolation.

## Where to go next

- For programmatic / agent-driven workflows, see
  AGENTS.md "Workflow 5 — Shadow-fork testing on a real
  snapshot" and the `shadow_fork_mutate` MCP tool.
- For multi-witness production-parity shadow forks, extend
  `examples/shadow-fork/fork.conf.template` with 27 witness
  entries + their keypairs, and add corresponding nodes to the
  intent.
- For testing specific hard-fork proposals against real mainnet
  state, swap `--network nile` for `--network mainnet` and
  update fork.conf with the mainnet accounts you want to fund.
