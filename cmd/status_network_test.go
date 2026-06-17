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

// TestRunStatus_EmitsNetworkAndIsPrivate is the C1 status wire test: the
// agent's PRIMARY query path. `status -o json` must carry `network` +
// `is_private` from recorded state. Node is stopped so the live probe is
// skipped (no docker needed).
func TestRunStatus_EmitsNetworkAndIsPrivate(t *testing.T) {
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
			Name:        "priv",
			Status:      "stopped", // skip live probe
			Runtime:     "docker",
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
	runErr := runStatus(c, []string{"priv"})
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
	if m["network"] != "private" {
		t.Errorf("status network = %v; want private", m["network"])
	}
	if m["is_private"] != true {
		t.Errorf("status is_private = %v; want true", m["is_private"])
	}
}
