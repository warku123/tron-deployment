package shadowfork

import (
	"strings"
	"testing"

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
