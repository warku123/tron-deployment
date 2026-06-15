package cmd

import (
	"context"
	"fmt"
	"maps"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var statusCmd = &cobra.Command{
	Use:   "status [node]",
	Short: "Show node status (or list all nodes)",
	Long: `Without arguments: list all managed nodes. With a node name: show
detailed status including (best-effort) live block height, peer count,
sync state, and the running endpoints. Network probes use the same
HTTP API endpoints inspect/diagnose use; failures are surfaced inline
rather than failing the whole command.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runStatus,
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	if len(args) == 0 {
		return runList(cmd, args)
	}

	name := args[0]

	store, err := state.NewStore(statePath())
	if err != nil {
		return err
	}

	deployState, err := store.Load()
	if err != nil {
		return err
	}

	node := store.GetNode(deployState, name)
	if node == nil {
		return exitWithError("NODE_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("Node %q not found", name),
			"Run: trond list")
	}

	// Build the contract-shaped response. The CLI contract
	// (specs/.../contracts/cli-contract.md) and knowledge/test-harness.md
	// promise block_height, peer_count, sync_progress_percent, is_synced,
	// uptime, api_endpoints — populate them when reachable, leave the
	// keys absent when the API isn't (so JSON consumers can distinguish
	// "not yet healthy" from "I forgot the field").
	statusInfo := map[string]any{
		"name":         node.Name,
		"status":       node.Status,
		"runtime":      node.Runtime,
		"version":      node.Version,
		"target":       node.Target,
		"last_applied": node.LastApplied,
		"intent_hash":  node.IntentHash,
		"config_hash":  node.ConfigHash,
		"api_endpoints": map[string]any{
			"http": fmt.Sprintf("http://127.0.0.1:%d", effectivePort(node.HTTPPort, 8090)),
			"grpc": fmt.Sprintf("127.0.0.1:%d", effectivePort(node.GRPCPort, 50051)),
		},
	}

	// Live probe — best effort, 3s timeout. We skip if the node isn't
	// running per state, since calling curl against a stopped node is
	// just noise.
	if node.Status == "running" {
		ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
		defer cancel()
		maps.Copy(statusInfo, liveStatusProbe(ctx, node))
	}

	if node.Monitoring != nil && node.Monitoring.Enabled {
		statusInfo["monitoring"] = map[string]any{
			"prometheus_port": node.Monitoring.PrometheusPort,
			"grafana_port":    node.Monitoring.GrafanaPort,
			"status":          probeMonitoringStatus(cmd.Context(), node.Name),
		}
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, statusInfo)
	}

	fmt.Printf("Node:         %s\n", node.Name)
	fmt.Printf("Status:       %s\n", node.Status)
	fmt.Printf("Runtime:      %s\n", node.Runtime)
	fmt.Printf("Version:      %s\n", node.Version)
	fmt.Printf("Target:       %s\n", node.Target.Type)
	fmt.Printf("Last Applied: %s\n", node.LastApplied.Format("2006-01-02 15:04:05 UTC"))
	if h, ok := statusInfo["block_height"].(int64); ok {
		fmt.Printf("Block height: %d\n", h)
	}
	if p, ok := statusInfo["peer_count"].(int); ok {
		fmt.Printf("Peers:        %d\n", p)
	}
	if syn, ok := statusInfo["is_synced"].(bool); ok {
		fmt.Printf("Synced:       %v\n", syn)
	}

	// Show monitoring stack health if deployed.
	if node.Monitoring != nil && node.Monitoring.Enabled {
		monStatus := probeMonitoringStatus(cmd.Context(), node.Name)
		fmt.Printf("Monitoring:   %s (prometheus :%d, grafana :%d)\n",
			monStatus, node.Monitoring.PrometheusPort, node.Monitoring.GrafanaPort)
	}

	return nil
}

func effectivePort(stored, fallback int) int {
	if stored != 0 {
		return stored
	}
	return fallback
}

// liveStatusProbe wraps the package-level apply.LiveStatus so cmd/
// callers don't have to think about target resolution. The actual
// probe logic lives in internal/apply for reuse by MCP / recipe.
func liveStatusProbe(ctx context.Context, node *state.ManagedNode) map[string]any {
	tgt, err := resolveTargetFromNode(node)
	if err != nil {
		return map[string]any{}
	}
	if c, ok := any(tgt).(interface{ Close() error }); ok {
		defer c.Close()
	}
	return apply.LiveStatus(ctx, tgt, node)
}

// probeMonitoringStatus returns a short health status string for the
// monitoring stack deployed alongside the named node. Best-effort —
// returns "unknown" when the stack can't be reached.
func probeMonitoringStatus(ctx context.Context, name string) string {
	monRT := runtime.NewMonitoringRuntime(target.NewLocalTarget(), paths.Deployments())
	status, err := monRT.Status(ctx, name)
	if err != nil || status == nil {
		return "unknown"
	}
	return status.Status
}
