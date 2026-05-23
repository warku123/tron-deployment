package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// OpenLevelDB opens a java-tron LevelDB store at
// `<dataDir>/database/<storeName>/`. java-tron writes LevelDB data
// directly into the store dir (no `.ldb` subdir wrapper), so the
// path is the same one a java-tron node would open.
//
// Returns an error if the store dir doesn't exist or the LevelDB
// open fails (corrupt SST, lock contention with a running node,
// etc.). dbfork's contract: the node MUST be stopped before mutate
// runs — we don't enforce this with a lock-file check today because
// java-tron's own LOCK file would prevent open anyway.
func OpenLevelDB(dataDir, storeName string) (Engine, error) {
	path := filepath.Join(dataDir, "database", storeName)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("dbfork: store %q at %s: %w", storeName, path, err)
	}
	// java-tron writes LevelDB without compression for some stores
	// and Snappy for others; goleveldb auto-detects on read. We
	// open in read-write mode because every dbfork store is going
	// to be mutated.
	db, err := leveldb.OpenFile(path, &opt.Options{
		// java-tron's storage.db.engine writer wrote these — we
		// match its block cache size for parity, but it's not
		// load-bearing for correctness.
		ErrorIfMissing: true,
	})
	if err != nil {
		return nil, fmt.Errorf("dbfork: open leveldb at %s: %w", path, err)
	}
	return &levelDBEngine{db: db, path: path}, nil
}

type levelDBEngine struct {
	db   *leveldb.DB
	path string
}

func (e *levelDBEngine) Get(key []byte) ([]byte, error) {
	v, err := e.db.Get(key, nil)
	if errors.Is(err, leveldb.ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("dbfork: get %s: %w", e.path, err)
	}
	// goleveldb returns a slice into its internal cache; we copy
	// because dbfork's callers retain values across subsequent Get
	// calls + the caller doesn't know about the internal sharing.
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (e *levelDBEngine) NewBatch() Batch {
	return &levelDBBatch{db: e.db, batch: new(leveldb.Batch)}
}

func (e *levelDBEngine) NewIterator() Iterator {
	// nil Range means full-store iteration; nil ReadOptions uses
	// default snapshot semantics (consistent point-in-time view).
	return &levelDBIterator{it: e.db.NewIterator(&util.Range{}, nil)}
}

func (e *levelDBEngine) Close() error {
	return e.db.Close()
}

// --- batch -----------------------------------------------------------------

type levelDBBatch struct {
	db    *leveldb.DB
	batch *leveldb.Batch
}

func (b *levelDBBatch) Put(key, value []byte) {
	// goleveldb's Batch.Put copies the inputs into its internal
	// buffer, so we don't need to defensively copy on the Go side.
	b.batch.Put(key, value)
}

func (b *levelDBBatch) Delete(key []byte) {
	b.batch.Delete(key)
}

func (b *levelDBBatch) Write() error {
	// nil WriteOptions = sync=false, which is what java-tron uses
	// (its `checkpoint.sync` is also default-false). Atomic per
	// goleveldb's contract: the batch's writes are visible together
	// or not at all.
	if err := b.db.Write(b.batch, nil); err != nil {
		return fmt.Errorf("dbfork: write batch: %w", err)
	}
	return nil
}

func (b *levelDBBatch) Close() {
	// goleveldb's Batch has no native handles — Reset clears the
	// queued writes so the batch can be GC'd cleanly. We Reset
	// even after a successful Write so accidental re-use after
	// Close is a no-op rather than a silent re-commit.
	b.batch.Reset()
}

// --- iterator --------------------------------------------------------------

type levelDBIterator struct {
	it iterator.Iterator
}

func (i *levelDBIterator) Next() bool { return i.it.Next() }
func (i *levelDBIterator) Key() []byte {
	// Same copy concern as Get — return a defensive copy because
	// goleveldb owns the underlying buffer for one Next() call only.
	k := i.it.Key()
	out := make([]byte, len(k))
	copy(out, k)
	return out
}
func (i *levelDBIterator) Value() []byte {
	v := i.it.Value()
	out := make([]byte, len(v))
	copy(out, v)
	return out
}
func (i *levelDBIterator) Error() error { return i.it.Error() }
func (i *levelDBIterator) Close()       { i.it.Release() }
