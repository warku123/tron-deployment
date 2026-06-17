package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// runKeygen generates a post-quantum keypair and writes it to stdout or a file.
//
// For FN_DSA_512 this requires the binary to be built with the `falcon` tag
// (make build-txgen-falcon). The generated JSON contains the private key, the
// on-chain public key (896-byte h polynomial), and the derived TRON address.
//
// Usage:
//
//	txgen keygen [--scheme FN_DSA_512] [--out falcon-keypair.json]
//
// The output JSON can be used in txgen.json as:
//
//	"pq": {
//	    "enabled": true,
//	    "scheme": "FN_DSA_512",
//	    "privateKey": "<from keypair JSON>",
//	    "publicKey":  "<from keypair JSON>"
//	}
func runKeygen(args []string) error {
	scheme := SchemeFNDSA512
	outPath := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--scheme", "-scheme":
			if i+1 >= len(args) {
				return fmt.Errorf("--scheme requires a value")
			}
			i++
			scheme = strings.ToUpper(args[i])
		case "--out", "-out", "-o":
			if i+1 >= len(args) {
				return fmt.Errorf("--out requires a path")
			}
			i++
			outPath = args[i]
		default:
			return fmt.Errorf("unknown keygen flag %q", args[i])
		}
	}

	switch scheme {
	case SchemeFNDSA512:
		return runFalconKeygen(outPath)
	default:
		return fmt.Errorf("unsupported scheme %q for keygen (supported: FN_DSA_512)", scheme)
	}
}

func runFalconKeygen(outPath string) error {
	log.Printf("keygen: generating Falcon-512 keypair via liboqs…")
	kp, err := GenerateFalconKeypair()
	if err != nil {
		return fmt.Errorf("keygen: %w", err)
	}

	raw, err := json.MarshalIndent(kp, "", "  ")
	if err != nil {
		return err
	}

	if outPath != "" {
		if err := os.WriteFile(outPath, append(raw, '\n'), 0o600); err != nil {
			return fmt.Errorf("keygen: write %s: %w", outPath, err)
		}
		log.Printf("keygen: keypair written to %s", outPath)
	} else {
		fmt.Printf("%s\n", raw)
	}

	fmt.Fprintf(os.Stderr, `
Falcon-512 keypair generated.
TRON address: %s (%s)

Before generating transactions, provision this account on-chain:
  1. Fund the address (%s) with TRX (activation fee + transfer amount).
  2. Call AccountPermissionUpdate to add the Falcon-512 public key:
       public_key: %s...
       weight: ≥ threshold

Add to txgen.json generate.pq section:
  "pq": {
    "enabled": true,
    "scheme": "FN_DSA_512",
    "privateKey": %q,
    "publicKey":  %q,
    "ratio": 30
  }
`, kp.Base58Address, kp.HexAddress,
		kp.Base58Address,
		kp.PublicKeyHex[:32],
		kp.PrivateKeyHex,
		kp.PublicKeyHex,
	)

	return nil
}
