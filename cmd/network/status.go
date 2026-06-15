package network

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/runtime"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show status of all network nodes",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	store, err := state.NewStore(paths.State())
	if err != nil {
		return err
	}

	deployState, err := store.Load()
	if err != nil {
		return err
	}

	// Filter for network nodes (name contains "-node"). Always emit a slice
	// (never nil) so JSON consumers can rely on the array shape.
	networkNodes := make([]state.ManagedNode, 0, len(deployState.Nodes))
	for _, n := range deployState.Nodes {
		if strings.Contains(n.Name, "-node") {
			networkNodes = append(networkNodes, n)
		}
	}

	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, networkNodes)
	}

	if len(networkNodes) == 0 {
		fmt.Println("No network nodes found.")
		return nil
	}

	fmt.Printf("%-25s %-10s %-10s %s\n", "NAME", "STATUS", "RUNTIME", "VERSION")
	for _, n := range networkNodes {
		fmt.Printf("%-25s %-10s %-10s %s\n", n.Name, n.Status, n.Runtime, n.Version)
	}

	// Show monitoring status for each network that has it deployed.
	fmt.Println()
	printMonitoringStatus(cmd.Context(), networkNodes)

	return nil
}

// printMonitoringStatus checks and prints the health of monitoring
// stacks deployed for any network.
func printMonitoringStatus(ctx context.Context, nodes []state.ManagedNode) {
	// Group networks and their monitoring.
	seen := make(map[string]bool)
	found := false
	for _, n := range nodes {
		networkName := extractNetworkName(n.Name)
		if networkName == "" || seen[networkName] {
			continue
		}
		seen[networkName] = true

		monRT := runtime.NewMonitoringRuntime(target.NewLocalTarget(), paths.Deployments())
		status, err := monRT.Status(ctx, networkName)
		if err != nil || status == nil || status.Status == "unknown" {
			continue
		}

		if !found {
			fmt.Println("Monitoring stacks:")
			found = true
		}
		fmt.Printf("  %-20s   %s\n", networkName+"-monitoring", status.Status)
	}
	if !found {
		fmt.Println("No monitoring stacks deployed.")
	}
}

// extractNetworkName strips the "-node<N>" suffix from a node name.
func extractNetworkName(nodeName string) string {
	idx := strings.LastIndex(nodeName, "-node")
	if idx < 0 {
		return ""
	}
	return nodeName[:idx]
}
