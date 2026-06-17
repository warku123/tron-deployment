package network

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// seedNetwork writes a "<network>-nodeN" set to an isolated state dir and
// turns the gate on (restored after). The multi-node guard fires before any
// docker/fs work, so refusal is hermetic.
func seedNetwork(t *testing.T, network string, networks ...string) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	nodes := make([]state.ManagedNode, 0, len(networks))
	for i, net := range networks {
		nodes = append(nodes, state.ManagedNode{
			Name:        network + "-node" + string(rune('0'+i)),
			Status:      "running",
			Runtime:     "docker",
			Network:     net,
			Target:      state.NodeTarget{Type: "local"},
			LastApplied: time.Now().UTC(),
		})
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: nodes}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := guard.FlagValue
	guard.FlagValue = true
	t.Cleanup(func() { guard.FlagValue = old })
}

func wantPrivReq(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected PRIVATE_NETWORK_REQUIRED, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.Code != "PRIVATE_NETWORK_REQUIRED" || se.ExitCode != output.ExitValidationError {
		t.Errorf("got code=%q exit=%d; want PRIVATE_NETWORK_REQUIRED/%d", se.Code, se.ExitCode, output.ExitValidationError)
	}
}

func newNetCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().String("output", "json", "")
	return c
}

// TestNetworkDestroy_RefusesNonPrivate: destroy refuses (before removing
// anything) when any node in the network is non-private.
func TestNetworkDestroy_RefusesNonPrivate(t *testing.T) {
	seedNetwork(t, "rig", "private", "mainnet")
	old := destroyConfirm
	destroyConfirm = "rig" // satisfy the confirm gate; identity comes from it
	t.Cleanup(func() { destroyConfirm = old })
	wantPrivReq(t, runDestroy(newNetCmd(), nil))
}

// TestNetworkUpgrade_RefusesNonPrivate: network upgrade refuses (before any
// stop/upgrade) when any node in the network is non-private.
func TestNetworkUpgrade_RefusesNonPrivate(t *testing.T) {
	seedNetwork(t, "rig", "private", "mainnet")
	wantPrivReq(t, runUpgrade(newNetCmd(), []string{"rig"}))
}
