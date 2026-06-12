package shadowfork

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/dbfork"
)

// TestParseFormat pins the case-insensitive --format flag parsing.
// Each enum spelling that an operator might plausibly type must map
// to the right dbfork.Format value; an unknown value must surface a
// typed error mentioning the valid choices.
func TestParseFormat(t *testing.T) {
	happy := map[string]dbfork.Format{
		"auto":  dbfork.FormatAuto,
		"Auto":  dbfork.FormatAuto,
		"AUTO":  dbfork.FormatAuto,
		"hocon": dbfork.FormatHOCON,
		"HOCON": dbfork.FormatHOCON,
		"Hocon": dbfork.FormatHOCON,
		"yaml":  dbfork.FormatYAML,
		"YAML":  dbfork.FormatYAML,
		"Yaml":  dbfork.FormatYAML,
		"yml":   dbfork.FormatYAML,
	}
	for in, want := range happy {
		t.Run("ok/"+in, func(t *testing.T) {
			got, err := parseFormat(in)
			if err != nil {
				t.Fatalf("parseFormat(%q): %v", in, err)
			}
			if got != want {
				t.Errorf("parseFormat(%q) = %v; want %v", in, got, want)
			}
		})
	}

	for _, bad := range []string{"", "json", "toml", "ini", "weird"} {
		t.Run("err/"+bad, func(t *testing.T) {
			_, err := parseFormat(bad)
			if err == nil {
				t.Fatalf("parseFormat(%q): expected error", bad)
			}
			if !strings.Contains(err.Error(), "auto | hocon | yaml") {
				t.Errorf("err = %v; should hint at valid choices", err)
			}
		})
	}
}

// TestRunMutate_FlagValidation pins the two required-flag error paths.
// Tests just the surface — full execution is exercised by the dbfork
// package's Apply tests and the equivalence harness.
func TestRunMutate_FlagValidation(t *testing.T) {
	// Reset globals between sub-tests; cobra holds them in package
	// scope so a previous test's --data-dir would leak otherwise.
	resetMutateFlags := func() {
		mtDataDir = ""
		mtConfigPath = ""
		mtFormat = "auto"
		mtRetainWitnesses = false
	}

	t.Run("missing data-dir", func(t *testing.T) {
		resetMutateFlags()
		mtConfigPath = "/some/conf"
		err := runMutate(mutateCmd, nil)
		if err == nil {
			t.Fatal("expected error for missing --data-dir")
		}
		if !strings.Contains(err.Error(), "--data-dir is required") {
			t.Errorf("err = %v; should mention --data-dir", err)
		}
	})

	t.Run("missing config", func(t *testing.T) {
		resetMutateFlags()
		mtDataDir = "/some/dir"
		err := runMutate(mutateCmd, nil)
		if err == nil {
			t.Fatal("expected error for missing --config")
		}
		if !strings.Contains(err.Error(), "--config is required") {
			t.Errorf("err = %v; should mention --config", err)
		}
	})

	t.Run("bad format", func(t *testing.T) {
		resetMutateFlags()
		mtDataDir = "/some/dir"
		mtConfigPath = "/some/conf"
		mtFormat = "weird"
		err := runMutate(mutateCmd, nil)
		if err == nil {
			t.Fatal("expected error for bad --format")
		}
		if !strings.Contains(err.Error(), "weird") {
			t.Errorf("err = %v; should mention the bad format value", err)
		}
	})
}

// TestRunMutate_HappyPathJSON exercises the full runMutate flow against
// a synthetic data dir + minimal fork.conf. Pins the CLI-layer JSON
// output schema (field names + types) so a future refactor that renames
// a JSON key — for example dropping `active_witnesses` for the
// Result-struct field name `ActiveWitnessesSet` — fails this test
// instead of silently breaking schema-conformant MCP agents.
//
// The dbfork package's Apply tests already cover engine correctness;
// this is the cmd-layer wiring proof.
func TestRunMutate_HappyPathJSON(t *testing.T) {
	dataDir := t.TempDir()
	// Stand up an empty (no stores) data dir — the gating in Apply
	// will skip every section since the config is empty, returning
	// a zero Result without opening any store.
	if err := os.MkdirAll(filepath.Join(dataDir, "database"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Write an empty fork.conf (HOCON) so LoadConfig parses it but
	// every section is empty. dbfork.Apply will skip all branches and
	// return a zero Result.
	confPath := filepath.Join(t.TempDir(), "empty.conf")
	if err := os.WriteFile(confPath, []byte("# empty fork.conf for CLI smoke\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reset + populate globals as the cobra flag parsing would.
	mtDataDir = dataDir
	mtConfigPath = confPath
	mtFormat = "auto"
	mtRetainWitnesses = false
	defer func() {
		mtDataDir, mtConfigPath, mtFormat, mtRetainWitnesses = "", "", "auto", false
	}()

	// Capture stdout so we can parse the JSON result.
	cmd := &cobra.Command{}
	cmd.Flags().StringP("output", "o", "json", "")
	_ = cmd.Flags().Set("output", "json")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	// runMutate writes JSON to os.Stdout via output.WriteJSON, not
	// cmd.OutOrStdout — temporarily redirect stdout to capture.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runMutate(cmd, nil)
	w.Close()
	os.Stdout = origStdout
	if err != nil {
		t.Fatalf("runMutate: %v", err)
	}
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, buf.String())
	}

	// Pin every field documented in the JSON Schema.
	for _, key := range []string{
		"data_dir", "config", "format", "retain_witnesses",
		"witnesses_written", "active_witnesses", "accounts_modified",
		"trc20_slots_updated", "properties_updated", "duration_ms",
	} {
		if _, ok := got[key]; !ok {
			t.Errorf("JSON output missing required field %q (got %v)", key, got)
		}
	}
	// format must be resolved (`hocon` for a .conf extension), not
	// the operator's literal "auto" input.
	if got["format"] != "hocon" {
		t.Errorf("format = %v; want \"hocon\" (resolved from .conf extension)", got["format"])
	}
	// Counters all zero — empty fork.conf + empty data dir.
	for _, key := range []string{
		"witnesses_written", "active_witnesses", "accounts_modified",
		"trc20_slots_updated", "properties_updated",
	} {
		if v, ok := got[key].(float64); !ok || v != 0 {
			t.Errorf("%s = %v; want 0", key, got[key])
		}
	}
}
