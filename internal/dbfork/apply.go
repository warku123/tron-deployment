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
)

// Config is the parsed fork.conf — driver-agnostic of YAML vs HOCON
// (Task #150 wires both into this single shape).
//
// Phase 1 carries a stub. Each subtask fills in its slice:
//
//	#147 — Witnesses, LatestBlockHeaderTimestamp, Maintenance*
//	#148 — Accounts, TRC10
//	#149 — TRC20Contracts
type Config struct {
	// TODO Task #147: Witnesses []WitnessSpec
	// TODO Task #147: LatestBlockHeaderTimestamp int64
	// TODO Task #147: MaintenanceTimeInterval int64
	// TODO Task #147: NextMaintenanceTime int64
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
	// TODO: per-section counters (witnesses written, accounts
	// modified, TRC20 slots updated, etc.). Filled in alongside
	// each Task #147-#149 deliverable.
}

// ErrNotImplemented is returned by Apply during Phase 1 until the
// per-section mutation code (witnesses, accounts, trc20) is wired.
// Callers can detect it via errors.Is to distinguish "engine works
// but feature not in this build" from real failures.
var ErrNotImplemented = errors.New("dbfork: section not yet implemented in Phase 1 PoC")

// Apply opens the data dir, mutates the 8 stores per cfg, and
// returns a Result. The engine handles + batches are managed
// internally; callers don't see Engine / Batch types from the db
// subpackage.
//
// Today (skeleton): returns ErrNotImplemented. Each Task #147-#149
// commit fills in a section.
func Apply(dataDir string, cfg *Config, opts Options) (*Result, error) {
	return nil, ErrNotImplemented
}
