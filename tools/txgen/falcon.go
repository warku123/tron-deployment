//go:build falcon

package main

// Falcon-512 post-quantum signing via liboqs (https://github.com/open-quantum-safe/liboqs).
//
// Wire-format compatibility with java-tron's BouncyCastle verifier:
//
//   - Algorithm: "Falcon-512" — produces compressed variable-length signatures.
//
//   - Header byte: 0x39 (= 0x30 + logn, logn=9 for Falcon-512). Matches the
//     compressed-format marker that BouncyCastle's FalconSigner emits and that
//     FNDSA512.java checks explicitly before calling verifySignature.
//
//   - Public key on-chain: 896 bytes (liboqs pk minus the leading 0x09 header
//     byte). Matches BouncyCastle's FalconPublicKeyParameters(PARAMS, getH())
//     which strips the same header.
//
//   - Address derivation: 0x41 ‖ Keccak-256(896-byte-h)[12..32], identical to
//     ECDSA and ML-DSA-44 paths in addressFromPubBytes.
//
// Build:
//
//   make build-txgen-falcon
//
// which sets CGO_ENABLED=1 and the liboqs / OpenSSL link flags. See the Makefile
// for the exact pkg-config invocations.

/*
#include <oqs/oqs.h>
#include <stdlib.h>
*/
import "C"
import (
	"encoding/hex"
	"errors"
	"fmt"
	"unsafe"
)

const falconAlg = "Falcon-512" // compressed variable-length sigs, header 0x39

// FalconSigner signs transaction IDs with Falcon-512 (FN_DSA_512) using liboqs.
// Create via NewFalconSigner (load existing keypair) or GenerateFalconKeypair
// (generate + immediately construct).
type FalconSigner struct {
	oqsSig  *C.OQS_SIG // retained across Sign calls; freed by Close
	skBytes []byte     // 1281-byte liboqs secret key
	hPoly   []byte     // 896-byte h polynomial (on-chain public key)
	hexAddr string     // 0x41-prefixed hex address
	b58Addr string     // Base58Check "T..." address
}

// NewFalconSigner builds a signer from an already-generated keypair.
//   - skHex: 2562 hex chars (1281-byte liboqs Falcon-512 secret key)
//   - pkHex: 1792 hex chars (896-byte h polynomial, header byte stripped)
//
// Generate a fresh keypair with GenerateFalconKeypair or `txgen keygen`.
func NewFalconSigner(skHex, pkHex string) (*FalconSigner, error) {
	if len(skHex) != falconSKHexLen {
		return nil, fmt.Errorf("FN_DSA_512 privateKey must be %d hex chars (%d bytes)", falconSKHexLen, falconSKLen)
	}
	if len(pkHex) != falconHHexLen {
		return nil, fmt.Errorf("FN_DSA_512 publicKey must be %d hex chars (%d bytes h polynomial)", falconHHexLen, falconHLen)
	}
	sk, err := hex.DecodeString(skHex)
	if err != nil {
		return nil, fmt.Errorf("decode FN_DSA_512 privateKey: %w", err)
	}
	hPoly, err := hex.DecodeString(pkHex)
	if err != nil {
		return nil, fmt.Errorf("decode FN_DSA_512 publicKey: %w", err)
	}
	sig, err := newOQSSig()
	if err != nil {
		return nil, err
	}
	hexAddr, b58 := addressFromPubBytes(hPoly)
	return &FalconSigner{
		oqsSig:  sig,
		skBytes: sk,
		hPoly:   hPoly,
		hexAddr: hexAddr,
		b58Addr: b58,
	}, nil
}

// Close releases the underlying liboqs OQS_SIG object.
// Call when the signer is no longer needed (e.g. via defer s.Close()).
func (s *FalconSigner) Close() {
	if s.oqsSig != nil {
		C.OQS_SIG_free(s.oqsSig)
		s.oqsSig = nil
	}
}

// Sign produces a compressed Falcon-512 signature over the 32-byte txID
// (hex-encoded). The message signed is the raw txID bytes — the same digest
// ECDSA and ML-DSA-44 sign — matching java-tron's verifier path.
func (s *FalconSigner) Sign(txIDHex string) ([]byte, error) {
	msg, err := hex.DecodeString(txIDHex)
	if err != nil {
		return nil, err
	}
	if len(msg) != 32 {
		return nil, errors.New("txID must be 32 bytes")
	}

	sigBuf := make([]byte, falconSigLen)
	var sigLen C.size_t

	rc := C.OQS_SIG_sign(
		s.oqsSig,
		(*C.uint8_t)(unsafe.Pointer(&sigBuf[0])),
		&sigLen,
		(*C.uint8_t)(unsafe.Pointer(&msg[0])),
		C.size_t(len(msg)),
		(*C.uint8_t)(unsafe.Pointer(&s.skBytes[0])),
	)
	if rc != C.OQS_SUCCESS {
		return nil, errors.New("liboqs: Falcon signing failed")
	}
	return sigBuf[:int(sigLen)], nil
}

// SchemeName returns "FN_DSA_512" — the PQScheme enum name used in
// java-tron's HTTP JSON pq_auth_sig envelope.
func (s *FalconSigner) SchemeName() string { return SchemeFNDSA512 }

// PublicKeyHex returns the 896-byte h polynomial as lowercase hex.
// This is the on-chain public key format expected by BouncyCastle's
// FalconPublicKeyParameters(PARAMS, getH()).
func (s *FalconSigner) PublicKeyHex() string { return hex.EncodeToString(s.hPoly) }

// HexAddress returns the PQ-derived TRON address (21-byte hex form).
func (s *FalconSigner) HexAddress() string { return s.hexAddr }

// Base58Address returns the PQ-derived TRON address ("T..." form).
func (s *FalconSigner) Base58Address() string { return s.b58Addr }

// FalconKeypair holds a freshly generated Falcon-512 keypair ready for use in
// txgen config and on-chain account provisioning.
type FalconKeypair struct {
	PrivateKeyHex string `json:"privateKey"` // 2562 hex chars (liboqs 1281-byte SK)
	PublicKeyHex  string `json:"publicKey"`  // 1792 hex chars (896-byte h polynomial)
	HexAddress    string `json:"hexAddress"` // 42 hex chars, 0x41-prefixed
	Base58Address string `json:"address"`    // Base58Check "T..." form
}

// GenerateFalconKeypair generates a fresh Falcon-512 keypair via liboqs and
// returns it as a FalconKeypair ready to be marshalled to JSON or loaded into
// NewFalconSigner.
func GenerateFalconKeypair() (*FalconKeypair, error) {
	sig, err := newOQSSig()
	if err != nil {
		return nil, err
	}
	defer C.OQS_SIG_free(sig)

	pkBuf := make([]byte, falconPKLen) // 897 bytes (with 0x09 header)
	skBuf := make([]byte, falconSKLen) // 1281 bytes

	rc := C.OQS_SIG_keypair(
		sig,
		(*C.uint8_t)(unsafe.Pointer(&pkBuf[0])),
		(*C.uint8_t)(unsafe.Pointer(&skBuf[0])),
	)
	if rc != C.OQS_SUCCESS {
		return nil, errors.New("liboqs: Falcon keypair generation failed")
	}

	// Strip the 0x09 serialization header: liboqs pk = 0x09 ‖ h_polynomial.
	// BouncyCastle's FalconPublicKeyParameters(PARAMS, getH()) takes the
	// 896-byte h polynomial without the header.
	if pkBuf[0] != 0x09 {
		return nil, fmt.Errorf("liboqs: unexpected Falcon public-key header 0x%02x, want 0x09", pkBuf[0])
	}
	hPoly := pkBuf[1:] // 896 bytes

	hexAddr, b58 := addressFromPubBytes(hPoly)
	return &FalconKeypair{
		PrivateKeyHex: hex.EncodeToString(skBuf),
		PublicKeyHex:  hex.EncodeToString(hPoly),
		HexAddress:    hexAddr,
		Base58Address: b58,
	}, nil
}

// newOQSSig allocates an OQS_SIG context for Falcon-padded-512.
func newOQSSig() (*C.OQS_SIG, error) {
	alg := C.CString(falconAlg)
	defer C.free(unsafe.Pointer(alg))
	sig := C.OQS_SIG_new(alg)
	if sig == nil {
		return nil, errors.New("liboqs: Falcon-512 not available (is liboqs built with OQS_ENABLE_SIG_falcon_512?)")
	}
	return sig, nil
}
