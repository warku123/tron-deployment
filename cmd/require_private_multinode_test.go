package cmd

import (
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/guard"
	"github.com/tronprotocol/tron-deployment/internal/paths"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// seedNodes writes several nodes to an isolated state dir and turns the
// gate on (restored after). The multi-node guard fires before any docker
// work, so refusal is hermetic.
func seedNodes(t *testing.T, nodes ...state.ManagedNode) {
	t.Helper()
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })
	store, err := state.NewStore(paths.State())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Save(&state.DeploymentState{Version: 1, Nodes: nodes}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := guard.FlagValue
	guard.FlagValue = true
	t.Cleanup(func() { guard.FlagValue = old })
}

func dockerNode(name, network string) state.ManagedNode {
	return state.ManagedNode{
		Name: name, Status: "running", Runtime: "docker",
		Network: network, Target: state.NodeTarget{Type: "local"},
		LastApplied: time.Now().UTC(),
	}
}

// TestChaosDisconnect_RefusesNonPrivate: disconnect refuses (before any
// docker work) when either endpoint is non-private, naming the offender.
func TestChaosDisconnect_RefusesNonPrivate(t *testing.T) {
	seedNodes(t, dockerNode("a", "private"), dockerNode("b", "mainnet"))
	err := runDisconnect(newCmd(), []string{"a", "b"})
	wantPrivateRequired(t, err)
}

// TestChaosPartition_RefusesNonPrivate: partition guards every node in the
// --groups spec up front, so one non-private member refuses cleanly with no
// partial application.
func TestChaosPartition_RefusesNonPrivate(t *testing.T) {
	seedNodes(t, dockerNode("a", "private"), dockerNode("b", "private"),
		dockerNode("c", "mainnet"), dockerNode("d", "private"))
	old := partitionGroups
	partitionGroups = "a,b|c,d"
	t.Cleanup(func() { partitionGroups = old })
	wantPrivateRequired(t, runPartition(newCmd(), nil))
}
