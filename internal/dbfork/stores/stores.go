// Package stores enumerates the on-disk java-tron store names and
// the fixed byte keys dbfork's mutation engine reads + writes.
//
// All values pinned byte-for-byte to java-tron's own
// `org.tron.plugins.utils.Constant` to guarantee that anything
// dbfork writes is readable by an unmodified FullNode binary.
// Source of truth:
//
//	tron-docker/tools/toolkit/src/main/java/org/tron/plugins/utils/Constant.java
//
// Do NOT change these values without verifying against java-tron's
// store-name conventions (the equivalence test catches mismatches,
// but the constants exist as the explicit contract).
package stores

// Directory names under `<data-dir>/database/<store>/`. Each store is
// its own LevelDB/RocksDB instance. dbfork opens exactly these 8.
const (
	WitnessStore           = "witness"
	WitnessScheduleStore   = "witness_schedule"
	AccountStore           = "account"
	DynamicPropertiesStore = "properties"
	AssetIssueV2Store      = "asset-issue-v2"
	AccountAssetStore      = "account-asset"
	ContractStore          = "contract"
	StorageRowStore        = "storage-row"
)

// AllStores is the deterministic open-order for dbfork's batch
// session. Used by Engine.Open to materialise the per-store handles
// in one pass.
var AllStores = []string{
	WitnessStore,
	WitnessScheduleStore,
	AccountStore,
	DynamicPropertiesStore,
	AssetIssueV2Store,
	AccountAssetStore,
	ContractStore,
	StorageRowStore,
}

// Fixed byte keys inside DynamicPropertiesStore + WitnessScheduleStore.
// Exposed as untyped string constants — the engine API takes []byte
// but Go converts cleanly at the call site (`engine.Get([]byte(stores.
// KeyLatestBlockHeaderTimestamp))`), and constants can't be mutated
// by import-side code the way `var []byte` literals can.
//
// The byte literals come straight from java-tron — note the case
// inconsistency between the config field name (camelCase) and the
// on-disk key (mixed snake_case / SHOUTING). dbfork MUST emit the
// on-disk variant when writing — the config-field name is just for
// fork.conf parsing.
//
// Examples:
//
//	conf: latestBlockHeaderTimestamp  →  db key: latest_block_header_timestamp
//	conf: maintenanceTimeInterval     →  db key: MAINTENANCE_TIME_INTERVAL
//	conf: nextMaintenanceTime         →  db key: NEXT_MAINTENANCE_TIME
const (
	// DynamicPropertiesStore.
	KeyLatestBlockHeaderTimestamp = "latest_block_header_timestamp"
	KeyLatestBlockHeaderNumber    = "latest_block_header_number"
	KeyMaintenanceTimeInterval    = "MAINTENANCE_TIME_INTERVAL"
	KeyNextMaintenanceTime        = "NEXT_MAINTENANCE_TIME"

	// WitnessScheduleStore. Holds the list of currently-active witnesses,
	// 21 entries for mainnet at full schedule. Replaced wholesale on
	// shadow fork (`witnessScheduleStore.put(ACTIVE_WITNESSES,
	// concat(addr1, addr2, …))`).
	KeyActiveWitnesses = "active_witnesses"
)

// Address length invariant. java-tron account addresses are
// 21-byte sequences (1 network-prefix byte 0x41 + 20-byte Keccak
// hash of secp256k1 pubkey). Used by validators across this package.
const AddressLength = 21

// MaxActiveWitnessNum bounds the witness scheduling capacity. Forking
// with more than this many witnesses listed in fork.conf is a
// validation error.
const MaxActiveWitnessNum = 27
