package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

func stdoutWriter() io.Writer { return os.Stdout }

// localDockerExec runs docker on the local host. Used for best-effort
// inspection (no SSH round-trip). Returned string is stdout; on error the
// error message includes stderr so callers can pattern-match docker daemon
// messages like "endpoint with name X already exists".
func localDockerExec(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	var stderr strings.Builder
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return string(out), fmt.Errorf("%s: %s", err, msg)
		}
		return string(out), err
	}
	return string(out), nil
}

// inspectCmd emits a machine-readable manifest of one node or a whole
// network. Test harnesses consume this output to wire test traffic.
//
// Single node:    trond inspect <node>
// Whole network:  trond inspect --network <prefix>
// Everything:     trond inspect --all
var inspectCmd = &cobra.Command{
	Use:   "inspect [node]",
	Short: "Print a topology manifest (endpoints, container IPs, runtime info)",
	Long: `Print a JSON manifest of a managed node or a whole network.

The output is intended for downstream tooling (test harnesses, CI scripts):

    trond inspect my-fullnode -o json
    trond inspect --network my-pn -o json
    trond inspect --all -o json

Each node entry includes resolved endpoints (http, grpc, p2p, metrics),
container_ip when available, runtime, version, status.`,
	RunE: runInspect,
}

var (
	inspectAll        bool
	inspectNetwork    string
	inspectLabelFlags []string
)

func init() {
	inspectCmd.Flags().BoolVar(&inspectAll, "all", false, "Inspect every managed node")
	inspectCmd.Flags().StringVar(&inspectNetwork, "network", "", "Inspect all nodes whose name starts with <network>-node")
	inspectCmd.Flags().StringArrayVar(&inspectLabelFlags, "label", nil, "Filter by label (key=value, repeatable; AND semantics)")
	rootCmd.AddCommand(inspectCmd)
}

func runInspect(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")

	store, err := state.NewStore(statePath())
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}
	deployState, err := store.Load()
	if err != nil {
		return output.NewError("STATE_ERROR", output.ExitGeneralError, err.Error())
	}

	// Pick which nodes to inspect.
	var nodes []state.ManagedNode
	switch {
	case inspectAll:
		nodes = deployState.Nodes
	case inspectNetwork != "":
		prefix := inspectNetwork + "-node"
		for _, n := range deployState.Nodes {
			if strings.HasPrefix(n.Name, prefix) || n.Name == inspectNetwork {
				nodes = append(nodes, n)
			}
		}
	case len(inspectLabelFlags) > 0:
		nodes = deployState.Nodes
	case len(args) == 1:
		n := store.GetNode(deployState, args[0])
		if n == nil {
			return output.NewError("NODE_NOT_FOUND", output.ExitGeneralError,
				fmt.Sprintf("Node %q not found in state", args[0])).
				WithSuggestions("Run: trond list")
		}
		nodes = []state.ManagedNode{*n}
	default:
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			"specify a node name, --network <prefix>, --all, or --label <k=v>")
	}

	// Apply --label filter on top of the chosen scope.
	if filter, ferr := parseLabelFilter(inspectLabelFlags); ferr != nil {
		return ferr
	} else if filter != nil {
		filtered := make([]state.ManagedNode, 0, len(nodes))
		for i := range nodes {
			if matchesLabels(&nodes[i], filter) {
				filtered = append(filtered, nodes[i])
			}
		}
		nodes = filtered
	}

	manifest := buildManifest(cmd.Context(), nodes)

	if outputFmt == "json" || len(nodes) > 1 {
		return output.WriteJSON(stdoutWriter(), manifest)
	}

	// Single-node text mode: compact human-readable format.
	one := manifest["nodes"].([]map[string]any)[0]
	fmt.Printf("Name:     %s\n", one["name"])
	fmt.Printf("Status:   %s\n", one["status"])
	fmt.Printf("Runtime:  %s\n", one["runtime"])
	fmt.Printf("Version:  %s\n", one["version"])
	fmt.Println("Endpoints:")
	for k, v := range one["endpoints"].(map[string]string) {
		fmt.Printf("  %-8s %s\n", k+":", v)
	}
	if ip, ok := one["container_ip"].(string); ok && ip != "" {
		fmt.Printf("Container IP: %s\n", ip)
	}
	return nil
}

// buildManifest assembles the JSON payload. We intentionally keep it cheap:
// no extra docker calls per node unless we have a stable handle. container_ip
// is best-effort — left empty if docker inspect fails or the node isn't a
// docker-runtime node.
func buildManifest(ctx context.Context, nodes []state.ManagedNode) map[string]any {
	out := make([]map[string]any, 0, len(nodes))
	for i := range nodes {
		out = append(out, manifestForNode(ctx, &nodes[i]))
	}
	return map[string]any{
		"nodes": out,
		"count": len(out),
	}
}

func manifestForNode(ctx context.Context, n *state.ManagedNode) map[string]any {
	endpoints := map[string]string{}
	if n.HTTPPort != 0 {
		endpoints["http"] = fmt.Sprintf("http://127.0.0.1:%d", n.HTTPPort)
	}
	if n.GRPCPort != 0 {
		endpoints["grpc"] = fmt.Sprintf("127.0.0.1:%d", n.GRPCPort)
	}

	entry := map[string]any{
		"name":         n.Name,
		"status":       n.Status,
		"runtime":      n.Runtime,
		"version":      n.Version,
		"intent_hash":  n.IntentHash,
		"config_hash":  n.ConfigHash,
		"target":       n.Target,
		"last_applied": n.LastApplied,
		"endpoints":    endpoints,
		// logs: runtime-discriminated locator so a log consumer knows how
		// to read this node's logs without screen-scraping (A1).
		"logs": apply.LogsDescriptor(n),
	}

	// Best-effort container IP + ID for docker nodes — only attempted if
	// the node is local; SSH inspect would need a remote docker call which
	// pulls in target resolution. The test harness usually runs on the
	// same host as the docker daemon, so this is the common case.
	if n.Runtime == "docker" && n.Target.Type == "local" {
		if ip := dockerContainerIP(ctx, n.Name); ip != "" {
			entry["container_ip"] = ip
		}
		if id := dockerContainerID(ctx, n.Name); id != "" {
			entry["container_id"] = id
		}
	}

	return entry
}

// dockerContainerIP shells out to docker inspect. Failures and transient
// states (restarting, no IP yet) are silent — the caller treats absence as
// "unknown" and the manifest omits container_ip rather than mislead the
// downstream tool.
func dockerContainerIP(ctx context.Context, name string) string {
	out, err := localDockerExec(ctx, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(out)
	// Reject anything that doesn't look like a dotted-quad. Docker prints
	// "invalid IP" or empty when the container is restarting.
	if ip == "" || strings.Contains(ip, " ") || !strings.Contains(ip, ".") {
		return ""
	}
	return ip
}

// dockerContainerID shells out to docker inspect for the full container ID.
// Validation (64-hex, reject docker error text) is shared with
// apply.ContainerID via apply.NormalizeContainerID so the format rule has a
// single source of truth across the local (string) and target.Exec ([]byte)
// paths.
func dockerContainerID(ctx context.Context, name string) string {
	out, err := localDockerExec(ctx, "inspect", "-f", "{{.Id}}", name)
	if err != nil {
		return ""
	}
	return apply.NormalizeContainerID(out)
}
