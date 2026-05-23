package dbfork

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/db"
	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// TestMutateProperties_WritesBigEndianLongs verifies that each
// PropertiesSpec field becomes a big-endian 8-byte value at the
// matching DynamicPropertiesStore key — the exact encoding java-tron
// expects (Guava `Longs.toByteArray`).
//
// Setting one field at a time also pins the "zero is skipped"
// contract: the other two keys must NOT be written when their spec
// field is zero.
func TestMutateProperties_WritesBigEndianLongs(t *testing.T) {
	cases := []struct {
		name  string
		spec  PropertiesSpec
		key   string
		value int64
	}{
		{
			name:  "LatestBlockHeaderTimestamp only",
			spec:  PropertiesSpec{LatestBlockHeaderTimestamp: 1_700_000_000_000},
			key:   stores.KeyLatestBlockHeaderTimestamp,
			value: 1_700_000_000_000,
		},
		{
			name:  "MaintenanceTimeInterval only",
			spec:  PropertiesSpec{MaintenanceTimeInterval: 60_000},
			key:   stores.KeyMaintenanceTimeInterval,
			value: 60_000,
		},
		{
			name:  "NextMaintenanceTime only",
			spec:  PropertiesSpec{NextMaintenanceTime: 1_700_000_060_000},
			key:   stores.KeyNextMaintenanceTime,
			value: 1_700_000_060_000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := seedLevelDBStore(t, stores.DynamicPropertiesStore)
			eng, err := db.OpenLevelDB(dataDir, stores.DynamicPropertiesStore)
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()

			n, err := MutateProperties(eng, tc.spec)
			if err != nil {
				t.Fatalf("MutateProperties: %v", err)
			}
			if n != 1 {
				t.Errorf("written = %d; want 1 (single-field spec)", n)
			}

			got, err := eng.Get([]byte(tc.key))
			if err != nil {
				t.Fatalf("Get %q: %v", tc.key, err)
			}
			want := make([]byte, 8)
			binary.BigEndian.PutUint64(want, uint64(tc.value))
			if !bytes.Equal(got, want) {
				t.Errorf("Get %q = %x; want %x", tc.key, got, want)
			}

			// The other two keys must be untouched.
			for _, otherKey := range []string{
				stores.KeyLatestBlockHeaderTimestamp,
				stores.KeyMaintenanceTimeInterval,
				stores.KeyNextMaintenanceTime,
			} {
				if otherKey == tc.key {
					continue
				}
				if _, err := eng.Get([]byte(otherKey)); err == nil {
					t.Errorf("unexpected write to %q for single-field spec", otherKey)
				}
			}
		})
	}
}

// TestMutateProperties_AllZeroNoOp: a fully-zero spec means
// fork.conf omitted the properties section entirely. Apply must
// not touch the store (no batch write at all, so the on-disk
// generation number doesn't advance).
func TestMutateProperties_AllZeroNoOp(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.DynamicPropertiesStore)
	eng, err := db.OpenLevelDB(dataDir, stores.DynamicPropertiesStore)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	n, err := MutateProperties(eng, PropertiesSpec{})
	if err != nil {
		t.Fatalf("MutateProperties zero spec: %v", err)
	}
	if n != 0 {
		t.Errorf("written = %d; want 0 for all-zero spec", n)
	}

	// __seed__ key from setup should be untouched — verifying the
	// engine wasn't even opened-for-write under the hood.
	v, err := eng.Get([]byte("__seed__"))
	if err != nil {
		t.Fatalf("__seed__ missing after no-op: %v", err)
	}
	if !bytes.Equal(v, []byte("seed")) {
		t.Errorf("__seed__ value mutated: %q", v)
	}
}

// TestMutateProperties_AllThreeFields: all three fields set in one
// call → 3 keys written in a single batch.
func TestMutateProperties_AllThreeFields(t *testing.T) {
	dataDir := seedLevelDBStore(t, stores.DynamicPropertiesStore)
	eng, err := db.OpenLevelDB(dataDir, stores.DynamicPropertiesStore)
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()

	spec := PropertiesSpec{
		LatestBlockHeaderTimestamp: 1_700_000_000_000,
		MaintenanceTimeInterval:    21_600_000,
		NextMaintenanceTime:        1_700_021_600_000,
	}
	n, err := MutateProperties(eng, spec)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("written = %d; want 3", n)
	}

	expect := map[string]int64{
		stores.KeyLatestBlockHeaderTimestamp: spec.LatestBlockHeaderTimestamp,
		stores.KeyMaintenanceTimeInterval:    spec.MaintenanceTimeInterval,
		stores.KeyNextMaintenanceTime:        spec.NextMaintenanceTime,
	}
	for key, val := range expect {
		got, err := eng.Get([]byte(key))
		if err != nil {
			t.Errorf("Get %q: %v", key, err)
			continue
		}
		want := make([]byte, 8)
		binary.BigEndian.PutUint64(want, uint64(val))
		if !bytes.Equal(got, want) {
			t.Errorf("Get %q = %x; want %x", key, got, want)
		}
	}
}
