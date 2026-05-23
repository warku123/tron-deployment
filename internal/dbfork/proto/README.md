# dbfork proto bindings

Source of truth for the protobuf wire formats trond's `dbfork` engine
reads from and writes to java-tron's on-disk capsule stores.

## Layout

```
internal/dbfork/proto/
├── upstream/                # git subtree of github.com/tronprotocol/protocol
│   ├── core/
│   │   ├── Tron.proto       # Account, Witness, Permission, Vote, AccountAsset
│   │   └── ...
│   └── api/                 # (unused — kept for forward extension)
├── pb/                      # generated *.pb.go (committed)
│   └── core/...
├── gen.go                   # `go generate` entry
└── README.md
```

`upstream/` is the unmodified protocol repo at a pinned tag. `pb/`
holds the generated Go bindings — these are committed so `go build`
works without protoc on every machine.

## Pinned upstream version

Last subtree pull: **GreatVoyage-v4.8.1** (Feb 2026).

The full pin history is in this directory's git log (look for
`Squashed 'internal/dbfork/proto/upstream/' content from commit ...`
messages from `git subtree add/pull` operations).

## Syncing upstream

When java-tron releases a new compatible version (typically a yearly
"GreatVoyage" tag), bump the proto subtree:

```bash
# From repo root
git subtree pull \
  --prefix=internal/dbfork/proto/upstream \
  https://github.com/tronprotocol/protocol.git \
  GreatVoyage-v4.8.2 \
  --squash

# Regenerate bindings
go generate ./internal/dbfork/proto/...

# Verify equivalence against java DbFork (release gate)
go test ./internal/dbfork/ -run TestEquivalenceVsJavaDbFork

# If green, commit both the subtree merge AND the regenerated pb/.
git add internal/dbfork/proto/upstream internal/dbfork/proto/pb
git commit -m "dbfork: bump proto to GreatVoyage-v4.8.2"
```

## What we generate (and why this subset)

`scripts/gen-dbfork-protos.sh` lists the .proto files we run through
protoc. The current subset covers the messages dbfork's mutation
engine reads + writes:

| Proto file | Messages used by dbfork |
|---|---|
| `core/Tron.proto` | `Account`, `Witness`, `Permission`, `Vote`, `AccountAsset` |
| `core/contract/asset_issue_contract.proto` | `AssetIssueContract` (TRC10 metadata) |
| `core/contract/smart_contract.proto` | `SmartContract` (TRC20 contract object) |
| `core/contract/account_contract.proto` | `AccountCreateContract` (permission updates) |
| transitive deps | autoload by protoc when imported |

Everything else stays in `upstream/` for future extension — add to
`PROTO_FILES` in the gen script when you need it.

## Why not just use `tronprotocol/grpc-gateway` Go bindings?

`grpc-gateway` ships Go bindings for the same proto repo, but it's
oriented at gRPC clients (it has the service stubs we don't need)
and pulls in dozens of transitive imports we don't want in trond's
binary. Generating only what we use keeps trond's static binary
small and our dependency surface narrow.

## Tooling install

```bash
# macOS
brew install protobuf
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest

# Linux (debian/ubuntu)
sudo apt install protobuf-compiler
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
```

`$GOPATH/bin` must be on `$PATH` so protoc can find `protoc-gen-go`.

## Why protoc-gen-go and not gogo or buf?

- **protoc-gen-go** is the reference Go protobuf generator,
  maintained by the Go team. Mature, predictable.
- **gogo/protobuf** is faster + smaller but archived since 2022.
- **buf** is a higher-level toolchain that would add a buf.yaml +
  registry workflow; overkill for a single subtree.

Reference: https://protobuf.dev/reference/go/
