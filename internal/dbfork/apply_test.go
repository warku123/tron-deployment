package dbfork

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	pb "github.com/tronprotocol/tron-deployment/internal/dbfork/proto/pb"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// TestApply_GuardsAndNoOp pins the surface contracts of Apply that
// don't need a real on-disk store:
//
//   - nil config errors out clearly (caller bug).
//   - DryRun is not yet implemented and surfaces a TODO error
//     pointing at the right task.
//   - An "empty everything" config (no witnesses, no property
//     tweaks, RetainWitnesses=true) is a true no-op — Apply returns
//     a zero Result with no engine traffic.
//
// The no-op case is what enables fork.conf authors to commit a
// `properties: {}` partial config without surprising side-effects.
func TestApply_GuardsAndNoOp(t *testing.T) {
	t.Run("nil config errors", func(t *testing.T) {
		_, err := Apply("/nonexistent", nil, Options{})
		if err == nil {
			t.Fatal("expected nil-config error")
		}
		if !strings.Contains(err.Error(), "nil config") {
			t.Errorf("err = %v; want 'nil config' message", err)
		}
	})
	t.Run("dry-run rejected with Task #150 hint", func(t *testing.T) {
		_, err := Apply("/nonexistent", &Config{}, Options{DryRun: true})
		if err == nil {
			t.Fatal("expected dry-run not-implemented error")
		}
		if !strings.Contains(err.Error(), "Task #150") {
			t.Errorf("err = %v; should reference Task #150", err)
		}
	})
	t.Run("empty config → no-op result", func(t *testing.T) {
		// The proof that no engine is opened is implicit but solid:
		// dataDir is "/nonexistent", so DetectKind would error with
		// a "probe /nonexistent" message on any open attempt. A nil
		// err here means Apply never tried to open a store — i.e.
		// the short-circuit conditions on cfg.Witnesses /
		// cfg.Properties chose to skip.
		res, err := Apply("/nonexistent", &Config{}, Options{})
		if err != nil {
			t.Fatalf("no-op apply: %v", err)
		}
		if res.WitnessesWritten != 0 || res.ActiveWitnessesSet != 0 ||
			res.PropertiesUpdated != 0 {
			t.Errorf("no-op result not all zero: %+v", res)
		}
	})
	t.Run("properties-only config does not touch witness stores", func(t *testing.T) {
		// Pins the principle-of-least-surprise contract: a fork.conf
		// that only tunes timing must NOT wipe the witness store
		// just because RetainWitnesses defaulted to false. If Apply
		// erroneously entered the witness branch, the "/nonexistent"
		// dataDir would surface as a DetectKind error here.
		_, err := Apply("/nonexistent", &Config{
			Properties: PropertiesSpec{MaintenanceTimeInterval: 60_000},
		}, Options{}) // RetainWitnesses defaults to false — must still skip witnesses
		// We expect an error from DetectKind on the properties store
		// (since "/nonexistent" doesn't exist), but it must mention
		// the PROPERTIES store, not the witness store.
		if err == nil {
			t.Fatal("expected DetectKind error on /nonexistent")
		}
		if !strings.Contains(err.Error(), stores.DynamicPropertiesStore) {
			t.Errorf("err = %v; want properties-store path, not witness",
				err)
		}
		if strings.Contains(err.Error(), stores.WitnessStore) {
			t.Errorf("err = %v; witness store should NOT have been opened",
				err)
		}
	})
}

// TestApply_EndToEnd is the wiring smoke test: stand up the 3 stores
// Apply touches (witness, witness_schedule, properties), call Apply
// with both witness specs + property tweaks, and verify the
// per-store state. This is what the equivalence test (Task #152)
// will scale up against a Java DbFork-produced fixture.
func TestApply_EndToEnd(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.WitnessStore)
	// reuse dataDir as the data root — seed the other two stores
	// under the same root so DetectKind + Open find them.
	seedLevelDBStoreUnder(t, dataDir, stores.WitnessScheduleStore)
	seedLevelDBStoreUnder(t, dataDir, stores.DynamicPropertiesStore)

	// Force a compaction so the .ldb sentinel files appear on disk
	// (otherwise DetectKind sees an empty dir).
	compactAllStores(t, dataDir)

	addr, raw := makeAddress(t, [20]byte{0x42})

	res, err := Apply(dataDir, &Config{
		Witnesses: []WitnessSpec{
			{Address: addr, URL: "http://w.example", VoteCount: 1000},
		},
		Properties: PropertiesSpec{
			LatestBlockHeaderTimestamp: 1_700_000_000_000,
			MaintenanceTimeInterval:    21_600_000,
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.WitnessesWritten != 1 || res.ActiveWitnessesSet != 1 {
		t.Errorf("witness counts off: %+v", res)
	}
	if res.PropertiesUpdated != 2 {
		t.Errorf("propertiesUpdated = %d; want 2 (NextMaintenanceTime omitted)",
			res.PropertiesUpdated)
	}

	// Re-open the witness store and verify the active slate.
	scheduleEng, err := db.OpenLevelDB(dataDir, stores.WitnessScheduleStore)
	if err != nil {
		t.Fatalf("re-open schedule: %v", err)
	}
	defer scheduleEng.Close()
	got, err := scheduleEng.Get([]byte(stores.KeyActiveWitnesses))
	if err != nil {
		t.Fatalf("Get active_witnesses: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("active slate = %x; want %x", got, raw)
	}

	// Spot-check one property — the BigEndian encoding pins the on-
	// disk format Java expects.
	propsEng, err := db.OpenLevelDB(dataDir, stores.DynamicPropertiesStore)
	if err != nil {
		t.Fatal(err)
	}
	defer propsEng.Close()
	tsRaw, err := propsEng.Get([]byte(stores.KeyLatestBlockHeaderTimestamp))
	if err != nil {
		t.Fatalf("Get timestamp: %v", err)
	}
	wantTS := make([]byte, 8)
	binary.BigEndian.PutUint64(wantTS, 1_700_000_000_000)
	if !bytes.Equal(tsRaw, wantTS) {
		t.Errorf("timestamp on disk = %x; want %x", tsRaw, wantTS)
	}
}

// TestApply_EndToEnd_TRC20 is the wiring smoke for the Task #149
// path: stand up contract + storage-row stores, seed a SmartContract
// proto, call Apply with one TRC20Spec, verify the storage-row was
// written. The keccak derivation correctness is covered in
// trc20_test.go; this just proves Apply opens the right stores.
func TestApply_EndToEnd_TRC20(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.ContractStore)
	seedLevelDBStoreUnder(t, dataDir, stores.StorageRowStore)
	compactAllStores(t, dataDir)

	contractAddrStr, contractRaw := makeAddress(t, [20]byte{0xe0})
	accountAddrStr, _ := makeAddress(t, [20]byte{0xe1})

	// Seed a SmartContract proto under the contract address — Apply's
	// MutateTRC20Contracts needs it to derive addressHash + version.
	{
		eng, err := db.OpenLevelDB(dataDir, stores.ContractStore)
		if err != nil {
			t.Fatal(err)
		}
		sc := &pb.SmartContract{Version: 0}
		raw, _ := proto.Marshal(sc)
		b := eng.NewBatch()
		b.Put(contractRaw, raw)
		_ = b.Write()
		b.Close()
		_ = eng.Close()
	}

	res, err := Apply(dataDir, &Config{
		TRC20Contracts: []TRC20Spec{{
			ContractAddress: contractAddrStr,
			Account:         accountAddrStr,
			Balance:         "7777",
		}},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.TRC20SlotsUpdated != 1 {
		t.Errorf("TRC20SlotsUpdated = %d; want 1", res.TRC20SlotsUpdated)
	}
}

// TestApply_EndToEnd_Accounts is the wiring smoke for the Task #148
// path: stand up the 3 account-related stores, call Apply with one
// AccountSpec, and verify Result counters + on-disk merge state.
// The deeper merge/TRC10 semantics are exercised in accounts_test.go;
// this just proves Apply routes correctly.
func TestApply_EndToEnd_Accounts(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.AccountStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AccountAssetStore)
	seedLevelDBStoreUnder(t, dataDir, stores.AssetIssueV2Store)
	compactAllStores(t, dataDir)

	addr, raw := makeAddress(t, [20]byte{0x77})

	res, err := Apply(dataDir, &Config{
		Accounts: []AccountSpec{
			{Address: addr, Balance: 5_000_000, AccountName: "wired"},
		},
	}, Options{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.AccountsModified != 1 {
		t.Errorf("AccountsModified = %d; want 1", res.AccountsModified)
	}

	// Re-open account store and verify the proto landed.
	accountEng, err := db.OpenLevelDB(dataDir, stores.AccountStore)
	if err != nil {
		t.Fatal(err)
	}
	defer accountEng.Close()
	got := readAccount(t, accountEng, raw)
	if got.Balance != 5_000_000 {
		t.Errorf("Balance on disk = %d; want 5_000_000", got.Balance)
	}
	if string(got.AccountName) != "wired" {
		t.Errorf("AccountName = %q; want wired", got.AccountName)
	}
}
