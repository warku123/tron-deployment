package dbfork

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

// TestDecodeAddress runs known Base58Check → 21-byte vectors. The
// inputs are from java-tron's own test suite + tronprotocol/protocol
// docs; the outputs are the exact bytes witnessStore expects as keys.
//
// If this test fails, dbfork's whole witness/account path is
// misaligned with java-tron — equivalence tests would catch this
// downstream but a unit-level guard surfaces the regression
// earlier.
func TestDecodeAddress(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string // hex of expected 21-byte address
	}{
		{
			// DBFork.md example witness. Checksum validates so the
			// decoded bytes are the canonical TRON 21-byte form
			// java-tron would produce.
			name: "DBFork example witness",
			in:   "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W",
			want: "41affaf754a72ddc745033007f9a9999b9565d2f74",
		},
		{
			name: "DBFork example account",
			in:   "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3",
			want: "41aabdb4e3749615e5f151def729fc8fcc54533dce",
		},
		{
			// Stable mainnet address; documented widely. Tests
			// that long-form (genuine 34-char) addresses decode.
			name: "Mainnet USDT contract address",
			in:   "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
			want: "41a614f803b6fd780986a42c78ec9c7f77e6ded13c",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DecodeAddress(tc.in)
			if err != nil {
				t.Fatalf("DecodeAddress(%q): %v", tc.in, err)
			}
			gotHex := hex.EncodeToString(got)
			if gotHex != tc.want {
				t.Errorf("DecodeAddress(%q) = %s; want %s",
					tc.in, gotHex, tc.want)
			}
		})
	}
}

// TestDecodeAddress_Errors pins the validation error paths so a
// future refactor can't silently downgrade checks (a base58 typo
// shouldn't decode into a "weird" 21 bytes that then writes to the
// wrong witness slot).
func TestDecodeAddress_Errors(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		errContains string
	}{
		{
			name:        "empty string",
			in:          "",
			errContains: "expected 25 bytes",
		},
		{
			name:        "invalid base58 char",
			in:          "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5_", // '_' not in alphabet
			errContains: "invalid base58 character",
		},
		{
			name: "truncated checksum",
			// Real address with last char dropped → checksum mismatch.
			in:          "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5",
			errContains: "checksum mismatch",
		},
		{
			// Pins the base58Decode phantom-zero edge case (pure-'1'
			// input). Without the trim-trailing-zeros guard, this
			// would decode to 26 bytes and fail with "expected 25
			// bytes". With it, the length is correct and we land on
			// the checksum-mismatch path. This test prevents the
			// off-by-one from regressing silently.
			name:        "all ones (zero address) hits checksum path, not length",
			in:          "1111111111111111111111111", // 25 chars
			errContains: "checksum mismatch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeAddress(tc.in)
			if err == nil {
				t.Fatalf("expected error for %q; got nil", tc.in)
			}
			if !errors.Is(err, ErrInvalidAddress) {
				t.Errorf("err %v should wrap ErrInvalidAddress", err)
			}
			if !strings.Contains(err.Error(), tc.errContains) {
				t.Errorf("err = %v; want substring %q", err, tc.errContains)
			}
		})
	}
}
