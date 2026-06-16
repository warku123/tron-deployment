package mcp

import (
	"context"
	"maps"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// registerInspectionTools wires the read-only "what's deployed?"
// tools. None of these mutate state; they're safe to call freely
// from an LLM without prompting the user.

type emptyArgs struct{}

type nodeArg struct {
	Name string `json:"name" jsonschema:"name of the managed node (must match intent.name)"`
}

func registerInspectionTools(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list",
		Title:       "List managed nodes",
		Description: "Returns every node trond is currently managing, with status, runtime, version, and labels. Equivalent to `trond list -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, listNodes)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "status",
		Title:       "Node status",
		Description: "Detailed status for one node. Combines stored state with a best-effort live HTTP probe (block height, peer count, sync state, endpoints). Equivalent to `trond status <name> -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, statusForNode)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "inspect",
		Title:       "Inspect endpoints",
		Description: "Manifest of every node's endpoints, container IPs, and labels. Used by test harnesses to discover where to send traffic. Equivalent to `trond inspect -o json`.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
	}, inspectAllNodes)
}

func listNodes(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return errResult(err)
	}
	st, err := store.Load()
	if err != nil {
		return errResult(err)
	}

	// Reshape into the same JSON we'd emit from `trond list -o json`.
	// Schema: schemas/output/list.schema.json — a flat array, not a
	// {nodes: [...]} object. Matching the CLI shape lets MCP-aware
	// agents pass results into pipelines that already parse `trond
	// list` output.
	rows := make([]map[string]any, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		row := map[string]any{
			"name":         n.Name,
			"status":       n.Status,
			"runtime":      n.Runtime,
			"version":      n.Version,
			"last_applied": n.LastApplied,
			"target_type":  n.Target.Type,
			"network":      n.Network,
			"is_private":   intent.IsPrivate(n.Network),
		}
		if len(n.Labels) > 0 {
			row["labels"] = n.Labels
		}
		rows = append(rows, row)
	}
	return jsonResult(rows)
}

func statusForNode(ctx context.Context, _ *mcp.CallToolRequest, args nodeArg) (*mcp.CallToolResult, any, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return errResult(err)
	}
	st, err := store.Load()
	if err != nil {
		return errResult(err)
	}
	node := store.GetNode(st, args.Name)
	if node == nil {
		return errResult(notFound("status", args.Name))
	}
	out := map[string]any{
		"name":         node.Name,
		"status":       node.Status,
		"runtime":      node.Runtime,
		"version":      node.Version,
		"target":       node.Target,
		"last_applied": node.LastApplied,
		"intent_hash":  node.IntentHash,
		"config_hash":  node.ConfigHash,
		"labels":       node.Labels,
		"network":      node.Network,
		"is_private":   intent.IsPrivate(node.Network),
	}
	// Live probe — best effort, only when state says the node is
	// running. We resolve a target via the node's stored target spec
	// (state.NodeTarget → target.Target) and hand it to apply.LiveStatus.
	// Errors during probe are silently dropped; the caller sees
	// block_height / is_synced / peer_count appear or not.
	if node.Status == "running" {
		if tgt, err := mcpResolveTargetFromNode(node); err == nil {
			if c, ok := any(tgt).(interface{ Close() error }); ok {
				defer c.Close()
			}
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			maps.Copy(out, apply.LiveStatus(probeCtx, tgt, node))
		}
	}
	return jsonResult(out)
}

// mcpResolveTargetFromNode mirrors cmd/resolve.go::resolveTargetFromNode.
// Duplicated so internal/mcp doesn't import cmd/.
func mcpResolveTargetFromNode(node *state.ManagedNode) (target.Target, error) {
	switch node.Target.Type {
	case "ssh":
		t := target.NewSSHTarget(node.Target.Host, node.Target.Port, node.Target.User, node.Target.IdentityFile)
		if err := t.Connect(); err != nil {
			return nil, err
		}
		return t, nil
	default:
		return target.NewLocalTarget(), nil
	}
}

func inspectAllNodes(ctx context.Context, _ *mcp.CallToolRequest, _ emptyArgs) (*mcp.CallToolResult, any, error) {
	store, err := state.NewStore(paths.State())
	if err != nil {
		return errResult(err)
	}
	st, err := store.Load()
	if err != nil {
		return errResult(err)
	}
	rows := make([]map[string]any, 0, len(st.Nodes))
	for _, n := range st.Nodes {
		row := map[string]any{
			"name":       n.Name,
			"status":     n.Status,
			"runtime":    n.Runtime,
			"network":    n.Network,
			"is_private": intent.IsPrivate(n.Network),
		}
		// Endpoints: we have HTTPPort/GRPCPort persisted in state from
		// apply; cmd/inspect.go's enrichment with container_ip
		// requires a live docker query, skipped for the static MCP
		// version. Agents that need live container IPs should call
		// `trond inspect` via shell or wait for the runtime probe
		// extraction.
		eps := map[string]string{}
		if n.HTTPPort != 0 {
			eps["http"] = httpURL(n.HTTPPort)
		}
		if n.GRPCPort != 0 {
			eps["grpc"] = grpcAddr(n.GRPCPort)
		}
		if len(eps) > 0 {
			row["endpoints"] = eps
		}
		if len(n.Labels) > 0 {
			row["labels"] = n.Labels
		}
		rows = append(rows, row)
	}
	return jsonResult(map[string]any{"nodes": rows})
}
