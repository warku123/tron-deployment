package db

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// TestLevelDBEngine_RoundTrip is the smoke gate for the LevelDB
// backend: open a fresh DB in the java-tron layout
// (`<root>/database/<store>/`), Put 3 keys via a batch, read them
// back via Get, iterate over them, delete one, verify it's gone.
//
// Covers the four primitives dbfork's mutation code actually uses
// (Get / Batch.Put / Batch.Delete / Iterator) so a regression in
// the engine layer surfaces here before it pollutes the higher-
// level witness/account/TRC20 tests.
func TestLevelDBEngine_RoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "witness")
	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// goleveldb creates the store dir itself if it's missing AND
	// ErrorIfMissing is false — but OpenLevelDB sets ErrorIfMissing
	// true (it's mutating an existing DB, not creating one), so we
	// pre-create with a single Put to make the DB valid.
	db, err := leveldb.OpenFile(storeDir, nil)
	if err != nil {
		t.Fatalf("seed leveldb: %v", err)
	}
	if err := db.Put([]byte("seed-key"), []byte("seed-val"), nil); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	// Open via dbfork's wrapper — this is the surface mutation
	// code (Task #147+) will use.
	eng, err := OpenLevelDB(dataDir, "witness")
	if err != nil {
		t.Fatalf("OpenLevelDB: %v", err)
	}
	defer eng.Close()

	t.Run("Get round-trip", func(t *testing.T) {
		v, err := eng.Get([]byte("seed-key"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !bytes.Equal(v, []byte("seed-val")) {
			t.Errorf("Get returned %q; want 'seed-val'", v)
		}
	})

	t.Run("Get missing returns ErrNotFound", func(t *testing.T) {
		_, err := eng.Get([]byte("definitely-not-here"))
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Get on absent key returned %v; want ErrNotFound", err)
		}
	})

	t.Run("Batch Put + Delete atomic", func(t *testing.T) {
		b := eng.NewBatch()
		defer b.Close()
		b.Put([]byte("k1"), []byte("v1"))
		b.Put([]byte("k2"), []byte("v2"))
		b.Put([]byte("k3"), []byte("v3"))
		b.Delete([]byte("seed-key"))
		if err := b.Write(); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// k1/k2/k3 should now exist; seed-key gone.
		if v, err := eng.Get([]byte("k1")); err != nil || string(v) != "v1" {
			t.Errorf("k1 after batch: %q err=%v", v, err)
		}
		if _, err := eng.Get([]byte("seed-key")); !errors.Is(err, ErrNotFound) {
			t.Errorf("seed-key should be deleted; got err=%v", err)
		}
	})

	t.Run("Iterator walks all keys", func(t *testing.T) {
		it := eng.NewIterator()
		defer it.Close()
		seen := map[string]string{}
		for it.Next() {
			seen[string(it.Key())] = string(it.Value())
		}
		if err := it.Error(); err != nil {
			t.Fatalf("Iterator error: %v", err)
		}
		// After the previous subtest: k1/k2/k3 present, seed-key gone.
		want := map[string]string{"k1": "v1", "k2": "v2", "k3": "v3"}
		for k, v := range want {
			if seen[k] != v {
				t.Errorf("iterator missed %s=%s (got %q)", k, v, seen[k])
			}
		}
		if _, ok := seen["seed-key"]; ok {
			t.Error("iterator returned deleted seed-key")
		}
	})

	t.Run("iterator returns defensive copies (key/value safe across Next)", func(t *testing.T) {
		// This is the buffer-reuse hazard that actually exists in
		// goleveldb: Iterator.Key/Value MAY share an internal buffer
		// that gets overwritten on the next Next() call. The wrapper
		// copies on the way out so callers retaining keys across
		// iteration (dbfork's witness-erase pattern: walk + collect
		// + delete) don't see corruption.
		//
		// DB.Get on the other hand returns a freshly-allocated slice
		// per goleveldb's godoc, so it doesn't need this protection
		// and we removed the redundant copy there.
		//
		// The test must actually expose the hazard: if the wrapper
		// DIDN'T copy, every retainedKeys[i] would alias the SAME
		// internal buffer ending up at the last iteration's key, so
		// retainedKeys[0] would byte-equal retainedKeys[N-1]. So we
		// SEED two distinct keys here in this subtest (independent
		// of what the earlier subtests left in the DB) so the
		// first-vs-last check has guaranteed-distinct ground truth.
		b := eng.NewBatch()
		b.Put([]byte("iter-A"), []byte("alpha"))
		b.Put([]byte("iter-Z"), []byte("omega"))
		if err := b.Write(); err != nil {
			t.Fatalf("seed for iterator test: %v", err)
		}
		b.Close()

		it := eng.NewIterator()
		defer it.Close()
		var retainedKeys [][]byte
		var retainedVals [][]byte
		for it.Next() {
			retainedKeys = append(retainedKeys, it.Key())
			retainedVals = append(retainedVals, it.Value())
		}
		if len(retainedKeys) < 2 {
			t.Fatal("expected at least 2 entries to differentiate buffers")
		}

		// The actual buffer-reuse detector: first and last retained
		// keys MUST differ. Under shared-buffer aliasing they'd be
		// the same memory holding the last key.
		first := retainedKeys[0]
		last := retainedKeys[len(retainedKeys)-1]
		if bytes.Equal(first, last) {
			t.Errorf("retainedKeys[0]=%q == retainedKeys[%d]=%q — iterator buffer shared?",
				first, len(retainedKeys)-1, last)
		}
		// Same check for values.
		firstVal := retainedVals[0]
		lastVal := retainedVals[len(retainedVals)-1]
		if bytes.Equal(firstVal, lastVal) {
			t.Errorf("retainedVals[0]=%q == retainedVals[%d]=%q — iterator buffer shared?",
				firstVal, len(retainedVals)-1, lastVal)
		}

		// Sanity: every retained pair still resolves to itself
		// via Get — catches the case where the wrapper copies but
		// returns garbage bytes.
		for i, k := range retainedKeys {
			got, err := eng.Get(k)
			if err != nil {
				t.Errorf("retainedKeys[%d]=%x not findable after iteration: %v", i, k, err)
				continue
			}
			if !bytes.Equal(got, retainedVals[i]) {
				t.Errorf("retainedVals[%d] = %q; Get returned %q",
					i, retainedVals[i], got)
			}
		}
	})
}

// TestDetectKind_LevelDB pins the engine sniffer's ability to
// classify a real goleveldb-written store. RocksDB detection is
// gated until the rocksdb build path lands; the dual-extension and
// empty-store error paths are exercised against synthetic dirs.
func TestDetectKind_LevelDB(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "account")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Plant a .ldb file by writing into the store and forcing a
	// compaction (which flushes memtables to SSTables on disk).
	// Without the explicit CompactRange, small writes stay in the
	// memtable and no .ldb file appears on disk — DetectKind then
	// sees an empty dir and errors.
	db, err := leveldb.OpenFile(storeDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		_ = db.Put([]byte{byte(i)}, make([]byte, 1024), nil)
	}
	if err := db.CompactRange(util.Range{}); err != nil {
		t.Fatalf("compact: %v", err)
	}
	_ = db.Close()

	kind, err := DetectKind(dataDir, "account")
	if err != nil {
		t.Fatalf("DetectKind: %v", err)
	}
	if kind != KindLevelDB {
		t.Errorf("DetectKind = %v; want %v", kind, KindLevelDB)
	}
}

// TestDetectKind_Empty: a store dir with no SST/LDB files yet (a
// freshly-initialised node mid-compaction) yields a diagnostic
// error rather than a misleading default. Pin the actionable hint
// in the error text so a future refactor that drops it fails this
// test rather than silently degrading the operator experience.
func TestDetectKind_Empty(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "empty-store")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := DetectKind(dataDir, "empty-store")
	if err == nil {
		t.Fatal("DetectKind on empty store should error")
	}
	for _, want := range []string{"no .ldb or .sst", "--engine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q so operators know what to do", err, want)
		}
	}
}
