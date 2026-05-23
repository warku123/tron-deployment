#!/usr/bin/env bash
# =============================================================================
# scripts/gen-dbfork-protos.sh — regenerate internal/dbfork/proto/pb/*.pb.go
#
# Driven by `go generate ./internal/dbfork/proto/...`. Idempotent. Assumes
# protoc + protoc-gen-go are on PATH (see proto/README.md for install).
#
# Strategy: only generate the messages dbfork's mutation engine touches.
# Everything in upstream/ stays available for future extension via additional
# entries in PROTO_FILES below.
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."   # repo root
UPSTREAM="internal/dbfork/proto/upstream"
OUT="internal/dbfork/proto/pb"

# Subset we GENERATE Go bindings for. Add entries here as dbfork's
# surface grows. Files transitively imported by these (but not in
# this list) get their go_package overridden too — see ALL_PROTOS
# below — but no .pb.go is emitted for them.
PROTO_FILES=(
    "core/Tron.proto"
    "core/Discover.proto"                       # Endpoint, used by Tron.proto
    "core/contract/account_contract.proto"
    "core/contract/asset_issue_contract.proto"
    "core/contract/smart_contract.proto"
    "core/contract/common.proto"
    "core/contract/balance_contract.proto"
    "core/tron/account.proto"
    "core/tron/transaction.proto"
)

# All bindings share this go module prefix so imports resolve.
GO_PACKAGE_PREFIX="github.com/tronprotocol/tron-deployment/internal/dbfork/proto/pb"

# Build M-flags for EVERY .proto file in upstream/. All map to the
# SAME Go package, which mirrors the .proto files' single
# `package protocol;` namespace. Splitting them by directory (e.g.
# core/Tron.proto → pb/core, core/contract/x.proto → pb/core/contract)
# creates Go import cycles because the .proto messages cross those
# boundaries freely (Tron.Account references contract messages and
# vice versa). One Go package = no cycles, matches upstream design.
mapfile -t ALL_PROTOS < <(cd "$UPSTREAM" && find . -name "*.proto" -type f | sed 's|^\./||' | sort)
GO_OPTS=()
for f in "${ALL_PROTOS[@]}"; do
    # M flag format: <proto-path>=<go-import>;<go-pkg-name>
    # All files share both the import path AND the package name "tronpb"
    # so they compile as a single Go package without name collisions.
    # (Without ;tronpb, protoc-gen-go infers the package name from the
    # .proto's `package protocol.contract;` etc. and they diverge.)
    GO_OPTS+=("--go_opt=M${f}=${GO_PACKAGE_PREFIX};tronpb")
done

# Ensure the output dir exists (otherwise protoc errors). Empty it
# first so stale generated files from prior runs don't linger after
# we drop a proto from the subset.
rm -rf "$OUT"
mkdir -p "$OUT"

# protoc emits flat into $OUT because every file maps to the same
# Go package (see ALL_PROTOS loop above). `module=` strips the
# upstream's go_package URL prefix; output files land at
# $OUT/<basename>.pb.go directly. Tron.pb.go, smart_contract.pb.go,
# etc. all in one dir → one Go package → no import cycles.
protoc \
    -I="$UPSTREAM" \
    "${GO_OPTS[@]}" \
    --go_out="$OUT" \
    --go_opt=module="$GO_PACKAGE_PREFIX" \
    "${PROTO_FILES[@]/#/$UPSTREAM/}"

echo "Generated:"
find "$OUT" -name "*.pb.go" -print | sort
