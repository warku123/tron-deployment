//go:build !rocksdb

package db

import "fmt"

// OpenRocksDB returns a clear error when trond was built without the
// `rocksdb` build tag. Operators with java-tron data dirs that use
// `storage.db.engine = "ROCKSDB"` must rebuild trond as:
//
//	go build -tags rocksdb ./...
//
// (Or download the pre-built `trond-with-rocksdb` release binary —
// CI ships both light and full variants. See proto/README.md.)
//
// We expose this stub at compile-time so dbfork's apply path can
// dispatch on engine type without conditional compilation in every
// call site — the engine selector just calls OpenRocksDB and the
// stub returns this error if cgo wasn't enabled at build time.
func OpenRocksDB(dataDir, storeName string) (Engine, error) {
	return nil, fmt.Errorf(
		"dbfork: rocksdb support not compiled into this trond binary "+
			"(data dir at %q store %q uses RocksDB; rebuild with "+
			"`go build -tags rocksdb` or use a release artifact whose "+
			"name includes `-rocksdb`)",
		dataDir, storeName,
	)
}
