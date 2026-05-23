// Package dbfork is the Go port of java-tron's DbFork toolkit
// utility, embedded into trond's `shadow-fork mutate` subcommand.
//
// Scope: open a halted java-tron data dir (`output-directory/`) and
// apply the state mutations described in a fork.conf — replace
// witnesses, fund accounts, set TRC10/TRC20 balances, adjust
// timestamps + maintenance window — so the data dir is ready for a
// shadow-fork private network to launch against.
//
// Architectural seams:
//
//	internal/dbfork/stores   on-disk store names + fixed byte keys
//	internal/dbfork/db       LevelDB / RocksDB engine abstraction
//	internal/dbfork/proto    protobuf bindings (generated from
//	                         tronprotocol/protocol)
//	(this package)           public Apply entry + per-section
//	                         mutation files (witnesses.go,
//	                         accounts.go, trc20.go, …)
//
// Equivalence with java DbFork is the release gate — see
// equivalence_test.go (Task #152). Until then, integration tests
// run against synthetic fixtures; Phase 1 PoC ships LevelDB only.
package dbfork

import (
	"errors"
	"fmt"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// Config is the parsed fork.conf — driver-agnostic of YAML vs HOCON
// (Task #150 wires both into this single shape). Each section is a
// slice so the loader can build the same shape from either format.
//
// Each apply step (witnesses, accounts, trc20, properties) reads
// from its corresponding field and is a no-op when the field is
// empty / zero. fork.conf is a partial spec: operators can fork
// only witnesses without touching accounts, for example.
type Config struct {
	// Task #147 — witness set replacement (with active set).
	Witnesses []WitnessSpec

	// Task #147 — DynamicProperties timing knobs.
	Properties PropertiesSpec

	// TODO Task #148: Accounts []AccountSpec
	// TODO Task #149: TRC20Contracts []TRC20Spec
}

// Options configures the Apply call. RetainWitnesses mirrors java
// DbFork's --retain-witnesses flag (keep existing witnesses + active
// set in addition to whatever fork.conf adds).
type Options struct {
	RetainWitnesses bool
	// DryRun: if true, walk the mutation plan and report what
	// WOULD change but don't write. The CLI surfaces this for
	// operators sizing a fork before committing.
	DryRun bool
}

// Result summarises what Apply actually did. The CLI surfaces this
// as JSON so MCP agents can chain build/apply/inspect.
type Result struct {
	// WitnessesWritten is the count of Witness protos written to
	// witnessStore. Equals len(cfg.Witnesses) on a successful run.
	WitnessesWritten int `json:"witnesses_written"`

	// ActiveWitnessesSet is the count of addresses in the new
	// witnessScheduleStore active-witness slate. Capped at 27
	// (MaxActiveWitnessNum) per TRON's scheduling rules.
	ActiveWitnessesSet int `json:"active_witnesses_set"`

	// PropertiesUpdated counts the DynamicPropertiesStore keys
	// modified (0..3 depending on which PropertiesSpec fields
	// were non-zero in fork.conf).
	PropertiesUpdated int `json:"properties_updated"`

	// TODO Task #148: AccountsUpdated int
	// TODO Task #149: TRC20SlotsUpdated int
}

// Apply opens the data dir, mutates the 8 stores per cfg, and
// returns a Result. The engine handles + batches are managed
// internally; callers don't see Engine / Batch types from the db
// subpackage.
//
// Phase 1 PoC scope: witnesses + DynamicProperties (Task #147).
// Accounts / TRC10 / TRC20 land in subsequent tasks and gain
// counters on Result as they ship.
//
// DryRun (Task #150): walks the mutation plan and counts what
// WOULD change without writing. Not yet implemented; Apply errors
// when opts.DryRun is true.
func Apply(dataDir string, cfg *Config, opts Options) (*Result, error) {
	if cfg == nil {
		return nil, errors.New("dbfork: nil config")
	}
	if opts.DryRun {
		return nil, errors.New("dbfork: --dry-run not yet implemented (Task #150)")
	}

	res := &Result{}

	// openStore probes the on-disk engine + opens one store. Each
	// java-tron store is a separate LevelDB instance under
	// <dataDir>/database/<name>, so we sniff + open per-store. In
	// practice all 8 stores share the same engine per java-tron's
	// global storage.db.engine config, but we don't ASSUME that.
	openStore := func(name string) (db.Engine, error) {
		kind, kindErr := db.DetectKind(dataDir, name)
		if kindErr != nil {
			return nil, fmt.Errorf("detect kind for %s: %w", name, kindErr)
		}
		return db.Open(dataDir, name, kind)
	}

	// Enter the witness branch ONLY when fork.conf actually lists
	// witnesses. A properties-only or empty-witness Config is a true
	// no-op on the witness stores — gating on `len > 0` here makes
	// the zero-value `Options{}` safe (otherwise RetainWitnesses=false
	// would silently wipe the witness store on a properties-only
	// call). RetainWitnesses then controls only the wipe-before-add
	// behaviour when witnesses ARE listed.
	//
	// Note: MutateWitnesses still supports the "wipe with no
	// replacement" path for direct callers, but Apply does not
	// expose it — wiping witnesses without replacement leaves the
	// shadow chain unable to produce blocks, so it's never the right
	// behaviour from a fork.conf.
	if len(cfg.Witnesses) > 0 {
		witnessEng, err := openStore(stores.WitnessStore)
		if err != nil {
			return nil, err
		}
		defer func() { _ = witnessEng.Close() }()
		scheduleEng, err := openStore(stores.WitnessScheduleStore)
		if err != nil {
			return nil, err
		}
		defer func() { _ = scheduleEng.Close() }()
		written, active, err := MutateWitnesses(witnessEng, scheduleEng,
			cfg.Witnesses, opts.RetainWitnesses)
		if err != nil {
			return nil, err
		}
		res.WitnessesWritten = written
		res.ActiveWitnessesSet = active
	}

	// DynamicProperties — only touch if at least one field is non-zero.
	if cfg.Properties.LatestBlockHeaderTimestamp != 0 ||
		cfg.Properties.MaintenanceTimeInterval != 0 ||
		cfg.Properties.NextMaintenanceTime != 0 {
		propsEng, err := openStore(stores.DynamicPropertiesStore)
		if err != nil {
			return nil, err
		}
		defer func() { _ = propsEng.Close() }()
		written, err := MutateProperties(propsEng, cfg.Properties)
		if err != nil {
			return nil, err
		}
		res.PropertiesUpdated = written
	}

	return res, nil
}
