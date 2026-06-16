package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// TestRunApply_RequirePrivate_RefusesNonPrivate is the C1 guard test:
// `apply --require-private` against a non-private intent must refuse with
// a PRIVATE_NETWORK_REQUIRED / exit-2 envelope BEFORE touching the target
// (so no docker daemon is needed — the guard returns at intent-load
// time). A private intent must NOT trip the guard.
func TestRunApply_RequirePrivate_RefusesNonPrivate(t *testing.T) {
	// Isolate state so we never read/write the real ~/.trond.
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	// Global flag state is package-level; save + restore.
	oldPath, oldReq := applyIntentPath, applyRequirePrivate
	t.Cleanup(func() { applyIntentPath, applyRequirePrivate = oldPath, oldReq })

	write := func(network string) string {
		p := filepath.Join(t.TempDir(), "intent.yaml")
		body := "name: guard-test\nnetwork: " + network + "\n" +
			"target:\n  type: local\n  runtime: docker\n" +
			"nodes:\n  - type: fullnode\n    version: latest\n"
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	// mainnet + --require-private → refused with the right code/exit.
	applyIntentPath = write("mainnet")
	applyRequirePrivate = true
	err := runApply(cmd, nil)
	if err == nil {
		t.Fatal("expected refusal for mainnet + --require-private, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" {
		t.Errorf("error_code = %q; want PRIVATE_NETWORK_REQUIRED", se.Code)
	}
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit_code = %d; want %d", se.ExitCode, output.ExitValidationError)
	}

	// private + --require-private → must NOT be the private-net refusal
	// (it'll fail later at deploy without a daemon, but never with this
	// code).
	applyIntentPath = write("private")
	err = runApply(cmd, nil)
	if errors.As(err, &se) && se.Code == "PRIVATE_NETWORK_REQUIRED" {
		t.Error("private intent wrongly refused by --require-private guard")
	}
}
