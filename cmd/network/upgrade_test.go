package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerifyNodeInvokesVerifyForEachProjectedNode(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "network.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: net
network: private
target:
  type: local
nodes:
  - type: fullnode
    version: latest
  - type: witness
    version: latest
    witness_key_env: SR_KEY
`), 0600); err != nil {
		t.Fatal(err)
	}

	oldRunChild := runChild
	defer func() { runChild = oldRunChild }()
	var intents []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		for i := range argv {
			if argv[i] == "--intent" && i+1 < len(argv) {
				data, err := os.ReadFile(argv[i+1])
				if err != nil {
					t.Fatalf("read projected intent: %v", err)
				}
				intents = append(intents, string(data))
			}
		}
		return nil
	}

	for i, node := range []string{"net-node0", "net-node1"} {
		if err := verifyNode(context.Background(), "trond", node, intentPath, time.Second); err != nil {
			t.Fatalf("verifyNode(%s): %v", node, err)
		}
		wantName := []string{"name: net-node0", "name: net-node1"}[i]
		if !strings.Contains(intents[i], wantName) {
			t.Fatalf("projected intent %q does not target %s", intents[i], node)
		}
	}
	if len(intents) != 2 {
		t.Fatalf("verify calls = %d, want 2", len(intents))
	}
}

func TestVerifyNodeFailureReturnedForFailingNode(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "network.yaml")
	if err := os.WriteFile(intentPath, []byte(`name: net
network: private
target:
  type: local
nodes:
  - type: fullnode
    version: latest
  - type: fullnode
    version: latest
`), 0600); err != nil {
		t.Fatal(err)
	}

	oldRunChild := runChild
	defer func() { runChild = oldRunChild }()
	var calls []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		calls = append(calls, strings.Join(argv, " "))
		if len(calls) == 2 {
			return errors.New("verify node net-node1 failed")
		}
		return nil
	}
	if err := verifyNode(context.Background(), "trond", "net-node0", intentPath, time.Second); err != nil {
		t.Fatal(err)
	}
	err := verifyNode(context.Background(), "trond", "net-node1", intentPath, time.Second)
	if err == nil || !strings.Contains(err.Error(), "verify node net-node1 failed") {
		t.Fatalf("error = %v, want failing node verify error", err)
	}
	if len(calls) != 2 {
		t.Fatalf("verify calls = %d, want 2", len(calls))
	}
}

func TestFinishWithFailureRollsBackNodeWhoseVerifyFailed(t *testing.T) {
	oldAutoRollback := upgradeAutoRollback
	oldRunChild := runChild
	defer func() {
		upgradeAutoRollback = oldAutoRollback
		runChild = oldRunChild
	}()
	upgradeAutoRollback = true
	var rollbackNodes []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		if len(argv) >= 2 && argv[0] == "rollback" {
			rollbackNodes = append(rollbackNodes, argv[1])
		}
		return nil
	}

	err := finishWithFailure(context.Background(), "", "net", "trond",
		[]upgradeStep{{Node: "net-node0", Phase: "upgrade", Status: "ok"},
			{Node: "net-node0", Phase: "verify", Status: "failed"}},
		[]string{"net-node0"}, time.Now(), "net-node0", errors.New("verify failed"))
	if err == nil || !strings.Contains(err.Error(), "net-node0") {
		t.Fatalf("error = %v, want failed node", err)
	}
	if len(rollbackNodes) != 1 || rollbackNodes[0] != "net-node0" {
		t.Fatalf("rollback nodes = %v, want [net-node0]", rollbackNodes)
	}
}
