package config

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/render"
)

var validateExplain bool

var validateCmd = &cobra.Command{
	Use:   "validate <intent-path>",
	Short: "Validate an intent file",
	Long: `Load and validate an intent YAML file against the schema.

Exit 0 if valid, exit 2 if invalid.

With --explain, print a per-field breakdown showing which values were
explicit, which fell back to defaults, and any derived values (JVM heap
from resources.memory, etc.). Useful for reviewing what trond will
actually deploy without running render.`,
	Args: cobra.ExactArgs(1),
	RunE: runValidate,
}

func init() {
	validateCmd.Flags().BoolVar(&validateExplain, "explain", false, "Print a per-field breakdown of explicit vs default values")
}

func runValidate(cmd *cobra.Command, args []string) error {
	intentPath := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")

	parsed, err := intent.Load(intentPath)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error()).
			WithSuggestions(
				"Check the intent file syntax with: cat "+intentPath,
				"Refer to examples/ for valid intent files",
			)
	}
	raw, rawErr := intent.LoadRaw(intentPath)
	if rawErr != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, rawErr.Error())
	}
	if err := render.ValidateIntentHTTPPortConflicts("", parsed, raw); err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error()).
			WithSuggestions(
				"Change ports.http, or move the colliding service: ports.solidity_http for solidityPort, config_overrides \"node.http.PBFTPort\" for PBFTPort",
				"Run config render after correcting the conflicting HTTP port",
			)
	}

	if validateExplain {
		// The raw (no-defaults) form distinguishes explicit vs default.
		fmt.Printf("Intent %q is valid (%s, %d node(s))\n\n", parsed.Name, parsed.Network, len(parsed.Nodes))
		printExplain(os.Stdout, raw, parsed)
		return nil
	}

	result := map[string]any{
		"valid":   true,
		"name":    parsed.Name,
		"network": parsed.Network,
		"nodes":   len(parsed.Nodes),
	}

	if outputFmt == "json" {
		output.WriteJSON(os.Stdout, result)
	} else {
		fmt.Printf("Intent %q is valid (%s, %d node(s))\n", parsed.Name, parsed.Network, len(parsed.Nodes))
	}

	return nil
}
