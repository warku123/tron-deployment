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

// TestRunStatus_EmitsHealthyAndLogs is the A1 status wire test: `status
// -o json` must always carry `healthy` (seeded false, fail-safe) and a
// runtime-discriminated `logs` locator. Uses a stopped jar node so no
// live probe and no docker call fires (hermetic, fast).
func TestRunStatus_EmitsHealthyAndLogs(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{
		Version: 1,
		Nodes: []state.ManagedNode{{
			Name:        "sr0",
			Status:      "stopped", // skip live probe
			Runtime:     "jar",     // skip docker container_id call
			Network:     "private",
			LastApplied: time.Now().UTC(),
		}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c := &cobra.Command{}
	c.Flags().String("output", "json", "")
	c.SetContext(context.Background())

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	runErr := runStatus(c, []string{"sr0"})
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if runErr != nil {
		t.Fatalf("runStatus: %v", runErr)
	}

	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("status JSON parse: %v\n%s", err, out)
	}

	// healthy must be PRESENT and false (node stopped → probe skipped → the
	// seeded fail-safe value survives). Absence would let an agent misread
	// "field missing" as "can't tell".
	h, ok := m["healthy"]
	if !ok {
		t.Fatalf("status missing `healthy`; A1 requires it always present")
	}
	if h != false {
		t.Errorf("healthy = %v; want false for a stopped node", h)
	}

	logs, ok := m["logs"].(map[string]any)
	if !ok {
		t.Fatalf("status missing `logs` object; got %v", m["logs"])
	}
	if logs["runtime"] != "jar" {
		t.Errorf("logs.runtime = %v; want jar", logs["runtime"])
	}
	if logs["unit"] != "tron-sr0.service" {
		t.Errorf("logs.unit = %v; want tron-sr0.service", logs["unit"])
	}
}

// TestManifestForNode_EmitsLogs covers the `trond inspect` manifest's A1
// logs locator for both runtimes. Uses jar + an ssh-target docker node so
// the local-docker enrichment guard (Runtime==docker && Target==local) is
// never hit — no real docker daemon call fires, keeping the test hermetic.
func TestManifestForNode_EmitsLogs(t *testing.T) {
	cases := []struct {
		node    state.ManagedNode
		wantRT  string
		wantKey string // a key the descriptor must carry
		wantVal string
	}{
		{state.ManagedNode{Name: "sr0", Runtime: "jar"}, "jar", "unit", "tron-sr0.service"},
		{state.ManagedNode{Name: "fn0", Runtime: "docker", Target: state.NodeTarget{Type: "ssh"}}, "docker", "path", "/java-tron/logs/tron.log"},
	}
	for _, tc := range cases {
		entry := manifestForNode(context.Background(), &tc.node)
		logs, ok := entry["logs"].(map[string]any)
		if !ok {
			t.Fatalf("%s: manifest missing `logs`; got %v", tc.node.Name, entry["logs"])
		}
		if logs["runtime"] != tc.wantRT {
			t.Errorf("%s: logs.runtime = %v; want %s", tc.node.Name, logs["runtime"], tc.wantRT)
		}
		if logs[tc.wantKey] != tc.wantVal {
			t.Errorf("%s: logs.%s = %v; want %s", tc.node.Name, tc.wantKey, logs[tc.wantKey], tc.wantVal)
		}
	}
}
