package dbfork

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gurkankaymak/hocon"
	"gopkg.in/yaml.v3"
)

// Format identifies the on-disk serialization of a fork.conf.
type Format int

const (
	// FormatAuto detects format by file extension. Used as default
	// when callers don't care which spelling the operator used.
	FormatAuto Format = iota
	// FormatHOCON is the canonical java DbFork format
	// (Typesafe Config), drop-in compatible with existing fork.conf
	// files from the java toolkit.
	FormatHOCON
	// FormatYAML is the trond-native format, consistent with the
	// rest of the CLI's intent files.
	FormatYAML
)

// String returns the lowercase format name for logs / error messages.
func (f Format) String() string {
	switch f {
	case FormatAuto:
		return "auto"
	case FormatHOCON:
		return "hocon"
	case FormatYAML:
		return "yaml"
	default:
		return fmt.Sprintf("Format(%d)", int(f))
	}
}

// LoadConfig reads a fork.conf from disk and returns a parsed Config.
// Format detection is by file extension when format is omitted (or
// FormatAuto is passed explicitly):
//
//	.yaml, .yml          → YAML
//	.conf, .hocon, ""    → HOCON (matches java DbFork's default)
//
// Pass FormatHOCON or FormatYAML explicitly to override detection.
//
// HOCON include directives (`include "other.conf"`) are SUPPORTED by
// the underlying parser (gurkankaymak/hocon). Includes resolve
// relative to the directory of the file being loaded — equivalent to
// java's Typesafe Config behavior. Operators sourcing fork.conf from
// untrusted input should sanitize accordingly. LoadConfigBytes has
// no file-system context, so includes there resolve relative to CWD
// or fail; prefer LoadConfig for include-using configs.
func LoadConfig(path string, format ...Format) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("dbfork: read fork.conf %s: %w", path, err)
	}
	if len(format) > 1 {
		return nil, fmt.Errorf("dbfork: LoadConfig takes at most one Format, got %d",
			len(format))
	}
	chosen := FormatAuto
	if len(format) == 1 {
		chosen = format[0]
	}
	if chosen == FormatAuto {
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".yaml", ".yml":
			chosen = FormatYAML
		case ".conf", ".hocon", "":
			chosen = FormatHOCON
		default:
			return nil, fmt.Errorf("dbfork: unrecognized fork.conf "+
				"extension %q at %s (want .yaml/.yml/.conf/.hocon, "+
				"or pass an explicit Format)", ext, path)
		}
	}
	return LoadConfigBytes(raw, chosen)
}

// LoadConfigBytes parses fork.conf content in the given format. format
// must be FormatYAML or FormatHOCON — FormatAuto is rejected (the
// caller has the file extension; we don't).
func LoadConfigBytes(raw []byte, format Format) (*Config, error) {
	switch format {
	case FormatYAML:
		return loadYAML(raw)
	case FormatHOCON:
		return loadHOCON(raw)
	case FormatAuto:
		return nil, fmt.Errorf("dbfork: LoadConfigBytes requires explicit format (FormatAuto only works via LoadConfig)")
	default:
		return nil, fmt.Errorf("dbfork: unknown format %v", format)
	}
}

// rawConfig is the YAML/HOCON-facing shape, flat at the top level.
// The mapping to Config nests Properties inside a sub-struct so the
// public API stays organized, but the wire format keeps the timing
// fields at the top — matching java DbFork's fork.conf example.
type rawConfig struct {
	Witnesses                  []WitnessSpec `yaml:"witnesses"`
	Accounts                   []AccountSpec `yaml:"accounts"`
	TRC20Contracts             []TRC20Spec   `yaml:"trc20Contracts"`
	LatestBlockHeaderTimestamp int64         `yaml:"latestBlockHeaderTimestamp"`
	MaintenanceTimeInterval    int64         `yaml:"maintenanceTimeInterval"`
	NextMaintenanceTime        int64         `yaml:"nextMaintenanceTime"`
}

func (rc *rawConfig) toConfig() *Config {
	return &Config{
		Witnesses:      rc.Witnesses,
		Accounts:       rc.Accounts,
		TRC20Contracts: rc.TRC20Contracts,
		Properties: PropertiesSpec{
			LatestBlockHeaderTimestamp: rc.LatestBlockHeaderTimestamp,
			MaintenanceTimeInterval:    rc.MaintenanceTimeInterval,
			NextMaintenanceTime:        rc.NextMaintenanceTime,
		},
	}
}

// loadYAML uses gopkg.in/yaml.v3's reflection-based unmarshaller —
// every spec type carries `yaml:"camelCaseName"` tags that match the
// java fork.conf field names exactly, so YAML files can be authored
// as one-to-one translations of HOCON.
//
// KnownFields(true) enables strict mode: unknown top-level or per-
// entry keys produce an error rather than being silently dropped.
// Catches typos like `lastestBlockHeaderTimestamp` (transposed
// letters) which would otherwise no-op and leave the fork unapplied.
// HOCON has no equivalent strict-mode in this lib — the asymmetry is
// intentional, YAML authors are more typo-prone (cf. the canonical
// fork.conf which is HOCON).
func loadYAML(raw []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	rc := &rawConfig{}
	if err := dec.Decode(rc); err != nil {
		// io.EOF for an empty stream is "no document", not a parse
		// error — return a zero Config so empty fork.conf still
		// parses (matches the prior behavior + the empty-config test).
		if errors.Is(err, io.EOF) {
			return rc.toConfig(), nil
		}
		return nil, fmt.Errorf("dbfork: parse YAML: %w", err)
	}
	return rc.toConfig(), nil
}

// loadHOCON hand-extracts each section from the parsed HOCON tree.
// The library has no struct-unmarshal mode, so each field is pulled
// by path. Missing fields return zero values (matching java DbFork's
// `hasPath` skip semantic and the YAML loader's behavior). Wrong-type
// fields return a typed error — matches Typesafe Config's
// `ConfigException.WrongType`, and prevents silent zero-coercion on
// `balance = "100"` (operator quoted a number by mistake).
//
// IMPORTANT: do NOT call cfg.GetInt / cfg.GetArray on this library —
// both panic on wrong-type input. All extractors below go through
// cfg.Get + type-switch instead.
func loadHOCON(raw []byte) (*Config, error) {
	cfg, err := hocon.ParseString(string(raw))
	if err != nil {
		return nil, fmt.Errorf("dbfork: parse HOCON: %w", err)
	}

	tsTimestamp, err := hoconTopInt64(cfg, "latestBlockHeaderTimestamp")
	if err != nil {
		return nil, err
	}
	tsInterval, err := hoconTopInt64(cfg, "maintenanceTimeInterval")
	if err != nil {
		return nil, err
	}
	tsNext, err := hoconTopInt64(cfg, "nextMaintenanceTime")
	if err != nil {
		return nil, err
	}
	rc := &rawConfig{
		LatestBlockHeaderTimestamp: tsTimestamp,
		MaintenanceTimeInterval:    tsInterval,
		NextMaintenanceTime:        tsNext,
	}
	witnesses, err := extractWitnesses(cfg)
	if err != nil {
		return nil, err
	}
	rc.Witnesses = witnesses

	accounts, err := extractAccounts(cfg)
	if err != nil {
		return nil, err
	}
	rc.Accounts = accounts

	trc20s, err := extractTRC20s(cfg)
	if err != nil {
		return nil, err
	}
	rc.TRC20Contracts = trc20s

	return rc.toConfig(), nil
}

// hoconTypeName maps the hocon library's concrete Go types to the
// user-facing HOCON type name (the term an operator would recognize
// from the Typesafe Config docs). Avoids leaking Go's `hocon.Int` /
// `hocon.Float64` package-internal names into error messages.
func hoconTypeName(v hocon.Value) string {
	switch v.(type) {
	case hocon.Int:
		return "integer"
	case hocon.String:
		return "string"
	case hocon.Float32, hocon.Float64:
		return "float"
	case hocon.Boolean:
		return "boolean"
	case hocon.Array:
		return "array"
	case hocon.Object:
		return "object"
	case hocon.Null:
		return "null"
	case hocon.Duration:
		return "duration"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// --- HOCON value extractors -----------------------------------------------
//
// Each extractor is type-strict: missing → zero+nil, wrong-type →
// error. Matches java Typesafe Config semantics and prevents silent
// coercions that would let a misconfigured fork.conf apply with
// zeroed-out fields. The library's typed Get* methods (GetInt /
// GetArray) PANIC on wrong-type and must NOT be used.

// hoconTopInt64 reads a top-level int field. Missing → 0+nil;
// wrong-type → error naming the path. Used for the 3 timing fields.
func hoconTopInt64(cfg *hocon.Config, path string) (int64, error) {
	v := cfg.Get(path)
	if v == nil {
		return 0, nil
	}
	if i, ok := v.(hocon.Int); ok {
		return int64(i), nil
	}
	return 0, fmt.Errorf("dbfork: %s: expected integer, got %s",
		path, hoconTypeName(v))
}

// hoconString reads a string field from an Object. Missing → ""+nil;
// wrong-type → error naming the array+index+key path.
func hoconString(obj hocon.Object, section string, idx int, key string) (string, error) {
	v := obj[key]
	if v == nil {
		return "", nil
	}
	if s, ok := v.(hocon.String); ok {
		return string(s), nil
	}
	return "", fmt.Errorf("dbfork: %s[%d].%s: expected string, got %s",
		section, idx, key, hoconTypeName(v))
}

// hoconInt64Field reads an int field from an Object. Missing → 0+nil;
// wrong-type → error.
func hoconInt64Field(obj hocon.Object, section string, idx int, key string) (int64, error) {
	v := obj[key]
	if v == nil {
		return 0, nil
	}
	if i, ok := v.(hocon.Int); ok {
		return int64(i), nil
	}
	return 0, fmt.Errorf("dbfork: %s[%d].%s: expected integer, got %s",
		section, idx, key, hoconTypeName(v))
}

// hoconIntField is the same as hoconInt64Field but returns Go int
// (for BalancesSlotPosition which the spec defines as int, not int64).
func hoconIntField(obj hocon.Object, section string, idx int, key string) (int, error) {
	v, err := hoconInt64Field(obj, section, idx, key)
	return int(v), err
}

// extractObjects walks an Array path and returns the entries cast to
// Object, with wrong-type errors named to the index. Missing path →
// nil+nil. Replaces cfg.GetArray which panics on wrong-type.
func extractObjects(cfg *hocon.Config, path string) ([]hocon.Object, error) {
	v := cfg.Get(path)
	if v == nil {
		return nil, nil
	}
	arr, ok := v.(hocon.Array)
	if !ok {
		return nil, fmt.Errorf("dbfork: %s: expected array, got %s",
			path, hoconTypeName(v))
	}
	out := make([]hocon.Object, 0, len(arr))
	for i, entry := range arr {
		obj, ok := entry.(hocon.Object)
		if !ok {
			return nil, fmt.Errorf("dbfork: %s[%d]: expected object, got %s",
				path, i, hoconTypeName(entry))
		}
		out = append(out, obj)
	}
	return out, nil
}

func extractWitnesses(cfg *hocon.Config) ([]WitnessSpec, error) {
	objs, err := extractObjects(cfg, "witnesses")
	if err != nil {
		return nil, err
	}
	out := make([]WitnessSpec, 0, len(objs))
	for i, obj := range objs {
		addr, err := hoconString(obj, "witnesses", i, "address")
		if err != nil {
			return nil, err
		}
		url, err := hoconString(obj, "witnesses", i, "url")
		if err != nil {
			return nil, err
		}
		vc, err := hoconInt64Field(obj, "witnesses", i, "voteCount")
		if err != nil {
			return nil, err
		}
		out = append(out, WitnessSpec{Address: addr, URL: url, VoteCount: vc})
	}
	return out, nil
}

func extractAccounts(cfg *hocon.Config) ([]AccountSpec, error) {
	objs, err := extractObjects(cfg, "accounts")
	if err != nil {
		return nil, err
	}
	out := make([]AccountSpec, 0, len(objs))
	for i, obj := range objs {
		spec := AccountSpec{}
		var err error
		if spec.Address, err = hoconString(obj, "accounts", i, "address"); err != nil {
			return nil, err
		}
		if spec.AccountName, err = hoconString(obj, "accounts", i, "accountName"); err != nil {
			return nil, err
		}
		if spec.AccountType, err = hoconString(obj, "accounts", i, "accountType"); err != nil {
			return nil, err
		}
		if spec.Balance, err = hoconInt64Field(obj, "accounts", i, "balance"); err != nil {
			return nil, err
		}
		if spec.Owner, err = hoconString(obj, "accounts", i, "owner"); err != nil {
			return nil, err
		}
		if spec.TRC10ID, err = hoconString(obj, "accounts", i, "trc10Id"); err != nil {
			return nil, err
		}
		if spec.TRC10Balance, err = hoconInt64Field(obj, "accounts", i, "trc10Balance"); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}

func extractTRC20s(cfg *hocon.Config) ([]TRC20Spec, error) {
	objs, err := extractObjects(cfg, "trc20Contracts")
	if err != nil {
		return nil, err
	}
	out := make([]TRC20Spec, 0, len(objs))
	for i, obj := range objs {
		spec := TRC20Spec{}
		var err error
		if spec.ContractAddress, err = hoconString(obj, "trc20Contracts", i, "contractAddress"); err != nil {
			return nil, err
		}
		if spec.BalancesSlotPosition, err = hoconIntField(obj, "trc20Contracts", i, "balancesSlotPosition"); err != nil {
			return nil, err
		}
		if spec.Account, err = hoconString(obj, "trc20Contracts", i, "address"); err != nil {
			return nil, err
		}
		if spec.Balance, err = hoconString(obj, "trc20Contracts", i, "balance"); err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	return out, nil
}
