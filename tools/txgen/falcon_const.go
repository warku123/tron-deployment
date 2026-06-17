package main

// Falcon-512 scheme name and key/signature size constants. Defined without a
// build tag so both config.go (validation) and the build-tag-gated
// implementation files (falcon.go / falcon_stub.go) can share them.

const (
	// SchemeFNDSA512 is the PQScheme enum name as serialized in java-tron's
	// HTTP JSON pq_auth_sig envelope.
	SchemeFNDSA512 = "FN_DSA_512"

	falconSKLen  = 1281 // liboqs Falcon-512 secret key bytes
	falconPKLen  = 897  // liboqs Falcon-512 public key bytes (with 0x09 header)
	falconHLen   = 896  // on-chain public key: h polynomial (header stripped)
	falconSigLen = 690  // liboqs Falcon-512 max signature buffer

	falconSKHexLen = falconSKLen * 2 // 2562 hex chars
	falconHHexLen  = falconHLen * 2  // 1792 hex chars
)
