package dbfork

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

// seedLevelDBStoreUnder is the multi-store variant of seedLevelDBStore:
// drops a freshly-initialised LevelDB into <dataDir>/database/<storeName>/
// without allocating a new tempdir, so end-to-end tests can stand up
// the 3 stores Apply touches under a single root.
func seedLevelDBStoreUnder(t *testing.T, dataDir, storeName string) {
	t.Helper()
	storeDir := filepath.Join(dataDir, "database", storeName)
	if err := os.MkdirAll(filepath.Dir(storeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	ldb, err := leveldb.OpenFile(storeDir, nil)
	if err != nil {
		t.Fatalf("seed %s: %v", storeName, err)
	}
	if err := ldb.Put([]byte("__seed__"), []byte("seed"), nil); err != nil {
		t.Fatalf("seed put %s: %v", storeName, err)
	}
	if err := ldb.Close(); err != nil {
		t.Fatalf("seed close %s: %v", storeName, err)
	}
}

// compactAllStores forces every store under <dataDir>/database/ to
// flush its memtable to a .ldb SSTable on disk so DetectKind can see
// a non-empty dir. goleveldb keeps small writes in the memtable
// until either a flush threshold or an explicit CompactRange — for
// our tiny seed key we have to call CompactRange explicitly.
func compactAllStores(t *testing.T, dataDir string) {
	t.Helper()
	dbRoot := filepath.Join(dataDir, "database")
	entries, err := os.ReadDir(dbRoot)
	if err != nil {
		t.Fatalf("read %s: %v", dbRoot, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		storeDir := filepath.Join(dbRoot, e.Name())
		ldb, err := leveldb.OpenFile(storeDir, nil)
		if err != nil {
			t.Fatalf("open %s for compaction: %v", e.Name(), err)
		}
		// Force a flush by inserting a series of writes large enough
		// to trigger compaction, then explicitly compacting. Writing
		// a single tiny key with CompactRange alone doesn't always
		// flush in goleveldb — but for the tests' purposes we only
		// need ONE .ldb to exist.
		//
		// IMPORTANT: delete the throwaway keys after compaction. If
		// they survive, they linger in stores that MutateProperties
		// (and future Mutate* funcs) don't wipe — which would let
		// future test additions accidentally collide their real
		// fixtures with `[]byte{0..99}` and silently mis-assert.
		for i := range 100 {
			_ = ldb.Put([]byte{byte(i)}, make([]byte, 1024), nil)
		}
		if err := ldb.CompactRange(util.Range{}); err != nil {
			t.Fatalf("compact %s: %v", e.Name(), err)
		}
		for i := range 100 {
			_ = ldb.Delete([]byte{byte(i)}, nil)
		}
		if err := ldb.Close(); err != nil {
			t.Fatalf("close %s: %v", e.Name(), err)
		}
	}
}
