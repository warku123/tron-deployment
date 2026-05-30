// Package db abstracts the on-disk key/value engine dbfork operates
// against. java-tron supports both LevelDB (default) and RocksDB
// (opt-in via `storage.db.engine = "ROCKSDB"`); dbfork must read
// whichever the operator's data-dir was created with.
//
// Two implementations:
//
//   - leveldb.go              always-on (default build, pure Go via
//     syndtr/goleveldb)
//   - rocksdb_disabled.go     //go:build !rocksdb — returns a friendly
//     "rebuild with -tags rocksdb" error
//   - rocksdb_enabled.go      //go:build rocksdb — cgo wrapper around
//     linxGnu/grocksdb v1.9.7 (RocksDB 9.7.3; pinned to match
//     java-tron arm64's rocksdbjni 9.7.4 — see #166)
//
// Build matrix:
//
//	go build ./...                  → LevelDB only, pure Go, single static binary
//	go build -tags rocksdb ./...    → + RocksDB via cgo (grocksdb)
//
// The rocksdb build needs a specifically-versioned librocksdb that
// neither apt nor brew currently ships — see rocksdb_enabled.go's
// package doc for the full `make libs` + CGO_* recipe and the
// validation status note. CI integration is Task #163.
//
// The `Engine` interface keeps mutation code (witnesses.go,
// accounts.go, …) backend-agnostic. Both engines expose the same
// surface: Get / NewBatch / NewIterator / Close.
package db

import "errors"

// ErrNotFound is the canonical "key absent" sentinel. Engines wrap
// their backend-specific equivalents (leveldb's ErrNotFound,
// rocksdb's not-found semantics) so callers can use errors.Is.
var ErrNotFound = errors.New("dbfork: key not found")

// Engine is one open store (e.g. <data-dir>/database/witness/).
// Callers obtain an Engine via OpenLevelDB or OpenRocksDB; the
// mutation code never branches on engine type.
//
// Lifecycle: each Engine maps to a single underlying DB instance.
// Close() releases the LevelDB/RocksDB handles. Batches obtained
// from this Engine MUST be closed before the Engine itself, or the
// backend leaks resources.
//
// Thread-safety: NOT safe for concurrent Get + Batch.Write from
// the same Engine across goroutines. dbfork's mutation flow is
// single-threaded per store, so this restriction is acceptable.
type Engine interface {
	// Get fetches a value by key. Returns ErrNotFound if absent;
	// any other error indicates an engine-level problem.
	Get(key []byte) ([]byte, error)

	// NewBatch returns a fresh write batch. Atomicity guarantee:
	// every Put/Delete in the batch is applied together or not at
	// all — the engine takes a write lock for the duration of
	// Write(). Callers MUST call Close() on the batch when done
	// (handles native resources in the cgo rocksdb path).
	NewBatch() Batch

	// NewIterator returns a cursor over the entire store. Used by
	// dbfork's witness-erase path (delete every key in witness/).
	// Iterators MUST be closed.
	NewIterator() Iterator

	// Close releases the underlying DB handle. After Close, any
	// outstanding Batch or Iterator is invalid.
	Close() error
}

// Batch accumulates writes to commit atomically via Write().
// Mirrors java-tron's WriteBatch pattern; both LevelDB and RocksDB
// expose this primitive natively.
type Batch interface {
	// Put queues a (key, value) write. The value is copied — the
	// caller may mutate the buffer after Put returns.
	Put(key, value []byte)

	// Delete queues a key deletion. Idempotent (deleting a missing
	// key is not an error).
	Delete(key []byte)

	// Write commits the batch atomically. Returns any engine-level
	// error encountered during the commit. The batch is invalid
	// after Write whether or not it succeeds.
	Write() error

	// Close releases batch resources. MUST be called even if Write
	// errored (the engine may have native cgo handles to free).
	Close()
}

// Iterator walks every key in a store in the engine's natural sort
// order (LevelDB + RocksDB both sort lexicographically by raw bytes,
// so dbfork callers can treat the order as stable).
//
// Standard usage:
//
//	it := eng.NewIterator()
//	defer it.Close()
//	for it.Next() {
//	    key := it.Key()    // valid until next Next()/Close()
//	    val := it.Value()  // ditto
//	    // ... act on (key, val)
//	}
//	if err := it.Error(); err != nil { ... }
//
// Key/Value buffers are owned by the iterator; copy them if they
// need to outlive the Next() call.
type Iterator interface {
	Next() bool
	Key() []byte
	Value() []byte
	Error() error
	Close()
}
