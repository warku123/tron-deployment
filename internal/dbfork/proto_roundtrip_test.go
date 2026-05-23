package dbfork

import (
	"testing"

	pb "github.com/tronprotocol/tron-deployment/internal/dbfork/proto/pb"
	"google.golang.org/protobuf/proto"
)

// TestProtoRoundTrip is the smoke gate for the proto pipeline: a
// freshly-set Account / Witness must marshal AND unmarshal back to
// the same field values. Pins that:
//
//   - protoc + protoc-gen-go produced compilable bindings
//   - All transitively-imported messages are resolvable in one Go
//     package (no import cycles between Tron.proto and contract/)
//   - Standard google.golang.org/protobuf/proto wire format works
//     against tronprotocol/protocol's messages
//
// If this fails, run scripts/gen-dbfork-protos.sh and check the
// pinned upstream tag matches what's in proto/README.md.
func TestProtoRoundTrip(t *testing.T) {
	t.Run("Account", func(t *testing.T) {
		orig := &pb.Account{
			Address: []byte{0x41, 0x12, 0x34, 0x56, 0x78, 0x90, 0xab, 0xcd,
				0xef, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0x12, 0x34, 0x56},
			Balance:     1_000_000_000,
			AccountName: []byte("test-account"),
			AssetV2: map[string]int64{
				"1000001": 5000,
				"1000002": 9999,
			},
		}
		data, err := proto.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if len(data) == 0 {
			t.Fatal("marshalled bytes empty")
		}
		var back pb.Account
		if err := proto.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(back.Address) != string(orig.Address) {
			t.Errorf("Address mismatch: %x vs %x", back.Address, orig.Address)
		}
		if back.Balance != orig.Balance {
			t.Errorf("Balance: %d vs %d", back.Balance, orig.Balance)
		}
		if back.AssetV2["1000001"] != 5000 {
			t.Errorf("AssetV2[1000001] = %d; want 5000", back.AssetV2["1000001"])
		}
	})

	t.Run("Witness", func(t *testing.T) {
		orig := &pb.Witness{
			Address:   []byte{0x41, 0xaa, 0xbb, 0xcc},
			VoteCount: 100_000_000,
			Url:       "http://example-witness.com",
			IsJobs:    true,
		}
		data, err := proto.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back pb.Witness
		if err := proto.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if back.VoteCount != orig.VoteCount || back.Url != orig.Url || !back.IsJobs {
			t.Errorf("Witness fields mismatch: got %+v", &back)
		}
	})

	t.Run("Permission with Keys slice", func(t *testing.T) {
		// Permission is what owner_permission on Account uses — needed
		// for the account-permission mutation path.
		orig := &pb.Permission{
			Type:           pb.Permission_Owner,
			Id:             0,
			PermissionName: "owner",
			Threshold:      1,
			Operations:     []byte{0x7f, 0xff, 0x1f, 0xc0},
			Keys: []*pb.Key{
				{Address: []byte{0x41, 0xaa, 0xbb}, Weight: 1},
				{Address: []byte{0x41, 0xcc, 0xdd}, Weight: 1},
			},
		}
		data, err := proto.Marshal(orig)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var back pb.Permission
		if err := proto.Unmarshal(data, &back); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(back.Keys) != 2 || back.Keys[1].Weight != 1 {
			t.Errorf("Permission.Keys round-trip failed: %+v", back.Keys)
		}
	})
}
