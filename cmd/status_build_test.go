package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// statusJSON seeds a single node, runs `status <name> -o json` (stopped jar
// → no probe, no docker), and returns the parsed map.
func statusJSON(t *testing.T, node state.ManagedNode) map[string]any {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: []state.ManagedNode{node}}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c := &cobra.Command{}
	c.Flags().String("output", "json", "")
	c.SetContext(context.Background())
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	runErr := runStatus(c, []string{node.Name})
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("runStatus: %v", runErr)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out)
	}
	return m
}

// TestRunStatus_EmitsBuildIdentity is the B1 wire test: a source-built node
// surfaces build_cache_key + a clean build_revision, so an agent knows which
// commit is running.
func TestRunStatus_EmitsBuildIdentity(t *testing.T) {
	m := statusJSON(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "jar",
		BuildCacheKey: "abc123def456-b1234abcd", LastApplied: time.Now().UTC(),
	})
	if m["build_cache_key"] != "abc123def456-b1234abcd" {
		t.Errorf("build_cache_key = %v; want the full key", m["build_cache_key"])
	}
	if m["build_revision"] != "abc123def456" {
		t.Errorf("build_revision = %v; want abc123def456 (the cache-key prefix)", m["build_revision"])
	}
}

// TestRunStatus_OmitsBuildIdentityForPrebuilt: a node that consumed a
// pre-built image/jar (no cache key) must not carry build fields at all.
func TestRunStatus_OmitsBuildIdentityForPrebuilt(t *testing.T) {
	m := statusJSON(t, state.ManagedNode{
		Name: "fn0", Status: "stopped", Runtime: "jar",
		LastApplied: time.Now().UTC(),
	})
	if _, ok := m["build_cache_key"]; ok {
		t.Errorf("build_cache_key must be absent for a pre-built node; got %v", m["build_cache_key"])
	}
	if _, ok := m["build_revision"]; ok {
		t.Errorf("build_revision must be absent for a pre-built node; got %v", m["build_revision"])
	}
}
