package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var startCmd = &cobra.Command{
	Use:   "start <node>",
	Short: "Start a stopped node",
	Args:  cobra.ExactArgs(1),
	RunE:  runStart,
}

var saveStartState = func(nc *nodeContext) error { return nc.SaveState() }
var resolveStartNodeContext = resolveNodeContext

func init() {
	rootCmd.AddCommand(startCmd)
}

func runStart(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	nc, err := resolveStartNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "start", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "START_ERROR", Start: start})
		return exitWithError("START_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to start %s: %v", name, err))
	}

	nc.Node.Status = "running"
	if err := persistStartState(name, nc, start); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "start", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{"name": name, "status": "running"})
	return nil
}

func persistStartState(name string, nc *nodeContext, start time.Time) error {
	if err := saveStartState(nc); err != nil {
		writeAudit(auditEvent{Command: "start", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "STATE_ERROR", Start: start})
		return exitWithError("STATE_ERROR", output.ExitGeneralError, fmt.Sprintf("Failed to persist %s state: %v", name, err))
	}
	return nil
}
