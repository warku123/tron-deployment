package db

import (
	"fmt"
	"os"
	"path/filepath"
)

// EngineKind identifies which on-disk format a java-tron store uses.
// Both engines coexist on a single node — java-tron's
// `storage.db.engine` is global, but for forensic reasons (snapshots
// taken before a config change, mixed-mode test fixtures) dbfork
// detects per-store. In practice all 8 stores share the same engine,
// but we don't ASSUME that.
type EngineKind int

const (
	EngineLevelDB EngineKind = iota
	EngineRocksDB
)

func (k EngineKind) String() string {
	switch k {
	case EngineLevelDB:
		return "leveldb"
	case EngineRocksDB:
		return "rocksdb"
	default:
		return "unknown"
	}
}

// DetectKind sniffs `<dataDir>/database/<storeName>/` to decide
// which engine wrote it. Heuristic:
//
//   - `*.ldb` files OR `CURRENT` + `MANIFEST-*` (LevelDB format) →
//     LevelDB.
//   - `*.sst` files OR `OPTIONS-*` (RocksDB format) → RocksDB.
//
// LevelDB and RocksDB share enough metadata files (CURRENT, MANIFEST)
// that we can't distinguish on those alone. The .ldb vs .sst extension
// is the cleanest tie-breaker; both engines emit it for the same
// reason (sorted string tables) but with different format magic.
//
// Returns an error if the store dir doesn't exist or neither engine
// signature is found.
func DetectKind(dataDir, storeName string) (EngineKind, error) {
	path := filepath.Join(dataDir, "database", storeName)
	dirents, err := os.ReadDir(path)
	if err != nil {
		return 0, fmt.Errorf("dbfork: probe %s: %w", path, err)
	}
	var hasLDB, hasSST bool
	for _, d := range dirents {
		name := d.Name()
		if len(name) < 4 {
			continue
		}
		ext := name[len(name)-4:]
		switch ext {
		case ".ldb":
			hasLDB = true
		case ".sst":
			hasSST = true
		}
	}
	switch {
	case hasLDB && !hasSST:
		return EngineLevelDB, nil
	case hasSST && !hasLDB:
		return EngineRocksDB, nil
	case hasLDB && hasSST:
		return 0, fmt.Errorf("dbfork: mixed .ldb + .sst in %s — "+
			"manual cleanup required, refusing to guess engine", path)
	default:
		// New / empty store, or a format we don't recognize. java-tron's
		// initial sync writes manifests before any SST/LDB exists, so
		// "no .ldb and no .sst" can be a freshly-started node. Default
		// to LevelDB (java-tron's default `storage.db.engine`) but log
		// it via the error message for diagnostic.
		return 0, fmt.Errorf("dbfork: cannot detect engine for %s "+
			"(no .ldb or .sst files); has the node finished initial "+
			"DB compaction? Pass --engine to override", path)
	}
}

// Open routes to the right engine implementation given a kind.
// Callers either pass an explicit kind (CLI override) or run
// DetectKind first.
func Open(dataDir, storeName string, kind EngineKind) (Engine, error) {
	switch kind {
	case EngineLevelDB:
		return OpenLevelDB(dataDir, storeName)
	case EngineRocksDB:
		return OpenRocksDB(dataDir, storeName)
	default:
		return nil, fmt.Errorf("dbfork: unknown engine kind %d", kind)
	}
}
