package cmd

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var healthCmd = &cobra.Command{
	Use:   "health <node>",
	Short: "Quick health check on a node",
	Long:  "Probe the node's HTTP API and check sync progress. Faster than diagnose.",
	Args:  cobra.ExactArgs(1),
	RunE:  runHealth,
}

func init() {
	rootCmd.AddCommand(healthCmd)
}

func runHealth(cmd *cobra.Command, args []string) error {
	name := args[0]

	nc, err := resolveNodeContext(name)
	if err != nil {
		return err
	}
	defer nc.Close()

	// Quick HTTP API probe. The port is captured at apply time and stored in
	// state; older state files (before that field existed) fall back to the
	// java-tron default.
	httpPort := nc.Node.HTTPPort
	if httpPort == 0 {
		httpPort = 8090
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/wallet/getnowblock", httpPort)

	out, err := target.Get(cmd.Context(), nc.Target, url, 5*time.Second)
	if err != nil {
		result := map[string]any{
			"name":   name,
			"health": "unhealthy",
			"error":  "Cannot reach HTTP API",
		}
		writeResult(result)
		return output.NewError("UNHEALTHY", output.ExitGeneralError, "Cannot reach HTTP API")
	}

	var block struct {
		BlockHeader struct {
			RawData struct {
				Number    int64 `json:"number"`
				Timestamp int64 `json:"timestamp"`
			} `json:"raw_data"`
		} `json:"block_header"`
	}

	if err := json.Unmarshal(out, &block); err != nil {
		result := map[string]any{
			"name":   name,
			"health": "degraded",
			"error":  "Invalid API response",
		}
		writeResult(result)
		return nil
	}

	health := "healthy"
	if block.BlockHeader.RawData.Number == 0 {
		health = "starting"
	}

	result := map[string]any{
		"name":         name,
		"health":       health,
		"block_height": block.BlockHeader.RawData.Number,
	}

	writeResult(result)
	return nil
}
