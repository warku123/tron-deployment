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

// TestLevelDBClose_RenamesLDBToSST locks down the #164 fix: after
// Close() runs, the store dir must contain only .sst (not .ldb), and
// none of goleveldb's .bak/.old atomic-update residue. This is what
// makes a java-tron-format snapshot readable by leveldbjni after
// dbfork has touched it.
//
// We exercise the path by:
//
//  1. Opening a goleveldb DB and writing+compacting so .ldb files
//     land on disk (mirrors what mutation actually produces).
//  2. Planting a `.bak` file by hand to simulate the residue we
//     observed in real Nile snapshots on 2026-05-25.
//  3. Closing via the dbfork engine wrapper and asserting the dir
//     is in java-tron-readable shape.
func TestLevelDBClose_RenamesLDBToSST(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "account")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Step 1: write through goleveldb directly + compact to flush
	// memtable to .ldb. CompactRange(nil,nil) covers the whole store.
	seedDB, err := leveldb.OpenFile(storeDir, nil)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	for i := range 32 {
		k := []byte{byte(i)}
		v := bytes.Repeat([]byte{'x'}, 4096) // make it big enough to flush
		if err := seedDB.Put(k, v, nil); err != nil {
			t.Fatalf("seed put: %v", err)
		}
	}
	if err := seedDB.CompactRange(util.Range{}); err != nil {
		t.Fatalf("seed compact: %v", err)
	}
	if err := seedDB.Close(); err != nil {
		t.Fatalf("seed close: %v", err)
	}

	// Sanity check: the seed produced at least one .ldb file.
	if !dirHasExt(t, storeDir, ".ldb") {
		t.Fatal("seed did not produce any .ldb files; test premise broken")
	}

	// Step 2: plant a .bak residue file.
	if err := os.WriteFile(filepath.Join(storeDir, "MANIFEST-000001.bak"),
		[]byte("residue"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Step 3: open through the dbfork engine, close, assert dir state.
	eng, err := OpenLevelDB(dataDir, "account")
	if err != nil {
		t.Fatalf("OpenLevelDB: %v", err)
	}
	if err := eng.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if dirHasExt(t, storeDir, ".ldb") {
		t.Errorf("Close left .ldb files behind: %s", listExt(t, storeDir, ".ldb"))
	}
	if !dirHasExt(t, storeDir, ".sst") {
		t.Errorf("Close did not produce any .sst files")
	}
	if dirHasExt(t, storeDir, ".bak") || dirHasExt(t, storeDir, ".old") {
		t.Errorf("Close left residue: %s %s",
			listExt(t, storeDir, ".bak"), listExt(t, storeDir, ".old"))
	}
}

// TestConvertGoleveldbToSST_NoopWhenAlreadyClean is the boring case:
// running the sweep on a dir that has only .sst files is a no-op
// (does not error, does not touch files). This matters because the
// hook runs on every Close, including the read-only opens dbfork
// does for its equivalence-test fixtures.
func TestConvertGoleveldbToSST_NoopWhenAlreadyClean(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "000001.sst"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "MANIFEST-000002"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := convertGoleveldbToSST(dir); err != nil {
		t.Fatalf("noop sweep errored: %v", err)
	}
	if !fileExists(filepath.Join(dir, "000001.sst")) {
		t.Error("clean sweep deleted .sst file")
	}
	if !fileExists(filepath.Join(dir, "MANIFEST-000002")) {
		t.Error("clean sweep deleted MANIFEST")
	}
}

// dirHasExt returns true if storeDir has any file with the given
// suffix. Helper for the sweep tests.
func dirHasExt(t *testing.T, storeDir, ext string) bool {
	t.Helper()
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ext) {
			return true
		}
	}
	return false
}

func listExt(t *testing.T, storeDir, ext string) []string {
	t.Helper()
	entries, err := os.ReadDir(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ext) {
			out = append(out, e.Name())
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
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
	for _, want := range []string{".ldb/.sst", "--engine"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should mention %q so operators know what to do", err, want)
		}
	}
}

// TestDetectKind_EngineProperties pins the authoritative
// engine.properties path. java-tron's snapshot pipeline writes this
// file in every store dir with `ENGINE=LEVELDB` or `ENGINE=ROCKSDB`;
// DetectKind must read it BEFORE falling back to extension/marker
// heuristics. Without this path, a Nile snapshot with `.sst` files
// (java's iq80/leveldb naming, identical-on-wire to Go's `.ldb`)
// would incorrectly route to the RocksDB engine and the dbfork
// mutator would fail.
func TestDetectKind_EngineProperties(t *testing.T) {
	cases := []struct {
		name        string
		engineValue string
		extraFile   string
		want        EngineKind
	}{
		{
			name:        "LEVELDB authoritative",
			engineValue: "LEVELDB",
			extraFile:   "001.sst", // .sst alone would default to LevelDB now too, but engine.properties is stronger evidence
			want:        KindLevelDB,
		},
		{
			name:        "ROCKSDB authoritative",
			engineValue: "ROCKSDB",
			extraFile:   "001.sst",
			want:        KindRocksDB,
		},
		{
			name:        "case-insensitive leveldb",
			engineValue: "leveldb",
			extraFile:   "001.sst",
			want:        KindLevelDB,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			storeDir := filepath.Join(dataDir, "database", "probe")
			if err := os.MkdirAll(storeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			// Write a realistic-looking engine.properties (matches
			// java.util.Properties#store output format).
			content := "#Generated by the application.  PLEASE DO NOT EDIT!\n" +
				"#Thu Dec 05 12:58:50 CST 2019\n" +
				"ENGINE=" + tc.engineValue + "\n"
			if err := os.WriteFile(filepath.Join(storeDir, "engine.properties"),
				[]byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			// Plant the extra file (.sst etc) to verify engine.properties
			// wins over extension heuristics.
			if tc.extraFile != "" {
				if err := os.WriteFile(filepath.Join(storeDir, tc.extraFile),
					[]byte("placeholder"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			got, err := DetectKind(dataDir, "probe")
			if err != nil {
				t.Fatalf("DetectKind: %v", err)
			}
			if got != tc.want {
				t.Errorf("DetectKind = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestDetectKind_EnginePropertiesMalformed pins the parser's handling
// of pathological engine.properties content. Real java-tron writes
// 7-bit ASCII ENGINE values, but if a future release changes that
// (or a test fixture is hand-written), parser robustness matters:
//
//   - Unknown ENGINE value → typed error (not silent fallthrough).
//   - File present but no ENGINE= line → fall through to other
//     heuristics (treated as "missing", since the file conveys no
//     declaration).
//
// What we do NOT pin: line continuations (`\` at EOL) or `\uNNNN`
// escapes — see readEngineProperties' docstring for the assumption
// boundary.
func TestDetectKind_EnginePropertiesMalformed(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantFallback bool   // true: parser returns (0, false, nil) → tries other heuristics
		wantErrSub   string // non-empty: parser returns error containing this
	}{
		{
			name:       "unknown ENGINE value",
			body:       "ENGINE=POSTGRES\n",
			wantErrSub: "unrecognized ENGINE value",
		},
		{
			// Empty value (`ENGINE=` with nothing after the `=`) is
			// treated as an unknown value, not as a missing key. The
			// parser cuts on `=` and trims whitespace, leaving "".
			name:       "ENGINE= empty value",
			body:       "ENGINE=\n",
			wantErrSub: "unrecognized ENGINE value",
		},
		{
			name:         "no ENGINE= line, only comments",
			body:         "# nothing useful here\n# move along\n",
			wantFallback: true,
		},
		{
			name:         "empty file",
			body:         "",
			wantFallback: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			storeDir := filepath.Join(dataDir, "database", "probe")
			if err := os.MkdirAll(storeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(storeDir, "engine.properties"),
				[]byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := DetectKind(dataDir, "probe")
			if tc.wantErrSub != "" {
				if err == nil {
					t.Fatal("expected error for unknown ENGINE value")
				}
				if !strings.Contains(err.Error(), tc.wantErrSub) {
					t.Errorf("err = %v; want substring %q", err, tc.wantErrSub)
				}
				return
			}
			// Fallback case: no ENGINE line → engine.properties is
			// treated as missing → DetectKind falls through to other
			// heuristics. With no other files in the store dir, the
			// final fallback returns the "no engine indicators" error.
			if err == nil {
				t.Fatal("expected error: no engine indicators after fallback")
			}
			if !strings.Contains(err.Error(), ".ldb/.sst") {
				t.Errorf("err = %v; want extension-heuristic message", err)
			}
		})
	}
}

// TestDetectKind_SSTDefaultsToLevelDB pins the fallback heuristic for
// the case where engine.properties is absent (older snapshots, manually-
// created test fixtures) but `.sst` files are present. Java
// iq80/leveldb writes `.sst` files for LevelDB stores — the previous
// "sst = RocksDB" rule wrongly rejected those. Now `.sst` alone
// defaults to LevelDB unless RocksDB-specific markers (IDENTITY,
// OPTIONS-*) are also present.
func TestDetectKind_SSTDefaultsToLevelDB(t *testing.T) {
	dataDir := t.TempDir()
	storeDir := filepath.Join(dataDir, "database", "java-style")
	if err := os.MkdirAll(storeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "001.sst"),
		[]byte("placeholder"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := DetectKind(dataDir, "java-style")
	if err != nil {
		t.Fatalf("DetectKind: %v", err)
	}
	if got != KindLevelDB {
		t.Errorf("DetectKind = %v; want LevelDB (.sst alone is LevelDB without RocksDB markers)", got)
	}
}

// TestDetectKind_RocksDBMarkers pins the RocksDB-specific-marker path.
// IDENTITY and OPTIONS-NNNNNN are RocksDB-only — LevelDB writes neither.
func TestDetectKind_RocksDBMarkers(t *testing.T) {
	for _, marker := range []string{"IDENTITY", "OPTIONS-000007"} {
		t.Run(marker, func(t *testing.T) {
			dataDir := t.TempDir()
			storeDir := filepath.Join(dataDir, "database", "rocks")
			if err := os.MkdirAll(storeDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(storeDir, marker),
				[]byte("placeholder"), 0o644); err != nil {
				t.Fatal(err)
			}
			// Plant .sst as well — RocksDB marker should still win.
			if err := os.WriteFile(filepath.Join(storeDir, "001.sst"),
				[]byte("data"), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := DetectKind(dataDir, "rocks")
			if err != nil {
				t.Fatalf("DetectKind: %v", err)
			}
			if got != KindRocksDB {
				t.Errorf("DetectKind = %v; want RocksDB (marker %s present)", got, marker)
			}
		})
	}
}
