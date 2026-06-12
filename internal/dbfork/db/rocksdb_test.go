//go:build rocksdb

package db

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/linxGnu/grocksdb"
)

// TestRocksDBEngine_RoundTrip mirrors TestLevelDBEngine_RoundTrip from
// leveldb_test.go against the RocksDB engine. Exercises the four
// primitives dbfork's mutation code actually uses (Get, Batch.Put,
// Batch.Delete, Iterator) so a regression in the cgo wrapper surfaces
// here before it bleeds into the higher-level account/witness/TRC20
// tests.
//
// Compile requirements: librocksdb at the version grocksdb's bundled
// build.sh pins (v1.10.8 → rocksdb 10.10.1 at time of writing). See
// `internal/dbfork/db/rocksdb_enabled.go` package doc + the build
// instructions section in knowledge/shadow-fork-poc.md.
//
// Why a separate test file vs the LevelDB one: cgo's link step pulls
// the entire grocksdb library, which is gated behind the `rocksdb`
// build tag. Without the tag, this file is excluded and the default
// `go test ./...` flow stays cgo-free.
func TestRocksDBEngine_RoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "witness")
	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	// grocksdb won't create the store dir itself when our OpenRocksDB
	// passes CreateIfMissing=false. Seed a valid (one-key) DB first so
	// the path resolves to a real RocksDB instance.
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	seedDB, err := grocksdb.OpenDb(opts, storeDir)
	if err != nil {
		t.Fatalf("seed rocksdb: %v", err)
	}
	wopts := grocksdb.NewDefaultWriteOptions()
	if err := seedDB.Put(wopts, []byte("seed-key"), []byte("seed-val")); err != nil {
		t.Fatalf("seed put: %v", err)
	}
	wopts.Destroy()
	seedDB.Close()
	opts.Destroy()

	// Now open via dbfork's wrapper.
	eng, err := OpenRocksDB(dataDir, "witness")
	if err != nil {
		t.Fatalf("OpenRocksDB: %v", err)
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
		// Same buffer-reuse hazard as LevelDB: grocksdb's iterator
		// Key/Value return slices backed by C memory that's freed on
		// the next advance. Our wrapper defensively copies; without
		// it, retainedKeys[0] would alias retainedKeys[N-1] (same
		// internal buffer holding the last key seen).
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
		// Distinct seed keys (iter-A vs iter-Z) → distinct first/last
		// retained keys. Shared-buffer aliasing would collapse them.
		first := retainedKeys[0]
		last := retainedKeys[len(retainedKeys)-1]
		if bytes.Equal(first, last) {
			t.Errorf("retainedKeys[0]=%q == retainedKeys[%d]=%q — iterator buffer shared?",
				first, len(retainedKeys)-1, last)
		}
		firstVal := retainedVals[0]
		lastVal := retainedVals[len(retainedVals)-1]
		if bytes.Equal(firstVal, lastVal) {
			t.Errorf("retainedVals[0]=%q == retainedVals[%d]=%q — iterator buffer shared?",
				firstVal, len(retainedVals)-1, lastVal)
		}

		// Round-trip: each retained pair still resolves via Get.
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

// TestDetectKind_RocksDB pins that DetectKind classifies a real
// grocksdb-written store as KindRocksDB via the IDENTITY marker file
// that RocksDB writes on first open. Complements
// TestDetectKind_LevelDB which runs on every build.
func TestDetectKind_RocksDB(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "account")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	opts := grocksdb.NewDefaultOptions()
	opts.SetCreateIfMissing(true)
	defer opts.Destroy()
	db, err := grocksdb.OpenDb(opts, storeDir)
	if err != nil {
		t.Fatal(err)
	}
	// Force a real flush so the on-disk layout includes IDENTITY +
	// SST files (a fresh RocksDB open writes IDENTITY immediately
	// though, so technically we don't even need writes for DetectKind).
	wopts := grocksdb.NewDefaultWriteOptions()
	for i := byte(0); i < 100; i++ {
		_ = db.Put(wopts, []byte{i}, make([]byte, 1024))
	}
	fopts := grocksdb.NewDefaultFlushOptions()
	if err := db.Flush(fopts); err != nil {
		t.Fatalf("flush: %v", err)
	}
	fopts.Destroy()
	wopts.Destroy()
	db.Close()

	kind, err := DetectKind(dataDir, "account")
	if err != nil {
		t.Fatalf("DetectKind: %v", err)
	}
	if kind != KindRocksDB {
		t.Errorf("DetectKind = %v; want %v (IDENTITY marker present)", kind, KindRocksDB)
	}
}
