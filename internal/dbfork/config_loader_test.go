package dbfork

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canonicalForkConfHOCON is the verbatim fork.conf from java toolkit's
// resources directory (tron-docker/tools/toolkit/src/main/resources/
// fork.conf), pasted here so the parser is validated against the real
// reference document — not a Go author's transcription. If java
// updates fork.conf, sync this string.
const canonicalForkConfHOCON = `witnesses = [
  {
    address = "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
    url = "http://meme5.com"
    voteCount = 100000036
  },
  {
    address = "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    voteCount = 100000035
  },
  {
    address = "TKmyxLsRR2FWMVEHaQA2pZh1xB7oXPXzG1"
  }
]

accounts = [
  {
    address = "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
    accountName = "Meme"
    balance = 99000000000000000
  },
  {
    address = "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    accountType = "Normal"
    balance = 99000000000000000
  },
  {
    address = "TLLM21wteSPs4hKjbxgmH1L6poyMjeTbHm"
    owner = "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
  },
  {
    address = "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    trc10Id = "1000001"
    trc10Balance = 100000000
  },
  {
    address = "TKmyxLsRR2FWMVEHaQA2pZh1xB7oXPXzG1"
  }
]

trc20Contracts = [
  {
    contractAddress = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
    balancesSlotPosition = 0
    address = "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    balance = "98800000000000000"
  },
  {
    contractAddress = "TSSMHYeV2uE9qYH95DqyoCuNCzEL1NvU3S"
    balancesSlotPosition = 0
    address = "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    balance = "128745186062510400000000000"
  }
]

latestBlockHeaderTimestamp = 1747986162000
maintenanceTimeInterval = 21600000
nextMaintenanceTime = 1747996162000
`

// canonicalForkConfYAML is the same content as the HOCON above but
// in trond-native YAML. The two MUST parse to identical Config
// structs — pinned by TestLoadConfig_BothFormatsMatch.
const canonicalForkConfYAML = `witnesses:
  - address: "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
    url: "http://meme5.com"
    voteCount: 100000036
  - address: "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    voteCount: 100000035
  - address: "TKmyxLsRR2FWMVEHaQA2pZh1xB7oXPXzG1"

accounts:
  - address: "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
    accountName: "Meme"
    balance: 99000000000000000
  - address: "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    accountType: "Normal"
    balance: 99000000000000000
  - address: "TLLM21wteSPs4hKjbxgmH1L6poyMjeTbHm"
    owner: "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
  - address: "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    trc10Id: "1000001"
    trc10Balance: 100000000
  - address: "TKmyxLsRR2FWMVEHaQA2pZh1xB7oXPXzG1"

trc20Contracts:
  - contractAddress: "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
    balancesSlotPosition: 0
    address: "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    balance: "98800000000000000"
  - contractAddress: "TSSMHYeV2uE9qYH95DqyoCuNCzEL1NvU3S"
    balancesSlotPosition: 0
    address: "TRY18iTFy6p8yhWiCt1dhd2gz2c15ungq3"
    balance: "128745186062510400000000000"

latestBlockHeaderTimestamp: 1747986162000
maintenanceTimeInterval: 21600000
nextMaintenanceTime: 1747996162000
`

// TestLoadConfigBytes_HOCONCanonical is the byte-level pin against
// the java toolkit's canonical fork.conf — every field that the
// fixture sets must parse to the documented value. A regression in
// the hocon library or our extractors surfaces here before it can
// silently mis-fork a real chain.
func TestLoadConfigBytes_HOCONCanonical(t *testing.T) {
	cfg, err := LoadConfigBytes([]byte(canonicalForkConfHOCON), FormatHOCON)
	if err != nil {
		t.Fatalf("LoadConfigBytes HOCON: %v", err)
	}
	assertCanonical(t, cfg)
}

// TestLoadConfigBytes_YAMLCanonical is the equivalent for YAML — same
// data, different syntax. Pinning both ensures the cross-format
// equivalence stays a real contract, not a "happens to work on
// simple inputs" coincidence.
func TestLoadConfigBytes_YAMLCanonical(t *testing.T) {
	cfg, err := LoadConfigBytes([]byte(canonicalForkConfYAML), FormatYAML)
	if err != nil {
		t.Fatalf("LoadConfigBytes YAML: %v", err)
	}
	assertCanonical(t, cfg)
}

// TestLoadConfig_BothFormatsMatch is the cross-format equivalence
// gate: the two loaders must produce *structurally identical*
// Configs from the same data. Catches the case where one format
// silently does extra coercion (e.g. YAML's "true"/"false" string
// vs bool, HOCON's int vs float).
func TestLoadConfig_BothFormatsMatch(t *testing.T) {
	hoconCfg, err := LoadConfigBytes([]byte(canonicalForkConfHOCON), FormatHOCON)
	if err != nil {
		t.Fatal(err)
	}
	yamlCfg, err := LoadConfigBytes([]byte(canonicalForkConfYAML), FormatYAML)
	if err != nil {
		t.Fatal(err)
	}

	// Compare section-by-section so a mismatch points at the exact
	// section. reflect.DeepEqual would work but the per-section diff
	// is much easier to act on when a regression bites.
	if len(hoconCfg.Witnesses) != len(yamlCfg.Witnesses) {
		t.Errorf("Witnesses: hocon=%d yaml=%d", len(hoconCfg.Witnesses), len(yamlCfg.Witnesses))
	} else {
		for i := range hoconCfg.Witnesses {
			if hoconCfg.Witnesses[i] != yamlCfg.Witnesses[i] {
				t.Errorf("Witnesses[%d] mismatch:\n hocon=%+v\n yaml=%+v",
					i, hoconCfg.Witnesses[i], yamlCfg.Witnesses[i])
			}
		}
	}
	if len(hoconCfg.Accounts) != len(yamlCfg.Accounts) {
		t.Errorf("Accounts: hocon=%d yaml=%d", len(hoconCfg.Accounts), len(yamlCfg.Accounts))
	} else {
		for i := range hoconCfg.Accounts {
			if hoconCfg.Accounts[i] != yamlCfg.Accounts[i] {
				t.Errorf("Accounts[%d] mismatch:\n hocon=%+v\n yaml=%+v",
					i, hoconCfg.Accounts[i], yamlCfg.Accounts[i])
			}
		}
	}
	if len(hoconCfg.TRC20Contracts) != len(yamlCfg.TRC20Contracts) {
		t.Errorf("TRC20Contracts: hocon=%d yaml=%d",
			len(hoconCfg.TRC20Contracts), len(yamlCfg.TRC20Contracts))
	} else {
		for i := range hoconCfg.TRC20Contracts {
			if hoconCfg.TRC20Contracts[i] != yamlCfg.TRC20Contracts[i] {
				t.Errorf("TRC20Contracts[%d] mismatch:\n hocon=%+v\n yaml=%+v",
					i, hoconCfg.TRC20Contracts[i], yamlCfg.TRC20Contracts[i])
			}
		}
	}
	if hoconCfg.Properties != yamlCfg.Properties {
		t.Errorf("Properties mismatch:\n hocon=%+v\n yaml=%+v",
			hoconCfg.Properties, yamlCfg.Properties)
	}
}

// assertCanonical pins every field of the canonical fork.conf so
// future drift surfaces immediately. Don't shortcut to DeepEqual a
// pre-computed expected struct — the per-field assertions are what
// makes a regression actionable.
func assertCanonical(t *testing.T, cfg *Config) {
	t.Helper()

	if len(cfg.Witnesses) != 3 {
		t.Fatalf("Witnesses len = %d; want 3", len(cfg.Witnesses))
	}
	if cfg.Witnesses[0].Address != "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W" {
		t.Errorf("Witnesses[0].Address = %q", cfg.Witnesses[0].Address)
	}
	if cfg.Witnesses[0].URL != "http://meme5.com" {
		t.Errorf("Witnesses[0].URL = %q", cfg.Witnesses[0].URL)
	}
	if cfg.Witnesses[0].VoteCount != 100000036 {
		t.Errorf("Witnesses[0].VoteCount = %d", cfg.Witnesses[0].VoteCount)
	}
	// Witness without url, with voteCount.
	if cfg.Witnesses[1].URL != "" {
		t.Errorf("Witnesses[1].URL = %q; want empty", cfg.Witnesses[1].URL)
	}
	if cfg.Witnesses[1].VoteCount != 100000035 {
		t.Errorf("Witnesses[1].VoteCount = %d", cfg.Witnesses[1].VoteCount)
	}
	// Witness with only address.
	if cfg.Witnesses[2].Address != "TKmyxLsRR2FWMVEHaQA2pZh1xB7oXPXzG1" {
		t.Errorf("Witnesses[2].Address = %q", cfg.Witnesses[2].Address)
	}
	if cfg.Witnesses[2].VoteCount != 0 {
		t.Errorf("Witnesses[2].VoteCount = %d; want 0 (omitted in conf)", cfg.Witnesses[2].VoteCount)
	}

	if len(cfg.Accounts) != 5 {
		t.Fatalf("Accounts len = %d; want 5", len(cfg.Accounts))
	}
	if cfg.Accounts[0].AccountName != "Meme" {
		t.Errorf("Accounts[0].AccountName = %q", cfg.Accounts[0].AccountName)
	}
	if cfg.Accounts[0].Balance != 99000000000000000 {
		t.Errorf("Accounts[0].Balance = %d", cfg.Accounts[0].Balance)
	}
	if cfg.Accounts[1].AccountType != "Normal" {
		t.Errorf("Accounts[1].AccountType = %q", cfg.Accounts[1].AccountType)
	}
	if cfg.Accounts[2].Owner != "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W" {
		t.Errorf("Accounts[2].Owner = %q", cfg.Accounts[2].Owner)
	}
	if cfg.Accounts[3].TRC10ID != "1000001" {
		t.Errorf("Accounts[3].TRC10ID = %q", cfg.Accounts[3].TRC10ID)
	}
	if cfg.Accounts[3].TRC10Balance != 100000000 {
		t.Errorf("Accounts[3].TRC10Balance = %d", cfg.Accounts[3].TRC10Balance)
	}
	// Sparse account — only address, all else zero.
	if cfg.Accounts[4].Balance != 0 || cfg.Accounts[4].AccountName != "" {
		t.Errorf("Accounts[4] = %+v; want only address set", cfg.Accounts[4])
	}

	if len(cfg.TRC20Contracts) != 2 {
		t.Fatalf("TRC20Contracts len = %d; want 2", len(cfg.TRC20Contracts))
	}
	if cfg.TRC20Contracts[0].ContractAddress != "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t" {
		t.Errorf("TRC20Contracts[0].ContractAddress = %q", cfg.TRC20Contracts[0].ContractAddress)
	}
	if cfg.TRC20Contracts[0].BalancesSlotPosition != 0 {
		t.Errorf("TRC20Contracts[0].BalancesSlotPosition = %d; want 0", cfg.TRC20Contracts[0].BalancesSlotPosition)
	}
	if cfg.TRC20Contracts[0].Balance != "98800000000000000" {
		t.Errorf("TRC20Contracts[0].Balance = %q", cfg.TRC20Contracts[0].Balance)
	}
	// uint256-scale balance — must round-trip as string (not lose
	// precision via int64/float64 conversion).
	if cfg.TRC20Contracts[1].Balance != "128745186062510400000000000" {
		t.Errorf("TRC20Contracts[1].Balance = %q; uint256 precision LOST",
			cfg.TRC20Contracts[1].Balance)
	}

	if cfg.Properties.LatestBlockHeaderTimestamp != 1747986162000 {
		t.Errorf("Properties.LatestBlockHeaderTimestamp = %d",
			cfg.Properties.LatestBlockHeaderTimestamp)
	}
	if cfg.Properties.MaintenanceTimeInterval != 21600000 {
		t.Errorf("Properties.MaintenanceTimeInterval = %d",
			cfg.Properties.MaintenanceTimeInterval)
	}
	if cfg.Properties.NextMaintenanceTime != 1747996162000 {
		t.Errorf("Properties.NextMaintenanceTime = %d", cfg.Properties.NextMaintenanceTime)
	}
}

// TestLoadConfig_FormatDetection pins the extension → format dispatch.
func TestLoadConfig_FormatDetection(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		content  string
		want     Format
	}{
		{"hocon by .conf", "fork.conf", canonicalForkConfHOCON, FormatHOCON},
		{"hocon by .hocon", "fork.hocon", canonicalForkConfHOCON, FormatHOCON},
		{"yaml by .yaml", "fork.yaml", canonicalForkConfYAML, FormatYAML},
		{"yaml by .yml", "fork.yml", canonicalForkConfYAML, FormatYAML},
		// No extension → HOCON, matching java toolkit's default.
		{"hocon by no ext", "fork", canonicalForkConfHOCON, FormatHOCON},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.filename)
			writeFile(t, path, tc.content)
			cfg, err := LoadConfig(path, FormatAuto)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			// Just spot-check one field — full pinning is in
			// TestLoadConfigBytes_*Canonical. The point here is
			// "the extension picked the right parser".
			if cfg.Properties.MaintenanceTimeInterval != 21600000 {
				t.Errorf("MaintenanceTimeInterval = %d; format detection failed",
					cfg.Properties.MaintenanceTimeInterval)
			}
		})
	}
}

// TestLoadConfig_UnknownExtension errors instead of guessing.
// Silently defaulting to one format risks parsing a YAML file as
// HOCON (which superficially "works" for small inputs but
// silently drops fields).
func TestLoadConfig_UnknownExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fork.json")
	writeFile(t, path, `{}`)
	_, err := LoadConfig(path, FormatAuto)
	if err == nil {
		t.Fatal("expected error for unknown extension")
	}
	if !strings.Contains(err.Error(), ".json") {
		t.Errorf("err = %v; want mention of the bad extension", err)
	}
}

// TestLoadConfig_ExplicitOverride: explicit Format wins over the
// detected one. Test by handing a YAML body with a .conf extension.
func TestLoadConfig_ExplicitOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actually-yaml.conf")
	writeFile(t, path, canonicalForkConfYAML)
	cfg, err := LoadConfig(path, FormatYAML)
	if err != nil {
		t.Fatalf("LoadConfig with explicit FormatYAML: %v", err)
	}
	if cfg.Properties.MaintenanceTimeInterval != 21600000 {
		t.Errorf("explicit format override didn't take effect")
	}
}

// TestLoadConfig_EmptyConfig: a fork.conf with no sections parses
// to an all-zero Config (no error). Matches java DbFork's behavior
// of "missing path → skip section".
func TestLoadConfig_EmptyConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format Format
		body   string
	}{
		{"empty HOCON", FormatHOCON, ""},
		{"empty YAML", FormatYAML, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadConfigBytes([]byte(tc.body), tc.format)
			if err != nil {
				t.Fatalf("empty config: %v", err)
			}
			if len(cfg.Witnesses) != 0 || len(cfg.Accounts) != 0 ||
				len(cfg.TRC20Contracts) != 0 {
				t.Errorf("empty config should have empty sections, got %+v", cfg)
			}
			if cfg.Properties.LatestBlockHeaderTimestamp != 0 {
				t.Errorf("empty config Properties not zero: %+v", cfg.Properties)
			}
		})
	}
}

// TestLoadConfigBytes_AutoRejected: FormatAuto only makes sense when
// the loader knows the path's extension. LoadConfigBytes must error
// rather than silently picking a default.
func TestLoadConfigBytes_AutoRejected(t *testing.T) {
	_, err := LoadConfigBytes([]byte("witnesses = []"), FormatAuto)
	if err == nil {
		t.Fatal("expected FormatAuto to be rejected")
	}
	if !strings.Contains(err.Error(), "explicit format") {
		t.Errorf("err = %v; want hint about explicit format requirement", err)
	}
}

// TestLoadConfig_MalformedHOCON: a syntax error surfaces from the
// parser with the file path in context.
func TestLoadConfig_MalformedHOCON(t *testing.T) {
	// Missing closing brace.
	_, err := LoadConfigBytes([]byte(`witnesses = [ { address = `), FormatHOCON)
	if err == nil {
		t.Fatal("expected HOCON parse error")
	}
	if !strings.Contains(err.Error(), "HOCON") {
		t.Errorf("err = %v; want mention of HOCON", err)
	}
}

// TestLoadConfig_HOCONWrongType pins HIGH-1: the underlying HOCON
// library panics on `cfg.GetInt`/`cfg.GetArray` against wrong-type
// values. The loader MUST convert all such panics to typed errors so
// a malformed fork.conf surfaces a structured error instead of
// crashing trond with a stack trace. Three cases cover the panic
// surfaces: top-level int field as string, top-level array as
// string, per-entry int field as string (the silent-zero case from
// HIGH-2 — `balance = "100"` instead of `balance = 100`).
func TestLoadConfig_HOCONWrongType(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantSub string
	}{
		{
			name:    "top-level int as string",
			body:    `maintenanceTimeInterval = "not a number"`,
			wantSub: "maintenanceTimeInterval",
		},
		{
			name:    "top-level array as string",
			body:    `witnesses = "oops"`,
			wantSub: "witnesses",
		},
		{
			name: "per-entry int field as quoted string (silent-zero risk)",
			body: `accounts = [ { address = "T...", balance = "100" } ]`,
			// "balance" is the field name; the error must mention it
			// so the operator knows where the bad quote is.
			wantSub: "accounts[0].balance",
		},
		{
			// User-facing type names: error says "got string", not
			// "got hocon.String". Operators don't care about the Go
			// package internals.
			name:    "user-facing hocon type name in error",
			body:    `maintenanceTimeInterval = "100"`,
			wantSub: "got string",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("loader panicked instead of returning error: %v", r)
				}
			}()
			_, err := LoadConfigBytes([]byte(tc.body), FormatHOCON)
			if err == nil {
				t.Fatal("expected wrong-type error")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("err = %v; want substring %q", err, tc.wantSub)
			}
		})
	}
}

// TestLoadConfig_TRC20NonZeroSlot closes the canonical-coverage gap
// flagged by M1: the canonical fork.conf has balancesSlotPosition=0
// everywhere, so a bug like misreading the key would silently pass
// `assertCanonical`. A synthetic non-zero slot pins the extraction.
func TestLoadConfig_TRC20NonZeroSlot(t *testing.T) {
	body := `trc20Contracts = [
		{
			contractAddress = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
			balancesSlotPosition = 7
			address = "TS1hu4ZCcwBFYpQqUGoWy1GWBzamqxiT5W"
			balance = "1"
		}
	]`
	cfg, err := LoadConfigBytes([]byte(body), FormatHOCON)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.TRC20Contracts) != 1 {
		t.Fatalf("len = %d; want 1", len(cfg.TRC20Contracts))
	}
	if cfg.TRC20Contracts[0].BalancesSlotPosition != 7 {
		t.Errorf("BalancesSlotPosition = %d; want 7",
			cfg.TRC20Contracts[0].BalancesSlotPosition)
	}
}

// TestLoadConfig_MissingFile closes M2: nonexistent paths surface a
// clean error that names the path, so operators don't get a generic
// "no such file" detached from their CLI input.
func TestLoadConfig_MissingFile(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "does-not-exist.conf")
	_, err := LoadConfig(bogus)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "does-not-exist.conf") {
		t.Errorf("err = %v; should name the missing path", err)
	}
}

// TestLoadConfig_YAMLStrictMode pins the KnownFields(true) contract:
// a typo'd top-level key (`lastestBlockHeaderTimestamp` — letters
// transposed) MUST surface an error, not silently drop. Without
// strict mode the operator's fork would apply with the timing field
// zeroed and they'd debug a chain that won't advance.
func TestLoadConfig_YAMLStrictMode(t *testing.T) {
	body := `lastestBlockHeaderTimestamp: 1747986162000`
	_, err := LoadConfigBytes([]byte(body), FormatYAML)
	if err == nil {
		t.Fatal("expected strict-mode error for unknown YAML field")
	}
	if !strings.Contains(err.Error(), "lastestBlockHeaderTimestamp") {
		t.Errorf("err = %v; should name the unknown field", err)
	}
}

// TestLoadConfig_VariadicRejectsExtra pins the >1 format guard on
// LoadConfig (silent-drop footgun from the variadic API).
func TestLoadConfig_VariadicRejectsExtra(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fork.conf")
	writeFile(t, path, canonicalForkConfHOCON)
	_, err := LoadConfig(path, FormatHOCON, FormatYAML)
	if err == nil {
		t.Fatal("expected error for >1 Format arg")
	}
	if !strings.Contains(err.Error(), "at most one Format") {
		t.Errorf("err = %v; want 'at most one Format' hint", err)
	}
}

// TestLoadConfig_MalformedYAML: a YAML syntax error surfaces too.
func TestLoadConfig_MalformedYAML(t *testing.T) {
	// Bad YAML — mixed indentation in a list.
	_, err := LoadConfigBytes([]byte("witnesses:\n  - address: x\n -malformed"), FormatYAML)
	if err == nil {
		t.Fatal("expected YAML parse error")
	}
	if !strings.Contains(err.Error(), "YAML") {
		t.Errorf("err = %v; want mention of YAML", err)
	}
}

// --- helper -----------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
