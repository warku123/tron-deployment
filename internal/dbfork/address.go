package dbfork

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/tronprotocol/tron-deployment/internal/dbfork/stores"
)

// TRON addresses are Base58Check-encoded 21-byte sequences:
//
//	[0x41][20-byte Keccak-256 hash of secp256k1 public key]
//
// Followed by a 4-byte SHA-256(SHA-256(...))[0:4] checksum during
// Base58 encoding. java-tron's `Commons.decodeFromBase58Check`
// inverts this: base58 → 25 bytes → strip + verify checksum → 21
// bytes. We implement the same inversion below so fork.conf
// witness/account addresses match what witnessStore expects as
// keys.
//
// Reference: https://tron-docs.com/api-and-sdks/tron-protocol/tron-addresses

// Base58 alphabet (Bitcoin standard, same as TRON).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// base58Decode converts a Base58 string to its raw byte form.
// Returns the decoded bytes including any leading zero bytes
// represented by leading '1's in the input.
func base58Decode(s string) ([]byte, error) {
	// Decode body via positional base-58 → big-endian bigint.
	// We don't want math/big as a dependency for one operation, so
	// do the conversion ourselves over a []uint16 accumulator.
	// TRON addresses encode to ~34 chars, so the bigint fits in
	// a handful of words.
	leadingZeros := 0
	for _, c := range s {
		if c != '1' {
			break
		}
		leadingZeros++
	}

	// Allocate enough words for the worst-case input length. log(58)
	// = ~1.36 bits per char, so 8 chars → ~11 bits → fits in one
	// uint16. We round up generously.
	words := make([]uint16, 0, len(s))
	words = append(words, 0)
	for _, c := range s[leadingZeros:] {
		digit := bytes.IndexByte([]byte(base58Alphabet), byte(c))
		if digit < 0 {
			return nil, fmt.Errorf("invalid base58 character %q", c)
		}
		// Multiply accumulator by 58 and add digit.
		carry := uint32(digit)
		for i := range words {
			carry += uint32(words[i]) * 58
			words[i] = uint16(carry & 0xff)
			carry >>= 8
		}
		for carry > 0 {
			words = append(words, uint16(carry&0xff))
			carry >>= 8
		}
	}

	// Trim the seed zero word for pure-zero inputs (all '1' chars, or
	// empty body after stripping leading zeros). Without this trim, an
	// "all 1s" input would emit `leadingZeros+1` bytes — one extra
	// zero — and tools reusing base58Decode standalone would see the
	// off-by-one. DecodeAddress's 25-byte length check masks this for
	// TRON addresses, but the helper should be correct in isolation.
	//
	// Drop ALL trailing zero words, not just down to length 1: a
	// pure-'1' input has a single seed-zero word that is itself the
	// phantom. For any input with a real body, the first iteration of
	// the divide loop overwrites words[0] with the first digit, so
	// trailing-zero trimming only fires when the body is genuinely
	// empty.
	for len(words) > 0 && words[len(words)-1] == 0 {
		words = words[:len(words)-1]
	}

	// words is little-endian; reverse to big-endian byte order.
	out := make([]byte, leadingZeros+len(words))
	for i, w := range words {
		out[leadingZeros+len(words)-1-i] = byte(w)
	}
	return out, nil
}

// DecodeAddress parses a TRON Base58Check address (e.g.
// "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W") into its 21-byte raw form
// suitable as a witnessStore / accountStore key.
//
// Validates: length, checksum, network prefix (0x41 for mainnet).
// Returns ErrInvalidAddress on any failure.
func DecodeAddress(s string) ([]byte, error) {
	raw, err := base58Decode(s)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidAddress, err.Error())
	}
	// Expect: 21 (prefix+payload) + 4 (checksum) = 25 bytes.
	if len(raw) != stores.AddressLength+4 {
		return nil, fmt.Errorf("%w: expected 25 bytes after base58 decode, got %d",
			ErrInvalidAddress, len(raw))
	}
	body, checksum := raw[:stores.AddressLength], raw[stores.AddressLength:]
	// Checksum = first 4 bytes of double-SHA256(body).
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	if !bytes.Equal(second[:4], checksum) {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrInvalidAddress)
	}
	if body[0] != 0x41 {
		return nil, fmt.Errorf("%w: expected mainnet prefix 0x41, got 0x%02x",
			ErrInvalidAddress, body[0])
	}
	return body, nil
}

// ErrInvalidAddress is the sentinel for any address-parsing failure
// (bad base58 chars, wrong length, checksum mismatch, wrong network
// prefix). Callers use errors.Is to distinguish from engine errors.
var ErrInvalidAddress = errors.New("dbfork: invalid TRON address")
