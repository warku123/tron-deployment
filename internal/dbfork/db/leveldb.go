package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/iterator"
	"github.com/syndtr/goleveldb/leveldb/opt"
)

// OpenLevelDB opens a java-tron LevelDB store at
// `<dataDir>/database/<storeName>/`. java-tron writes LevelDB data
// directly into the store dir (no `.ldb` subdir wrapper), so the
// path is the same one a java-tron node would open.
//
// `ErrorIfMissing: true` is intentional: dbfork is a MUTATION tool
// against an existing data dir. It must NEVER create a new store
// implicitly — if the store is missing, that's a sign the operator
// pointed us at the wrong directory and we should fail loud rather
// than fork a fresh empty chain by accident.
//
// Returns an error if the store dir doesn't exist or the LevelDB
// open fails (corrupt SST, lock contention with a running node,
// etc.). dbfork's contract: the node MUST be stopped before mutate
// runs — we don't enforce this with a lock-file check because
// java-tron's own LOCK file would prevent open anyway, surfacing as
// "resource temporarily unavailable" from the line below.
func OpenLevelDB(dataDir, storeName string) (Engine, error) {
	path := filepath.Join(dataDir, "database", storeName)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("dbfork: store %q at %s: %w", storeName, path, err)
	}
	// java-tron writes LevelDB without compression for some stores
	// and Snappy for others; goleveldb auto-detects on read. We
	// open in read-write mode because every dbfork store is going
	// to be mutated.
	db, err := leveldb.OpenFile(path, &opt.Options{ErrorIfMissing: true})
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
	// goleveldb's DB.Get already returns a freshly-allocated slice
	// (per its godoc: "The returned slice is its own copy"), so we
	// can hand it straight back. The defensive-copy contract still
	// holds — it's just goleveldb that's doing the copy, not us.
	// Iterator's Key/Value DO need a defensive copy though; see
	// levelDBIterator below.
	return v, nil
}

func (e *levelDBEngine) NewBatch() Batch {
	return &levelDBBatch{db: e.db, batch: new(leveldb.Batch)}
}

func (e *levelDBEngine) NewIterator() Iterator {
	// nil Range = full-store iteration; nil ReadOptions = default
	// snapshot semantics (consistent point-in-time view).
	return &levelDBIterator{it: e.db.NewIterator(nil, nil)}
}

func (e *levelDBEngine) Close() error {
	if err := e.db.Close(); err != nil {
		return fmt.Errorf("dbfork: close leveldb %s: %w", e.path, err)
	}
	// java-tron 4.8.x reads SST files via `org.fusesource.leveldbjni`
	// 1.8 (and the tronprotocol fork `io.github.tronprotocol:leveldbjni
	// -all:1.18.2`); both expect the `.sst` extension that native
	// LevelDB used pre-2013. syndtr/goleveldb writes `.ldb` natively
	// (the post-2013 native convention) and may rename .sst → .ldb
	// during compaction-on-open. The SST file content is byte-identical
	// across the two extensions — only the directory entry differs —
	// so renaming the residue back to `.sst` produces a store java-tron
	// can read. Also drop `.bak`/`.old` files goleveldb leaves behind
	// from its atomic-update flow; java-tron's MANIFEST walk ignores
	// them, but they confuse human inspection. See Task #164.
	if err := convertGoleveldbToSST(e.path); err != nil {
		return fmt.Errorf("dbfork: post-close ldb→sst sweep for %s: %w", e.path, err)
	}
	return nil
}

// convertGoleveldbToSST renames every `*.ldb` to `*.sst` and removes
// `*.bak`/`*.old` in storeDir. The sweep is bounded to the directory's
// immediate children (LevelDB doesn't nest); a single readdir is enough.
func convertGoleveldbToSST(storeDir string) error {
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		return err
	}
	for _, ent := range entries {
		name := ent.Name()
		switch {
		case strings.HasSuffix(name, ".ldb"):
			old := filepath.Join(storeDir, name)
			renamed := filepath.Join(storeDir, strings.TrimSuffix(name, ".ldb")+".sst")
			if err := os.Rename(old, renamed); err != nil {
				return fmt.Errorf("rename %s -> %s: %w", old, renamed, err)
			}
		case strings.HasSuffix(name, ".bak"), strings.HasSuffix(name, ".old"):
			if err := os.Remove(filepath.Join(storeDir, name)); err != nil {
				return fmt.Errorf("remove %s: %w", name, err)
			}
		}
	}
	return nil
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
	// Unlike DB.Get (which returns a freshly-allocated slice),
	// goleveldb's iterator Key/Value MAY return slices into its
	// internal buffer, valid only until the next Next()/Close().
	// We defensively copy so callers can retain a key across
	// further iteration — the standard pattern for dbfork's
	// witness-erase path (collect all keys, then delete them).
	k := i.it.Key()
	out := make([]byte, len(k))
	copy(out, k)
	return out
}
func (i *levelDBIterator) Value() []byte {
	// Same buffer-reuse hazard as Key — copy.
	v := i.it.Value()
	out := make([]byte, len(v))
	copy(out, v)
	return out
}
func (i *levelDBIterator) Error() error { return i.it.Error() }
func (i *levelDBIterator) Close()       { i.it.Release() }
