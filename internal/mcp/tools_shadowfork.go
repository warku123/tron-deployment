package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/dbfork"
)

// registerShadowforkTools exposes the dbfork mutation engine as an
// MCP tool. The fork.conf parser + Apply path are exercised in
// internal/dbfork; this layer maps MCP args to dbfork.Config /
// dbfork.Options and returns the per-section counters via the same
// JSON shape the CLI's `--output json` emits (`schemas/output/
// shadow-fork-mutate.schema.json`).
//
// Annotated DestructiveHint because Apply mutates the chain DB in
// place — there's no in-memory dry-run yet (Task #150 follow-up).
// MCP-aware clients should surface this to the user before invoking.

type shadowforkMutateArgs struct {
	DataDir         string `json:"data_dir" jsonschema:"absolute path to the java-tron data directory (parent of database/). Node MUST be stopped before mutation — open LevelDB handles fail with 'resource temporarily unavailable'."`
	ConfigPath      string `json:"config_path" jsonschema:"absolute path to fork.conf (HOCON or YAML)"`
	Format          string `json:"format,omitempty" jsonschema:"explicit format override: auto (default) | hocon | yaml. Auto detects from the file extension."`
	RetainWitnesses bool   `json:"retain_witnesses,omitempty" jsonschema:"keep existing witnesses + active set when fork.conf lists new ones (default: wipe then add). Has no effect when fork.conf has no witnesses section."`
}

func registerShadowforkTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "shadow_fork_mutate",
		Title: "Apply fork.conf to a halted java-tron DB",
		Description: `Mutate a java-tron data directory in place per a fork.conf — replace witnesses + active set, fund accounts, set TRC10/TRC20 holdings, adjust DynamicProperties timestamps. Use to set up a shadow-fork private chain off a real snapshot (the typical recipe: download Nile snapshot via snapshot_download, halt any running node on the data dir, run this tool, then deploy a single-witness node via apply pointed at the mutated dir).

Both HOCON and YAML fork.conf formats accepted (HOCON drops in directly from java-tron's existing tooling). Returns the per-section counters that mirror dbfork.Result + the schemas/output/shadow-fork-mutate.schema.json contract.

Equivalent to ` + "`trond shadow-fork mutate -d <data_dir> -c <config_path> -o json`" + `.

PREREQ: the node owning the data_dir must be stopped (leveldb locks). Verify via list / status before calling.`,
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptrTrue(),
		},
	}, shadowforkMutateTool)
}

func shadowforkMutateTool(_ context.Context, _ *mcp.CallToolRequest, args shadowforkMutateArgs) (*mcp.CallToolResult, any, error) {
	if args.DataDir == "" {
		return errResult(fmt.Errorf("data_dir is required"))
	}
	if args.ConfigPath == "" {
		return errResult(fmt.Errorf("config_path is required"))
	}

	format, err := parseShadowforkFormat(args.Format)
	if err != nil {
		return errResult(err)
	}

	cfg, err := dbfork.LoadConfig(args.ConfigPath, format)
	if err != nil {
		return errResult(fmt.Errorf("load fork.conf: %w", err))
	}

	start := time.Now()
	res, err := dbfork.Apply(args.DataDir, cfg, dbfork.Options{
		RetainWitnesses: args.RetainWitnesses,
	})
	if err != nil {
		return errResult(fmt.Errorf("apply: %w", err))
	}

	// Output shape matches schemas/output/shadow-fork-mutate.schema.json
	// exactly so MCP clients can validate against the same contract the
	// CLI's --output json honours.
	return jsonResult(map[string]any{
		"data_dir":            args.DataDir,
		"config":              args.ConfigPath,
		"format":              format.String(),
		"retain_witnesses":    args.RetainWitnesses,
		"witnesses_written":   res.WitnessesWritten,
		"active_witnesses":    res.ActiveWitnessesSet,
		"accounts_modified":   res.AccountsModified,
		"trc20_slots_updated": res.TRC20SlotsUpdated,
		"properties_updated":  res.PropertiesUpdated,
		"duration_ms":         time.Since(start).Milliseconds(),
	})
}

// parseShadowforkFormat mirrors cmd/shadowfork/mutate.go's parseFormat
// — kept as a private duplicate rather than exported from dbfork
// because the two call sites (cmd + mcp) accept slightly different
// surface shapes (cobra default vs json blank) and exporting would
// blur the dbfork API contract. If a third caller appears, lift to
// dbfork.
func parseShadowforkFormat(s string) (dbfork.Format, error) {
	switch s {
	case "", "auto", "Auto", "AUTO":
		return dbfork.FormatAuto, nil
	case "hocon", "HOCON", "Hocon":
		return dbfork.FormatHOCON, nil
	case "yaml", "YAML", "Yaml", "yml":
		return dbfork.FormatYAML, nil
	default:
		return dbfork.FormatAuto, fmt.Errorf("unknown format %q (want auto | hocon | yaml)", s)
	}
}
