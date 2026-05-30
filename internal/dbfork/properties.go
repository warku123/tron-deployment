package dbfork

import (
	"encoding/binary"
	"fmt"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// PropertiesSpec carries the 3 timing knobs fork.conf can tune in
// the DynamicPropertiesStore. All fields are optional in the conf —
// zero means "don't touch this key".
//
// Why these specifically: each is what java-tron reads on every
// block to decide "is the chain advancing on schedule"? Without
// adjusting at fork time, the shadow chain's first block lags by
// up to a maintenance window because mainnet's last-recorded
// timestamp is hours-to-days behind real time.
type PropertiesSpec struct {
	// LatestBlockHeaderTimestamp seeds the "last seen block time"
	// to roughly now-ish. java-tron uses this in
	// Manager.processBlock's slot calculation; if it's stale by
	// hours, the first new block has to "catch up" through many
	// missed slots, producing debug-log spam and delayed
	// confirmations. Setting to current millis-epoch is the
	// standard fork.conf value.
	LatestBlockHeaderTimestamp int64 `yaml:"latestBlockHeaderTimestamp"`

	// MaintenanceTimeInterval is the cadence between SR-set
	// recomputations. Mainnet uses 6h (21600000 ms). Shorter
	// values (e.g. 1 minute = 60000) help shadow-fork test
	// proposals + protocol upgrades without waiting a day per
	// cycle.
	MaintenanceTimeInterval int64 `yaml:"maintenanceTimeInterval"`

	// NextMaintenanceTime is the absolute deadline of the next
	// SR-set recomputation. Pair with LatestBlockHeaderTimestamp
	// for a clean fork: set NextMaintenanceTime slightly in the
	// future so the first cycle completes promptly.
	NextMaintenanceTime int64 `yaml:"nextMaintenanceTime"`
}

// MutateProperties writes any strictly-positive PropertiesSpec field
// into the DynamicPropertiesStore (gating on `> 0` to match java
// DbFork; negative/zero values are skipped, never written).
// java-tron encodes longs as big-endian
// 8-byte sequences (Guava's `Longs.toByteArray`) — we match exactly
// via `binary.BigEndian.PutUint64`.
//
// Returns the count of keys written so the apply summary can
// surface "3 properties updated" vs a silent no-op when fork.conf
// omitted all three.
func MutateProperties(propsEng db.Engine, spec PropertiesSpec) (written int, err error) {
	batch := propsEng.NewBatch()
	defer batch.Close()

	putLong := func(key string, val int64) {
		buf := make([]byte, 8)
		// Java's Longs.toByteArray is big-endian; binary.BigEndian
		// PutUint64 on the uint64 reinterpretation matches.
		binary.BigEndian.PutUint64(buf, uint64(val))
		batch.Put([]byte(key), buf)
		written++
	}

	// Gate on `> 0`, NOT `!= 0`, to match java DbFork exactly
	// (DbFork.java:373/384/395 each guard `hasPath(X) && getLong(X) > 0`).
	// These are epoch-millis / interval-millis values where a negative
	// is only ever a typo or an underflow; java skips it, and writing
	// it on the Go side would produce a 0xFFFF…-encoded long that
	// decodes as a perpetually-past-due timestamp AND diverges byte-for-
	// byte from java's output (failing the equivalence gate).
	if spec.LatestBlockHeaderTimestamp > 0 {
		putLong(stores.KeyLatestBlockHeaderTimestamp, spec.LatestBlockHeaderTimestamp)
	}
	if spec.MaintenanceTimeInterval > 0 {
		putLong(stores.KeyMaintenanceTimeInterval, spec.MaintenanceTimeInterval)
	}
	if spec.NextMaintenanceTime > 0 {
		putLong(stores.KeyNextMaintenanceTime, spec.NextMaintenanceTime)
	}

	if written == 0 {
		// Defensive guard for direct callers — Apply already
		// short-circuits the all-zero case before calling
		// MutateProperties, so this branch is unreachable from the
		// CLI today. Kept so that tests and any future programmatic
		// callers don't trigger an empty batch.Write.
		return 0, nil
	}
	if err := batch.Write(); err != nil {
		return 0, fmt.Errorf("dbfork: write properties batch: %w", err)
	}
	return written, nil
}
