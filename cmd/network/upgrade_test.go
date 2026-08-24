package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/tronprotocol/tron-deployment/internal/state"
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

	err := finishWithFailure(context.Background(), "", "net", "trond", nil,
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

func TestRunChildCommandRejectsNonJSONOutput(t *testing.T) {
	err := runChildCommand(context.Background(), "/bin/sh", "-c", "printf not-json")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v, want invalid JSON", err)
	}
}

func TestRunChildCommandRejectsNonObjectJSON(t *testing.T) {
	for _, value := range []string{"null", "1", "[]"} {
		t.Run(value, func(t *testing.T) {
			err := runChildCommand(context.Background(), "/bin/sh", "-c", "printf '%s' '"+value+"'")
			if err == nil || !strings.Contains(err.Error(), "not an object") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestRunChildCommandMarksTruncatedStderr(t *testing.T) {
	err := runChildCommand(context.Background(), "python3", "-c", "import sys; sys.stderr.write('x'*1100001); sys.exit(1)")
	if err == nil || !strings.Contains(err.Error(), "[truncated at 1MiB]") {
		t.Fatalf("error = %v", err)
	}
}

func TestLimitedBufferTracksOverflow(t *testing.T) {
	var b limitedBuffer
	_, _ = b.Write(make([]byte, (1<<20)+1))
	if !b.limited || b.total <= 1<<20 || b.Len() != 1<<20 {
		t.Fatalf("buffer state limited=%v total=%d len=%d", b.limited, b.total, b.Len())
	}
}

func TestFinishWithFailureDoesNotPassUpgradeJarArgumentsToRollback(t *testing.T) {
	oldAuto, oldURL, oldSHA, oldRun := upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256, runChild
	defer func() {
		upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256, runChild = oldAuto, oldURL, oldSHA, oldRun
	}()
	upgradeAutoRollback, upgradeJarURL, upgradeJarSHA256 = true, "https://example/jar", "abc"
	var got []string
	runChild = func(_ context.Context, _ string, argv ...string) error {
		got = append([]string(nil), argv...)
		return nil
	}
	_ = finishWithFailure(context.Background(), "", "net", "trond", nil, nil, []string{"net-node0"}, time.Now(), "net-node1", errors.New("failed"))
	want := []string{"rollback", "net-node0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback args = %v, want %v", got, want)
	}
}

func TestRunNetworkChildOnlyInjectsUpgradeEnvForRollback(t *testing.T) {
	st := &state.DeploymentState{Nodes: []state.ManagedNode{{Name: "net-node0", Runtime: "jar"}}}
	old := runChildWithEnv
	oldChild := runChild
	defer func() { runChildWithEnv = old; runChild = oldChild }()
	var envCalls [][]string
	runChildWithEnv = func(_ context.Context, _ string, env []string, _ ...string) error {
		envCalls = append(envCalls, append([]string(nil), env...))
		return nil
	}
	runChild = func(context.Context, string, ...string) error { return nil }
	if err := runNetworkChild(context.Background(), "trond", st, "net-node0", "upgrade", "net-node0"); err != nil {
		t.Fatal(err)
	}
	if len(envCalls) != 1 || !reflect.DeepEqual(envCalls[0], []string{networkUpgradePreserveEnv}) {
		t.Fatalf("upgrade env = %v, want [%s]", envCalls, networkUpgradePreserveEnv)
	}
	if err := runNetworkChild(context.Background(), "trond", st, "net-node0", "rollback", "net-node0"); err != nil {
		t.Fatal(err)
	}
	if len(envCalls) != 2 || !reflect.DeepEqual(envCalls[1], []string{networkUpgradeRestoreEnv}) {
		t.Fatalf("rollback env = %v, want [%s]", envCalls, networkUpgradeRestoreEnv)
	}
}
