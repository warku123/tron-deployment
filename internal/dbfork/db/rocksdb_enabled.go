//go:build rocksdb

package db

import "fmt"

// OpenRocksDB is the cgo-backed RocksDB engine path, enabled by
// building with `-tags rocksdb`. Real implementation lands when
// we have a concrete user need (TODO: wire github.com/linxGnu/grocksdb).
//
// The build tag is reserved NOW so callers don't have to refactor
// later — dbfork's apply path can already dispatch on engine type,
// and the rocksdb-tagged build will fail to compile (not silently
// produce wrong output) until the impl is in.
//
// Implementation plan:
//
//  1. Add `github.com/linxGnu/grocksdb` to go.mod (cgo-only).
//  2. Mirror leveldb.go's surface: Engine, Batch, Iterator wrappers.
//  3. java-tron's RocksDB stores have the same on-disk layout
//     (`<data-dir>/database/<store>/`) — only the file format
//     differs. No path-resolution changes needed.
//  4. Wire CI to build both `-tags ""` and `-tags rocksdb` for
//     coverage parity.
//  5. Add an equivalence test that runs the same fork.conf against
//     a LevelDB data dir and a RocksDB data dir, verifying both
//     produce the same logical state.
func OpenRocksDB(dataDir, storeName string) (Engine, error) {
	return nil, fmt.Errorf(
		"dbfork: rocksdb engine compiled in via -tags rocksdb but "+
			"the implementation is not yet wired (TODO). "+
			"data dir %q store %q. Track via task #146 follow-up.",
		dataDir, storeName,
	)
}
