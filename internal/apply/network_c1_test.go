package apply

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// TestApply_RequirePrivate_RefusesInCore pins the C1 guard at the CORE
// (apply.Apply), not just cmd — so network create + MCP inherit it. A
// non-private intent with RequirePrivate must refuse with
// PRIVATE_NETWORK_REQUIRED / exit 2 before any deploy.
func TestApply_RequirePrivate_RefusesInCore(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	in := &intent.Intent{
		Name:    "m",
		Network: "mainnet",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes:   []intent.NodeSpec{{Type: "fullnode", Version: "4.8.1"}},
	}
	store, st := freshStore(t)
	_, err := Apply(context.Background(), Options{
		Intent: in, Target: &fakeTarget{}, Store: store, State: st,
		IntentHash: "h", RequirePrivate: true,
	})
	if err == nil {
		t.Fatal("expected PRIVATE_NETWORK_REQUIRED refusal, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) || se.Code != "PRIVATE_NETWORK_REQUIRED" {
		t.Errorf("want PRIVATE_NETWORK_REQUIRED, got %v", err)
	}
	if len(st.Nodes) != 0 {
		t.Error("refused apply must not have persisted a node")
	}
}

// TestApply_PrivateNetwork_ResultAndState is the C1 happy path: applying
// a private-network intent records network="private" in state and reports
// it (plus is_private=true) in the result envelope, so an automated
// caller can confirm the rig is private. Uses the jar runtime + fakeTarget
// so no docker daemon is needed.
func TestApply_PrivateNetwork_ResultAndState(t *testing.T) {
	dir := t.TempDir()
	paths.SetBaseDir(dir)
	t.Cleanup(func() { paths.SetBaseDir("") })

	in := &intent.Intent{
		Name:    "priv-node",
		Network: "private",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
		Nodes: []intent.NodeSpec{{
			Type:        "fullnode",
			Version:     "4.8.1",
			Resources:   intent.Resources{Memory: "4G"},
			Ports:       intent.PortMapping{HTTP: 8090, GRPC: 50051},
			InstallPath: filepath.Join(dir, "install"),
		}},
	}

	store, st := freshStore(t)
	res, err := Apply(context.Background(), Options{
		Intent:         in,
		Target:         &fakeTarget{},
		Store:          store,
		State:          st,
		IntentHash:     "c1-private-hash",
		DeploymentsDir: filepath.Join(dir, "deployments"),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if res.Network != "private" {
		t.Errorf("Result.Network = %q; want private", res.Network)
	}
	if !res.IsPrivate {
		t.Error("Result.IsPrivate = false for a private node; want true")
	}

	stored, err := store.Load()
	if err != nil {
		t.Fatalf("reload state: %v", err)
	}
	if len(stored.Nodes) != 1 {
		t.Fatalf("expected 1 stored node; got %d", len(stored.Nodes))
	}
	if stored.Nodes[0].Network != "private" {
		t.Errorf("state did not persist network=private; got %q", stored.Nodes[0].Network)
	}
}
