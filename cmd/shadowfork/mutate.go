package shadowfork

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/dbfork"
	"github.com/tronprotocol/tron-deployment/internal/output"
)

var (
	mtDataDir         string
	mtConfigPath      string
	mtFormat          string
	mtRetainWitnesses bool
)

var mutateCmd = &cobra.Command{
	Use:   "mutate",
	Short: "Apply a fork.conf to a halted java-tron data directory",
	Long: `Apply the witness / account / TRC10 / TRC20 / property
mutations described by a fork.conf to an existing java-tron data
directory. The node MUST be stopped before running mutate — open
LevelDB handles will fail with "resource temporarily unavailable".

The data directory is the parent of the per-store database dirs,
i.e. the same path you'd pass to java-tron's --output-directory
or copy from a snapshot via 'trond snapshot download'.

fork.conf format auto-detects by extension: .yaml/.yml → YAML;
.conf/.hocon/no-extension → HOCON (the java DbFork default).
Override with --format if your file uses a non-standard extension.`,
	Example: `  # Apply to a Nile snapshot using the java toolkit's canonical fork.conf
  trond shadow-fork mutate \
    --data-dir ./output-directory \
    --config tron-docker/tools/toolkit/src/main/resources/fork.conf

  # YAML config + structured output for an MCP agent
  trond shadow-fork mutate -d ./output-directory -c fork.yaml --output json

  # Keep the existing witness set + active slate; only fund accounts
  trond shadow-fork mutate -d ./output-directory -c accounts-only.conf --retain-witnesses`,
	RunE: runMutate,
}

func init() {
	mutateCmd.Flags().StringVarP(&mtDataDir, "data-dir", "d", "",
		"java-tron data directory (parent of database/) — required")
	mutateCmd.Flags().StringVarP(&mtConfigPath, "config", "c", "",
		"path to fork.conf (HOCON or YAML) — required")
	mutateCmd.Flags().StringVar(&mtFormat, "format", "auto",
		"fork.conf format: auto | hocon | yaml")
	mutateCmd.Flags().BoolVarP(&mtRetainWitnesses, "retain-witnesses", "r", false,
		"keep existing witnesses + active set when fork.conf lists new ones (default: wipe then add). "+
			"Has no effect when fork.conf has no witnesses section: the witness store is never touched, "+
			"regardless of this flag.")
}

func runMutate(cmd *cobra.Command, _ []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	if mtDataDir == "" {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"--data-dir is required (path to the java-tron data directory, parent of database/)")
	}
	if mtConfigPath == "" {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"--config is required (path to fork.conf)")
	}

	format, err := parseFormat(mtFormat)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	cfg, err := dbfork.LoadConfig(mtConfigPath, format)
	if err != nil {
		// dbfork.LoadConfig already prefixes errors with `dbfork:`
		// and points at the offending field; pass through.
		return output.NewError("CONFIG_LOAD_ERROR", output.ExitValidationError, err.Error())
	}

	start := time.Now()
	res, err := dbfork.Apply(mtDataDir, cfg, dbfork.Options{
		RetainWitnesses: mtRetainWitnesses,
	})
	if err != nil {
		// Distinguish "data dir absent / wrong shape" from internal
		// engine errors so exit codes are actionable. The engine's
		// "store X at <path>" error is the signal that the operator
		// pointed --data-dir at the wrong location.
		exit := output.ExitGeneralError
		if errors.Is(err, os.ErrNotExist) {
			exit = output.ExitValidationError
		}
		return output.NewError("APPLY_ERROR", exit, err.Error())
	}
	durationMs := time.Since(start).Milliseconds()

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, map[string]any{
			"data_dir":            mtDataDir,
			"config":              mtConfigPath,
			"format":              format.String(),
			"retain_witnesses":    mtRetainWitnesses,
			"witnesses_written":   res.WitnessesWritten,
			"active_witnesses":    res.ActiveWitnessesSet,
			"accounts_modified":   res.AccountsModified,
			"trc20_slots_updated": res.TRC20SlotsUpdated,
			"properties_updated":  res.PropertiesUpdated,
			"duration_ms":         durationMs,
		})
	}

	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "Shadow-fork mutation applied to %s (config: %s, %s):\n",
		mtDataDir, mtConfigPath, format.String())
	fmt.Fprintf(w, "  witnesses written:    %d\n", res.WitnessesWritten)
	fmt.Fprintf(w, "  active witnesses:     %d\n", res.ActiveWitnessesSet)
	fmt.Fprintf(w, "  accounts modified:    %d\n", res.AccountsModified)
	fmt.Fprintf(w, "  TRC20 slots updated:  %d\n", res.TRC20SlotsUpdated)
	fmt.Fprintf(w, "  properties updated:   %d\n", res.PropertiesUpdated)
	fmt.Fprintf(w, "  duration:             %dms\n", durationMs)
	return nil
}

// parseFormat maps the --format flag string to a dbfork.Format. Case-
// insensitive so an operator passing HOCON, hocon, or Hocon all work.
func parseFormat(s string) (dbfork.Format, error) {
	switch s {
	case "auto", "Auto", "AUTO":
		return dbfork.FormatAuto, nil
	case "hocon", "HOCON", "Hocon":
		return dbfork.FormatHOCON, nil
	case "yaml", "YAML", "Yaml", "yml":
		return dbfork.FormatYAML, nil
	default:
		return dbfork.FormatAuto, fmt.Errorf("unknown --format %q (want auto | hocon | yaml)", s)
	}
}
