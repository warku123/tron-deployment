//go:build rocksdb

package db

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/linxGnu/grocksdb"
)

// OpenRocksDB is the cgo-backed RocksDB engine path, enabled by
// building with `-tags rocksdb`. Required for arm64 hosts (java-tron's
// Storage.java:180 forces RocksDB on arm64 regardless of config) and
// for any operator running java-tron with `storage.db.engine = ROCKSDB`.
//
// # Validation status
//
// Runtime-validated on linux/arm64 against grocksdb v1.10.8 +
// RocksDB 10.10.1 built via `make libs`. The rocksdb-tagged test
// suite (TestRocksDBEngine_RoundTrip + TestDetectKind_RocksDB)
// passes; a synthetic shadow-fork mutate against an empty
// RocksDB-flavoured data dir produces the same Result counters
// as the LevelDB path (1 witness, 1 active slate, 1 account, 3
// properties) and the on-disk state read-back matches what the
// mutation engine writes via the Batch interface. Wiring this
// into CI is still Task #163.
//
// # Build prerequisites
//
// grocksdb v1.10.8 (the pinned version in go.mod) is hard-coupled to
// RocksDB 10.10.1 — neither Ubuntu apt (6.x-8.x) nor Homebrew (11.x)
// ship a compatible version. The recommended workflow uses grocksdb's
// bundled build script which compiles RocksDB 10.10.1 + deps (snappy,
// zlib, lz4, zstd) from source as static libs:
//
//	# Build the deps (~10-15 min, one-time per machine).
//	GROCKSDB=$(go env GOMODCACHE)/github.com/linx!gnu/grocksdb@v1.10.8
//	cd "$GROCKSDB" && make libs
//
//	# Build trond with the rocksdb tag.
//	cd /path/to/tron-deployment
//	export CGO_CFLAGS="-I$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/include"
//	export CGO_LDFLAGS="-L$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/lib -lrocksdb -lstdc++ -lm -lz -lbz2 -llz4 -lzstd -lsnappy"
//	go build -tags rocksdb -o bin/trond-rocksdb .
//
// Wiring this into the project's CI + goreleaser pipelines is tracked
// under Task #162 follow-up — for now the rocksdb-tagged build is an
// operator-driven workflow, not a default release artifact.
//
// Common troubleshooting: if cgo errors with `'rocksdb/c.h' file not
// found`, the CGO_CFLAGS path is wrong (typo in $GROCKSDB, or `make
// libs` didn't produce a dist/<os>_<arch>/include dir). Verify the
// include dir exists before re-running `go build -tags rocksdb`.
//
// # On-disk layout
//
// Matches LevelDB's: java-tron writes each store as a standalone
// RocksDB instance under `<dataDir>/database/<storeName>/`. One column
// family per store dir (the default CF); no multi-CF handles.
//
// # Read/write options
//
//   - error_if_exists=false + create_if_missing=false: dbfork is a
//     MUTATION tool against an existing data dir, so we never create
//     stores implicitly. Same contract as the LevelDB path.
//   - WriteOptions defaults (sync=false) match java-tron's
//     checkpoint.sync default; WriteBatch atomicity is preserved on
//     commit regardless of fsync.
func OpenRocksDB(dataDir, storeName string) (Engine, error) {
	path := filepath.Join(dataDir, "database", storeName)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("dbfork: store %q at %s: %w", storeName, path, err)
	}
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(false)
	opts.SetErrorIfExists(false)
	db, err := grocksdb.OpenDb(opts, path)
	if err != nil {
		opts.Destroy()
		return nil, fmt.Errorf("dbfork: open rocksdb at %s: %w", path, err)
	}
	return &rocksDBEngine{
		db:    db,
		opts:  opts,
		ropts: grocksdb.NewDefaultReadOptions(),
		wopts: grocksdb.NewDefaultWriteOptions(),
		path:  path,
	}, nil
}

type rocksDBEngine struct {
	db    *grocksdb.DB
	opts  *grocksdb.Options
	ropts *grocksdb.ReadOptions
	wopts *grocksdb.WriteOptions
	path  string
}

func (e *rocksDBEngine) Get(key []byte) ([]byte, error) {
	// grocksdb.Slice owns C-allocated memory; we MUST Free it after
	// copying. The Engine interface contract is that callers can
	// retain returned slices, so we copy out into a fresh Go-managed
	// buffer immediately.
	slice, err := e.db.Get(e.ropts, key)
	if err != nil {
		return nil, fmt.Errorf("dbfork: get %s: %w", e.path, err)
	}
	defer slice.Free()
	if !slice.Exists() {
		return nil, ErrNotFound
	}
	data := slice.Data()
	out := make([]byte, len(data))
	copy(out, data)
	return out, nil
}

func (e *rocksDBEngine) NewBatch() Batch {
	return &rocksDBBatch{
		db:    e.db,
		batch: grocksdb.NewWriteBatch(),
		wopts: e.wopts,
	}
}

func (e *rocksDBEngine) NewIterator() Iterator {
	it := e.db.NewIterator(e.ropts)
	it.SeekToFirst()
	return &rocksDBIterator{it: it, started: false}
}

func (e *rocksDBEngine) Close() error {
	// Order: close the DB first so any in-flight iterators / pending
	// reads finish against valid options, THEN free the options. All
	// three handles (opts, ropts, wopts) leak C memory if not
	// explicitly Destroy()ed — grocksdb anchors them on the Go side
	// for GC, but DB.Close() does NOT call Destroy on them (verified
	// against grocksdb@v1.10.8/db.go:2063, which only calls
	// rocksdb_close + nil-out). A naive "DB consumes opts" mental
	// model from C++ RocksDB ownership doesn't apply here.
	e.db.Close()
	e.ropts.Destroy()
	e.wopts.Destroy()
	e.opts.Destroy()
	return nil
}

// --- batch -----------------------------------------------------------------

type rocksDBBatch struct {
	db    *grocksdb.DB
	batch *grocksdb.WriteBatch
	wopts *grocksdb.WriteOptions
}

func (b *rocksDBBatch) Put(key, value []byte) {
	// grocksdb's WriteBatch.Put COPIES inputs into its internal
	// buffer — same as goleveldb. No defensive copy on the Go side.
	b.batch.Put(key, value)
}

func (b *rocksDBBatch) Delete(key []byte) {
	b.batch.Delete(key)
}

func (b *rocksDBBatch) Write() error {
	if err := b.db.Write(b.wopts, b.batch); err != nil {
		return fmt.Errorf("dbfork: write batch: %w", err)
	}
	return nil
}

func (b *rocksDBBatch) Close() {
	// WriteBatch holds C memory; Destroy releases it. Safe to call
	// after a successful Write — RocksDB's contract is that the
	// batch's content is independent of its own lifetime.
	b.batch.Destroy()
}

// --- iterator --------------------------------------------------------------
//
// grocksdb.Iterator uses a SeekToFirst + Valid/Next traversal pattern,
// distinct from goleveldb's "Next() bool means advance". We adapt to
// the Engine.Next() shape: first call seeks the first key + returns
// true if valid; subsequent calls advance + return Valid().

type rocksDBIterator struct {
	it      *grocksdb.Iterator
	started bool
	closed  bool
	err     error
}

func (i *rocksDBIterator) Next() bool {
	if i.err != nil {
		return false
	}
	if !i.started {
		// SeekToFirst was called at construction; first Next() just
		// reports validity at position 0.
		i.started = true
	} else {
		i.it.Next()
	}
	if err := i.it.Err(); err != nil {
		i.err = err
		return false
	}
	return i.it.Valid()
}

func (i *rocksDBIterator) Key() []byte {
	// grocksdb.Iterator.Key() returns a *Slice whose underlying buffer
	// is owned by the iterator and overwritten on the next Next()/
	// Close() — we copy out so callers can retain across iteration
	// (the witness-erase pattern).
	//
	// Note: the Slice itself has `freed=true` set at construction (see
	// grocksdb@v1.10.8/iterator.go:65), so Slice.Free() is a no-op
	// here. The defensive-copy contract is what matters; the Slice
	// wrapper is just an addressable handle to the iterator-owned
	// buffer, not a separately-allocated chunk we'd otherwise leak.
	//
	// Post-Close guard mirrors Error()'s: after Close(), i.it.c is
	// nil and the C call would deref a nil pointer. Match goleveldb's
	// safe-after-Release contract by returning nil.
	if i.closed {
		return nil
	}
	k := i.it.Key()
	data := k.Data()
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func (i *rocksDBIterator) Value() []byte {
	// Same buffer-reuse semantics + same no-op Free() + same post-
	// Close guard as Key().
	if i.closed {
		return nil
	}
	v := i.it.Value()
	data := v.Data()
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func (i *rocksDBIterator) Error() error {
	if i.err != nil {
		return i.err
	}
	if i.closed {
		// it.Err() after Close() dereferences a nil C pointer in
		// grocksdb. The LevelDB wrapper is graceful here (goleveldb's
		// iterator.Error after Release is safe), so we mirror that
		// contract: post-Close Error() returns the last stashed err.
		return nil
	}
	if err := i.it.Err(); err != nil {
		return err
	}
	return nil
}

func (i *rocksDBIterator) Close() {
	if i.closed {
		return
	}
	// Stash any final iterator error BEFORE closing so subsequent
	// Error() calls don't try to dereference the closed handle.
	if err := i.it.Err(); err != nil && i.err == nil {
		i.err = err
	}
	i.it.Close()
	i.closed = true
}

