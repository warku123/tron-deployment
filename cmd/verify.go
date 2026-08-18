package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

var (
	verifyIntentPath string
	verifyTimeout    time.Duration
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Verify a deployed node is healthy",
	Long:  "Post-deployment health gate. Polls the node's HTTP API until healthy or timeout.",
	RunE:  runVerify,
}

func init() {
	verifyCmd.Flags().StringVar(&verifyIntentPath, "intent", "", "Path to intent.yaml (required)")
	verifyCmd.Flags().DurationVar(&verifyTimeout, "timeout", 10*time.Minute, "Timeout for health check")
	mustMarkRequired(verifyCmd, "intent")
	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, args []string) error {

	parsed, err := intent.Load(verifyIntentPath)
	if err != nil {
		return exitWithError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	tgt, err := resolveTarget(parsed)
	if err != nil {
		return exitWithError("TARGET_UNREACHABLE", output.ExitTargetUnreachable, err.Error())
	}
	if closer, ok := tgt.(interface{ Close() error }); ok {
		defer closer.Close()
	}

	node := &parsed.Nodes[0]
	httpPort := resolveVerifyPort(parsed.Name, node.Ports.HTTP)

	deadline := time.Now().Add(verifyTimeout)
	pollInterval := 10 * time.Second
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		url := fmt.Sprintf("http://127.0.0.1:%d/wallet/getnowblock", httpPort)
		out, err := target.Get(cmd.Context(), tgt, url, 5*time.Second)
		if err == nil {
			var block struct {
				BlockHeader struct {
					RawData struct {
						Number int64 `json:"number"`
					} `json:"raw_data"`
				} `json:"block_header"`
			}
			if json.Unmarshal(out, &block) == nil && block.BlockHeader.RawData.Number > 0 {
				result := map[string]any{
					"name":         parsed.Name,
					"verified":     true,
					"block_height": block.BlockHeader.RawData.Number,
					"attempts":     attempt,
				}
				writeResult(result)
				return nil
			}
		}

		if !quiet {
			fmt.Fprintf(os.Stderr, "Attempt %d: waiting for node to be ready...\n", attempt)
		}
		time.Sleep(pollInterval)
	}

	return exitWithError("VERIFY_TIMEOUT", output.ExitGeneralError,
		fmt.Sprintf("Node %s not healthy after %s", parsed.Name, verifyTimeout),
		"Check node logs: trond logs "+parsed.Name,
		"Run diagnostics: trond diagnose "+parsed.Name)
}

// resolveVerifyPort returns the HTTP port verify should probe.
//
// Source-of-truth ladder:
//  1. state.json's HTTPPort — what was actually allocated at deploy
//     time. This is the only correct value when the intent used
//     auto_ports (intent says 0 there until ApplyDefaults runs, and
//     each fresh Load reallocates different ports).
//  2. intentPort — operator's explicit value, used pre-deploy or when
//     a pre-1.0 state file predates the http_port field.
//  3. 8090 — the java-tron default; last-resort fallback so an
//     out-of-the-box `verify` still does something useful.
//
// Extracted as a free function so a unit test can exercise the ladder
// with a synthetic state directory without standing up cobra.
func resolveVerifyPort(name string, intentPort int) int {
	port := intentPort
	if store, err := state.NewStore(statePath()); err == nil {
		if st, err := store.Load(); err == nil {
			if managed := store.GetNode(st, name); managed != nil && managed.HTTPPort != 0 {
				port = managed.HTTPPort
			}
		}
	}
	if port == 0 {
		port = 8090
	}
	return port
}
