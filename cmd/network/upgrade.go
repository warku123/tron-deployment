package network

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"gopkg.in/yaml.v3"
)

// upgradeCmd does a rolling upgrade across every node in a private
// network. The semantics differ from `trond upgrade <node>` in three
// important ways:
//
//  1. ORDER: fullnode siblings are upgraded first (one at a time),
//     then witnesses (one at a time). This minimises the chance of
//     losing block production during the rollout — witnesses are
//     the consensus producers; fullnodes can drift briefly without
//     breaking the chain.
//
//  2. GATING: each node is verified with `trond verify` before the
//     runner moves to the next. A failed verify halts the rollout
//     and surfaces which node failed (operator decides whether to
//     rollback the half-upgraded set).
//
//  3. ROLLBACK ON FAILURE: when --auto-rollback is set, the runner
//     issues `trond rollback <node>` for every successfully-upgraded
//     node before halting. Without the flag, the partial rollout
//     stays as-is and the operator is told which nodes to roll back
//     manually.
//
// We deliberately re-exec the trond binary itself for `upgrade /
// verify / rollback` rather than calling internal/apply directly.
// Each node's lifecycle is its own subprocess: clean lock semantics,
// independent logging, and the operator can ^C between nodes if the
// rollout looks wrong.
var upgradeCmd = &cobra.Command{
	Use:   "upgrade <network-name>",
	Short: "Rolling upgrade every node in a private network",
	Long: `Upgrade every fullnode then every witness in a private network to a
new java-tron version, one node at a time, gated by per-node
verification.

Failure on any node halts the rollout. With --auto-rollback set,
all already-upgraded nodes get reverted before the command exits.
Without it, the operator is told which nodes need manual rollback.

The network name matches the intent's .name field used with
` + "`trond network create`" + `; nodes are discovered by the "<name>-nodeN"
pattern that create produces.`,
	Args: cobra.ExactArgs(1),
	RunE: runUpgrade,
}

var (
	upgradeVersion       string
	upgradeAutoRollback  bool
	upgradeWitnessFirst  bool
	upgradeVerifyTimeout time.Duration
	upgradeIntentPath    string
)

func init() {
	upgradeCmd.Flags().StringVar(&upgradeVersion, "version", "",
		"Target java-tron version (required)")
	upgradeCmd.Flags().BoolVar(&upgradeAutoRollback, "auto-rollback", false,
		"On any per-node failure, revert all already-upgraded nodes before exiting")
	upgradeCmd.Flags().BoolVar(&upgradeWitnessFirst, "witness-first", false,
		"Upgrade witnesses before fullnodes (default: fullnodes first to protect block production)")
	upgradeCmd.Flags().DurationVar(&upgradeVerifyTimeout, "verify-timeout", 5*time.Minute,
		"Per-node verify timeout passed through to `trond verify`")
	upgradeCmd.Flags().StringVar(&upgradeIntentPath, "intent", "",
		"Path to the original intent.yaml used by `network create` (required for verify)")
	if err := upgradeCmd.MarkFlagRequired("version"); err != nil {
		panic(err)
	}
	if err := upgradeCmd.MarkFlagRequired("intent"); err != nil {
		panic(err)
	}
	Cmd.AddCommand(upgradeCmd)
}

type upgradeStep struct {
	Node       string `json:"node"`
	Phase      string `json:"phase"`  // upgrade | verify | rollback
	Status     string `json:"status"` // ok | failed | skipped
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

func runUpgrade(cmd *cobra.Command, args []string) error {
	networkName := args[0]
	outputFmt, _ := cmd.Flags().GetString("output")
	start := time.Now()

	store, err := state.NewStore(paths.State())
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	st, err := store.Load()
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	witnesses, fullnodes := classifyNetworkNodes(st, networkName)
	if len(witnesses)+len(fullnodes) == 0 {
		return output.NewError("NETWORK_NOT_FOUND", output.ExitGeneralError,
			fmt.Sprintf("no nodes found for network %q", networkName)).
			WithSuggestions("Run `trond network status` to list deployed networks",
				"Confirm the network name matches the intent's .name field used with `network create`")
	}

	// --require-private: every node in the network must be private before we
	// stop/upgrade/restart any of them. Gather refs for the classified set.
	inNetwork := make(map[string]bool, len(witnesses)+len(fullnodes))
	for _, name := range append(append([]string{}, witnesses...), fullnodes...) {
		inNetwork[name] = true
	}
	var refs []guard.NodeRef
	for _, n := range st.Nodes {
		if inNetwork[n.Name] {
			refs = append(refs, guard.NodeRef{Name: n.Name, Network: n.Network})
		}
	}
	if err := guard.EnforceNodes(refs); err != nil {
		return err
	}

	order := append([]string{}, fullnodes...)
	order = append(order, witnesses...)
	if upgradeWitnessFirst {
		order = append([]string{}, witnesses...)
		order = append(order, fullnodes...)
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}

	var steps []upgradeStep
	upgraded := make([]string, 0, len(order))

	for _, node := range order {
		stepStart := time.Now()
		err := runChild(cmd.Context(), exe, "upgrade", node, "--version", upgradeVersion, "--auto-approve")
		steps = append(steps, upgradeStep{
			Node:       node,
			Phase:      "upgrade",
			Status:     statusFor(err),
			DurationMs: time.Since(stepStart).Milliseconds(),
			Error:      errString(err),
		})
		if err != nil {
			return finishWithFailure(cmd.Context(), outputFmt, networkName, exe,
				steps, upgraded, start, node, err)
		}

		stepStart = time.Now()
		err = verifyNode(cmd.Context(), exe, node, upgradeIntentPath, upgradeVerifyTimeout)
		steps = append(steps, upgradeStep{
			Node:       node,
			Phase:      "verify",
			Status:     statusFor(err),
			DurationMs: time.Since(stepStart).Milliseconds(),
			Error:      errString(err),
		})
		if err != nil {
			return finishWithFailure(cmd.Context(), outputFmt, networkName, exe,
				steps, upgraded, start, node, err)
		}

		upgraded = append(upgraded, node)
	}

	result := map[string]any{
		"network":        networkName,
		"version":        upgradeVersion,
		"upgraded_count": len(upgraded),
		"upgraded_nodes": upgraded,
		"steps":          steps,
		"duration_ms":    time.Since(start).Milliseconds(),
		"status":         "success",
	}
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, result)
	}
	fmt.Printf("Rolling upgrade of %s to %s: %d node(s) upgraded successfully (%dms)\n",
		networkName, upgradeVersion, len(upgraded), result["duration_ms"])
	return nil
}

// classifyNetworkNodes splits the discovered network into witness +
// fullnode buckets based on the persisted runtime, ordered so that
// roll-out is deterministic across runs (sorted by node name). The
// "is witness" heuristic is "name contains '-witness' or the node's
// install_path mentions 'witness'" — created by `trond network
// create`. If a future network shape is added, extend the matcher.
func classifyNetworkNodes(st *state.DeploymentState, networkName string) (witnesses, fullnodes []string) {
	prefix := networkName + "-node"
	for _, n := range st.Nodes {
		if !strings.HasPrefix(n.Name, prefix) {
			continue
		}
		if isWitnessNode(n) {
			witnesses = append(witnesses, n.Name)
		} else {
			fullnodes = append(fullnodes, n.Name)
		}
	}
	return witnesses, fullnodes
}

func isWitnessNode(n state.ManagedNode) bool {
	// Heuristics: state doesn't persist the intent's NodeSpec.Type
	// directly, so we fall back to the labels map (the create command
	// sets it when type=witness) and a name-substring check.
	if n.Labels != nil {
		if t, ok := n.Labels["tron.role"]; ok && t == "witness" {
			return true
		}
	}
	return strings.Contains(strings.ToLower(n.Name), "witness")
}

var runChild = runChildCommand

func runChildCommand(ctx context.Context, exe string, argv ...string) error {
	argv = append(argv, "--output", "json")
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// verifyNode projects the multi-node intent to the node being upgraded before
// invoking the existing single-node verify command. Verify reads Nodes[0], so
// passing the original multi-node intent would silently verify only node 0.
func verifyNode(ctx context.Context, exe, node, intentPath string, timeout time.Duration) error {
	parsed, err := intent.Load(intentPath)
	if err != nil {
		return fmt.Errorf("load verify intent: %w", err)
	}
	var projected *intent.Intent
	for i := range parsed.Nodes {
		if fmt.Sprintf("%s-node%d", parsed.Name, i) == node {
			projected, _, _, err = nodeIntent(parsed, i)
			break
		}
	}
	if projected == nil {
		return fmt.Errorf("node %q is not present in verify intent", node)
	}
	data, err := yaml.Marshal(projected)
	if err != nil {
		return fmt.Errorf("marshal verify intent for %s: %w", node, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(intentPath), ".trond-verify-*.yaml")
	if err != nil {
		return fmt.Errorf("create verify intent for %s: %w", node, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write verify intent for %s: %w", node, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close verify intent for %s: %w", node, err)
	}
	return runChild(ctx, exe, "verify", "--intent", tmpPath, "--timeout", timeout.String())
}

func statusFor(err error) string {
	if err == nil {
		return "ok"
	}
	return "failed"
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// finishWithFailure surfaces the partial-rollout state. When
// --auto-rollback is set, every already-upgraded node gets a rollback
// step appended; otherwise the operator is told which nodes need
// manual rollback.
func finishWithFailure(ctx context.Context, outputFmt, networkName, exe string,
	steps []upgradeStep, upgraded []string, start time.Time, failedNode string, failErr error) error {
	rolledBack := make([]string, 0, len(upgraded))
	if upgradeAutoRollback {
		for _, node := range upgraded {
			stepStart := time.Now()
			err := runChild(ctx, exe, "rollback", node)
			steps = append(steps, upgradeStep{
				Node:       node,
				Phase:      "rollback",
				Status:     statusFor(err),
				DurationMs: time.Since(stepStart).Milliseconds(),
				Error:      errString(err),
			})
			if err == nil {
				rolledBack = append(rolledBack, node)
			}
		}
	}

	result := map[string]any{
		"network":           networkName,
		"version":           upgradeVersion,
		"upgraded_nodes":    upgraded,
		"rolled_back_nodes": rolledBack,
		"failed_at":         failedNode,
		"steps":             steps,
		"duration_ms":       time.Since(start).Milliseconds(),
		"status":            "failed",
	}
	if outputFmt == "json" {
		_ = output.WriteJSON(os.Stdout, result)
	}

	se := output.NewError("UPGRADE_FAILED", output.ExitGeneralError,
		fmt.Sprintf("network upgrade halted at node %s: %v", failedNode, failErr))
	if upgradeAutoRollback {
		se = se.WithSuggestions(
			fmt.Sprintf("Auto-rollback ran for %d node(s); inspect logs", len(rolledBack)),
			"Investigate why "+failedNode+" failed before retrying",
		)
	} else {
		hint := "no auto-rollback ran"
		if len(upgraded) > 0 {
			hint += "; upgraded nodes still on new version: " + strings.Join(upgraded, ", ")
		}
		se = se.WithSuggestions(
			hint,
			"Re-run with --auto-rollback to revert on next failure, or `trond rollback <node>` per node",
		)
	}
	return se
}
