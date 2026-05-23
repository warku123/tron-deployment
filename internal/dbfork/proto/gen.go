// Package proto holds the protobuf bindings trond's dbfork engine
// uses to read + write java-tron's on-disk capsule formats.
//
// Layout:
//
//	upstream/  — git subtree of github.com/tronprotocol/protocol at a
//	             pinned GreatVoyage tag (e.g. GreatVoyage-v4.8.1).
//	             Source-of-truth .proto files. Sync via subtree pull
//	             when bumping java-tron compatibility — see README.md.
//	pb/        — generated *.pb.go files. Committed to the repo so
//	             `go build` doesn't require protoc on every dev
//	             machine. Regenerate via `go generate ./...` from
//	             repo root after a subtree pull.
//
// Scope: we only generate Go bindings for the subset of messages
// dbfork actually reads / writes (Account, Witness, Permission,
// SmartContract, AssetIssueContract + their transitive deps).
// upstream/ stays fully populated for future extension; pb/ stays
// narrow so binary size + compile time don't bloat.
//
// Generation requires protoc + protoc-gen-go:
//
//	brew install protobuf
//	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
//
// Then from repo root:
//
//	go generate ./internal/dbfork/proto/...
//
//go:generate bash -c "../../../scripts/gen-dbfork-protos.sh"
package proto
