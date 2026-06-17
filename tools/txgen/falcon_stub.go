//go:build !falcon

package main

// Stub definitions for when the binary is built without the `falcon` build tag.
// FN_DSA_512 signing is not available in this build; attempting to use it
// returns a descriptive error pointing to the correct Makefile target.

import "errors"

// errFalconUnavailable is returned by all FN_DSA_512 operations when the binary
// was built without the `falcon` tag (i.e. without liboqs CGO support).
var errFalconUnavailable = errors.New(
	"FN_DSA_512 requires liboqs: rebuild with `make build-txgen-falcon` (CGO_ENABLED=1 + liboqs installed)",
)

// FalconSigner is a placeholder that always returns errFalconUnavailable.
type FalconSigner struct{}

// NewFalconSigner always returns errFalconUnavailable in non-falcon builds.
func NewFalconSigner(_, _ string) (*FalconSigner, error) { return nil, errFalconUnavailable }

func (s *FalconSigner) Close()                          {}
func (s *FalconSigner) Sign(_ string) ([]byte, error)   { return nil, errFalconUnavailable }
func (s *FalconSigner) SchemeName() string               { return SchemeFNDSA512 }
func (s *FalconSigner) PublicKeyHex() string             { return "" }
func (s *FalconSigner) HexAddress() string               { return "" }
func (s *FalconSigner) Base58Address() string            { return "" }

// FalconKeypair holds a freshly generated Falcon-512 keypair.
type FalconKeypair struct {
	PrivateKeyHex string `json:"privateKey"`
	PublicKeyHex  string `json:"publicKey"`
	HexAddress    string `json:"hexAddress"`
	Base58Address string `json:"address"`
}

// GenerateFalconKeypair always returns errFalconUnavailable in non-falcon builds.
func GenerateFalconKeypair() (*FalconKeypair, error) { return nil, errFalconUnavailable }
