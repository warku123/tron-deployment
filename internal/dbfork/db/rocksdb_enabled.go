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
// Storage.java:180 force-switches to RocksDB on arm64 regardless of
// `storage.db.engine` config).
//
// # Version pinning — same RocksDB major as java-tron arm64
//
// grocksdb is pinned to v1.9.7, which wraps RocksDB 9.7.3. This
// matches java-tron 4.8.1's arm64 RocksDB 9.7.4 (per build.gradle's
// `isArm64 ? '9.7.4' : '5.15.10'`) so dbfork's MANIFEST writes use
// VersionEdit tags the running java-tron's rocksdbjni can parse.
//
// Don't bump grocksdb past v1.9.x without first checking what java-
// tron's arm64 rocksdbjni line is pinned to. Cross-major drift
// surfaces as `RocksDBException: VersionEdit: unknown tag` at
// java-tron's AccountStore init — see Task #166 for the empirical
// trace (we got bitten by this in May 2026 when v1.10.8 + RocksDB
// 10.10.1 was the default and amd64 java-tron rejected the snapshot).
//
// # AMD64 caveat — dbfork's RocksDB path is not supported there
//
// java-tron 4.8.1 amd64 pins to RocksDB 5.15.10 (2018). No tagged
// grocksdb release wraps RocksDB 5.x — the oldest tag (v1.6.48) is
// already RocksDB 6.29.3. There is no practical Go binding to RocksDB
// 5.15.10, so dbfork cannot produce an amd64-compatible mutated
// RocksDB snapshot without a custom binding or a java-tron upstream
// version bump.
//
// In practice this is a thin gap: amd64 java-tron defaults to LevelDB,
// so an amd64 operator using `storage.db.engine = ROCKSDB` is doing
// something unusual on purpose. amd64 operators should use the
// default LevelDB build of trond. The arm64 path is the production
// use case for this engine.
//
// # Validation status
//
// Engine-correct on linux/arm64 via the rocksdb-tagged test suite
// (TestRocksDBEngine_RoundTrip + TestDetectKind_RocksDB) and a
// synthetic shadow-fork mutate against an empty store — counters
// matched the LevelDB path and on-disk bytes read back correctly
// (last run May 25 2026 with the PRIOR grocksdb v1.10.8 pin).
//
// NOT YET RUNTIME-REVALIDATED against the current v1.9.7 (RocksDB
// 9.7.3) pin on arm64 hardware. The wrapper code is engine-version-
// agnostic (only the linked librocksdb changes), but a follow-up
// arm64 e2e against real java-tron 4.8.1 is required before the
// path can be declared release-ready (Task #166 closeout). The
// May 26 2026 e2e attempt on amd64 EC2 hit the version-mismatch
// crash this pin is meant to fix.
//
// # Build prerequisites
//
// grocksdb v1.9.7 (the pinned version in go.mod) is coupled to RocksDB
// 9.7.3 — neither Ubuntu apt nor Homebrew ship a matching version.
// The recommended workflow uses grocksdb's bundled build script which
// compiles RocksDB 9.7.3 + deps (snappy, zlib, lz4, zstd) from source
// as static libs:
//
//	# Build the deps (~10-15 min, one-time per machine).
//	GROCKSDB=$(go env GOMODCACHE)/github.com/linx!gnu/grocksdb@v1.9.7
//	cd "$GROCKSDB" && make libs
//
//	# Build trond with the rocksdb tag.
//	cd /path/to/tron-deployment
//	export CGO_CFLAGS="-I$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/include"
//	export CGO_LDFLAGS="-L$GROCKSDB/dist/$(go env GOOS)_$(go env GOARCH)/lib -lrocksdb -lstdc++ -lm -lz -lbz2 -llz4 -lzstd -lsnappy"
//	go build -tags rocksdb -o bin/trond-rocksdb .
//
// Wiring this into the project's CI + goreleaser pipelines is tracked
// under Task #163 — for now the rocksdb-tagged build is an operator-
// driven workflow, not a default release artifact.
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

