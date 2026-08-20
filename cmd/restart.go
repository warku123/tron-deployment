package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

var restartCmd = &cobra.Command{
	Use:   "restart <node>",
	Short: "Restart a node (stop + start)",
	Args:  cobra.ExactArgs(1),
	RunE:  runRestart,
}

var saveRestartState = func(nc *nodeContext) error { return nc.SaveState() }

func init() {
	rootCmd.AddCommand(restartCmd)
}

func runRestart(cmd *cobra.Command, args []string) error {
	name := args[0]
	start := time.Now()

	if err := requirePrivateForNode(name); err != nil {
		return err
	}

	nc, err := resolveNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	if err := nc.Runtime.Stop(cmd.Context(), name); err != nil {
		writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "RESTART_ERROR", Start: start})
		return exitWithError("RESTART_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to stop %s: %v", name, err))
	}

	if err := nc.Runtime.Start(cmd.Context(), name); err != nil {
		nc.Node.Status = "error"
		if saveErr := persistRestartState(name, nc, start); saveErr != nil {
			return saveErr
		}
		writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "RESTART_ERROR", Start: start})
		return exitWithError("RESTART_ERROR", output.ExitGeneralError,
			fmt.Sprintf("Failed to start %s after stop: %v", name, err))
	}

	nc.Node.Status = "running"
	if err := persistRestartState(name, nc, start); err != nil {
		return err
	}
	writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "success", Start: start})

	writeResult(map[string]any{"name": name, "status": "running"})
	return nil
}

func persistRestartState(name string, nc *nodeContext, start time.Time) error {
	if err := saveRestartState(nc); err != nil {
		writeAudit(auditEvent{Command: "restart", Node: name, Target: nc.Target.String(), Result: "error", ErrorCode: "STATE_ERROR", Start: start})
		return exitWithError("STATE_ERROR", output.ExitGeneralError, fmt.Sprintf("Failed to persist %s state: %v", name, err))
	}
	return nil
}
