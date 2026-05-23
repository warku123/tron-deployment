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
	KindLevelDB EngineKind = iota
	KindRocksDB
)

func (k EngineKind) String() string {
	switch k {
	case KindLevelDB:
		return "leveldb"
	case KindRocksDB:
		return "rocksdb"
	default:
		return "unknown"
	}
}

// DetectKind sniffs `<dataDir>/database/<storeName>/` to decide
// which engine wrote it. The heuristic is the SSTable file
// extension — `.ldb` for LevelDB, `.sst` for RocksDB — both engines
// share the auxiliary metadata file naming (CURRENT, MANIFEST-*,
// LOG, etc.) so the extension is the only cheap discriminator
// without parsing format magic bytes.
//
// Limitations:
//
//   - A freshly-initialised node before its first compaction has
//     neither extension on disk yet. DetectKind errors with a
//     "no .ldb or .sst" message — operators bypass with an explicit
//     `--engine` flag at the CLI.
//   - Mixed `.ldb` + `.sst` in the same dir suggests manual
//     intervention or an aborted engine migration; DetectKind
//     refuses to guess.
func DetectKind(dataDir, storeName string) (EngineKind, error) {
	path := filepath.Join(dataDir, "database", storeName)
	dirents, err := os.ReadDir(path)
	if err != nil {
		return 0, fmt.Errorf("dbfork: probe %s: %w", path, err)
	}
	var hasLDB, hasSST bool
	for _, d := range dirents {
		// filepath.Ext returns "" for files without a dot, so the
		// short-name check we used to do explicitly isn't needed.
		switch filepath.Ext(d.Name()) {
		case ".ldb":
			hasLDB = true
		case ".sst":
			hasSST = true
		}
	}
	switch {
	case hasLDB && !hasSST:
		return KindLevelDB, nil
	case hasSST && !hasLDB:
		return KindRocksDB, nil
	case hasLDB && hasSST:
		return 0, fmt.Errorf("dbfork: mixed .ldb + .sst in %s — "+
			"manual cleanup required, refusing to guess engine", path)
	default:
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
	case KindLevelDB:
		return OpenLevelDB(dataDir, storeName)
	case KindRocksDB:
		return OpenRocksDB(dataDir, storeName)
	default:
		return nil, fmt.Errorf("dbfork: unknown engine kind %d", kind)
	}
}
