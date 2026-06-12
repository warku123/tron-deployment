package dbfork

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestForkConfTemplate_ParsesAfterSubstitution catches HOCON syntax
// regressions in examples/shadow-fork/fork.conf.template.
//
// The template carries `<WITNESS_TRON_ADDRESS>`, `<NOW_MS>`, and
// `<NEXT_MAINTENANCE_MS>` placeholders that scripts/poc-shadow-fork.sh
// substitutes via sed. A future template edit that breaks HOCON
// syntax (missing quote, dangling brace, bad list separator) would
// silently ship — operators would only find out when the PoC's
// `mutate` phase errored. This test substitutes a known-good set of
// values and asserts the result loads cleanly via dbfork.LoadConfigBytes.
//
// Synthetic values match the script's substitution shape:
//   - address: any valid TRON Base58Check address from the existing
//     address_test.go fixtures.
//   - timestamps: integer literals (the script uses milliseconds-
//     since-epoch; any non-zero int64 exercises the same loader path).
func TestForkConfTemplate_ParsesAfterSubstitution(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	tplPath := filepath.Join(repoRoot, "examples", "shadow-fork", "fork.conf.template")
	raw, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}

	// Apply the same substitution the script does. The known-valid
	// witness address is the DBFork.md example address, also pinned
	// in address_test.go's TestDecodeAddress.
	substituted := bytes.ReplaceAll(raw,
		[]byte("<WITNESS_TRON_ADDRESS>"),
		[]byte("TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"))
	substituted = bytes.ReplaceAll(substituted,
		[]byte("<NOW_MS>"),
		[]byte("1747986162000"))
	substituted = bytes.ReplaceAll(substituted,
		[]byte("<NEXT_MAINTENANCE_MS>"),
		[]byte("1747996162000"))

	cfg, err := LoadConfigBytes(substituted, FormatHOCON)
	if err != nil {
		t.Fatalf("loaded substituted template failed to parse: %v", err)
	}

	// Spot-check that the substitution produced the expected shape —
	// catches a regression where the template's section names drift
	// from the loader's expected keys.
	if len(cfg.Witnesses) != 1 {
		t.Errorf("Witnesses len = %d; want 1 (single-witness PoC)", len(cfg.Witnesses))
	}
	if len(cfg.Accounts) != 1 {
		t.Errorf("Accounts len = %d; want 1 (witness funding)", len(cfg.Accounts))
	}
	if cfg.Properties.LatestBlockHeaderTimestamp != 1747986162000 {
		t.Errorf("LatestBlockHeaderTimestamp = %d; placeholder substitution broken",
			cfg.Properties.LatestBlockHeaderTimestamp)
	}
}
