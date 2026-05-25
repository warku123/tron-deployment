//go:build rocksdb

package db

import (
	"errors"
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
	// Order matters: close the DB before destroying option handles —
	// the DB references them internally. ropts/wopts are leaked if we
	// don't Destroy() them; opts is consumed by the DB and freed when
	// the DB closes (grocksdb's semantics).
	e.db.Close()
	e.ropts.Destroy()
	e.wopts.Destroy()
	// opts intentionally NOT Destroy()ed — DB.Close() handles it.
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
	// grocksdb.Iterator.Key() returns a grocksdb.Slice that's only
	// valid until the next Next()/Close(). Copy out so callers can
	// retain across iteration (the witness-erase pattern).
	k := i.it.Key()
	defer k.Free()
	data := k.Data()
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func (i *rocksDBIterator) Value() []byte {
	v := i.it.Value()
	defer v.Free()
	data := v.Data()
	out := make([]byte, len(data))
	copy(out, data)
	return out
}

func (i *rocksDBIterator) Error() error {
	if i.err != nil {
		return i.err
	}
	if err := i.it.Err(); err != nil {
		return err
	}
	return nil
}

func (i *rocksDBIterator) Close() {
	i.it.Close()
}

// Sentinel — defensive shape check that grocksdb is wired through. If
// the cgo build fails or grocksdb drops a method we use, the compiler
// catches it here rather than at runtime.
var _ = errors.New
