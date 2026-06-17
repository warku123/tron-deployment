package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// seedNode writes a single-node state file into an isolated base dir and
// returns it. Mirrors the C1/A1 wire-test pattern used in cmd/.
func seedNode(t *testing.T, n state.ManagedNode) {
	t.Helper()
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: []state.ManagedNode{n}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// TestStatusForNode_EmitsHealthyAndLogs is the A1 MCP-status wire test:
// healthy seeded false (always present) + the runtime-discriminated logs
// locator. Stopped jar node → no target resolution, no docker call.
func TestStatusForNode_EmitsHealthyAndLogs(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "sr0", Status: "stopped", Runtime: "jar",
		Network: "private", LastApplied: time.Now().UTC(),
	})

	_, v, err := statusForNode(context.Background(), nil, nodeArg{Name: "sr0"})
	if err != nil {
		t.Fatalf("statusForNode: %v", err)
	}
	out, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result is not a map: %T", v)
	}
	h, ok := out["healthy"]
	if !ok {
		t.Fatalf("MCP status missing `healthy`; A1 requires it always present")
	}
	if h != false {
		t.Errorf("healthy = %v; want false for a stopped node", h)
	}
	logs, ok := out["logs"].(map[string]any)
	if !ok {
		t.Fatalf("MCP status missing `logs` object; got %v", out["logs"])
	}
	if logs["runtime"] != "jar" || logs["unit"] != "tron-sr0.service" {
		t.Errorf("logs = %v; want jar/tron-sr0.service", logs)
	}
}

// TestInspectAllNodes_EmitsLogs verifies the MCP inspect manifest carries
// the (static) logs locator per node.
func TestInspectAllNodes_EmitsLogs(t *testing.T) {
	seedNode(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "docker",
		Network: "private", HTTPPort: 8090, LastApplied: time.Now().UTC(),
	})

	_, v, err := inspectAllNodes(context.Background(), nil, emptyArgs{})
	if err != nil {
		t.Fatalf("inspectAllNodes: %v", err)
	}
	top, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result is not a map: %T", v)
	}
	rows, ok := top["nodes"].([]map[string]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("nodes shape unexpected: %T %v", top["nodes"], top["nodes"])
	}
	logs, ok := rows[0]["logs"].(map[string]any)
	if !ok {
		t.Fatalf("inspect row missing `logs`; got %v", rows[0]["logs"])
	}
	if logs["runtime"] != "docker" || logs["path"] != "/java-tron/logs/tron.log" {
		t.Errorf("logs = %v; want docker shape", logs)
	}
}
