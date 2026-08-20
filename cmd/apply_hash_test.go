package cmd

import (
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/apply"
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/state"
	"gopkg.in/yaml.v3"
)

func TestLegacyIntentHashMatchesRawHash(t *testing.T) {
	raw := []byte("name: n\nnetwork: private\n")
	parsed := &intent.Intent{Name: "n", Network: "private"}
	canonical, _ := yaml.Marshal(parsed)
	if !apply.LegacyIntentHashMatches(raw, canonical, &state.ManagedNode{IntentHash: apply.IntentHashFromBytes(raw)}) {
		t.Fatal("legacy raw hash not accepted")
	}
}

func TestVersionedIntentHashIsStable(t *testing.T) {
	data := []byte("effective intent")
	if apply.EffectiveIntentHash(data) != apply.EffectiveIntentHash(data) {
		t.Fatal("versioned hash is unstable")
	}
	if apply.EffectiveIntentHash(data) == string(data) {
		t.Fatal("hash did not version/encode input")
	}
}

func TestLegacyRawHashRejectsMonitoringChange(t *testing.T) {
	raw := []byte("name: n\nnetwork: private\nmonitoring:\n  enabled: false\n")
	parsed := &intent.Intent{Name: "n", Network: "private", Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(true)}}
	canonical, _ := yaml.Marshal(parsed)
	if apply.LegacyIntentHashMatches(raw, canonical, &state.ManagedNode{IntentHash: apply.IntentHashFromBytes(raw)}) {
		t.Fatal("monitoring change treated as legacy no-op")
	}
}

func TestLegacyRawHashRejectsMissingRecordedMonitoring(t *testing.T) {
	raw := []byte("name: n\nnetwork: private\nmonitoring:\n  enabled: true\n")
	parsed := &intent.Intent{Name: "n", Network: "private", Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(true)}}
	canonical, _ := yaml.Marshal(parsed)
	if apply.LegacyIntentHashMatches(raw, canonical, &state.ManagedNode{IntentHash: apply.IntentHashFromBytes(raw)}) {
		t.Fatal("missing recorded monitoring treated as legacy no-op")
	}
}

func TestLegacyRawHashRejectsRecordedMonitoringWhenCurrentDisabled(t *testing.T) {
	raw := []byte("name: n\nnetwork: private\nmonitoring:\n  enabled: false\n")
	parsed := &intent.Intent{Name: "n", Network: "private", Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(false)}}
	existing := &state.ManagedNode{Monitoring: &state.MonitoringState{Enabled: true}}
	canonical, _ := yaml.Marshal(parsed)
	existing.IntentHash = apply.IntentHashFromBytes(raw)
	if apply.LegacyIntentHashMatches(raw, canonical, existing) {
		t.Fatal("recorded enabled monitoring treated as legacy no-op when current intent is disabled")
	}
}

func TestLegacyCanonicalHashRejectsRecordedMonitoringWhenCurrentDisabled(t *testing.T) {
	parsed := &intent.Intent{Name: "n", Network: "private", Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(false)}}
	canonical, err := yaml.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	existing := &state.ManagedNode{Monitoring: &state.MonitoringState{Enabled: true}}
	existing.IntentHash = apply.IntentHashFromBytes(canonical)
	if apply.LegacyIntentHashMatches([]byte("not-the-canonical-input"), canonical, existing) {
		t.Fatal("canonical legacy hash treated recorded enabled monitoring as no-op when current intent is disabled")
	}
}

func TestRestoreAutoPortsPreservesMissingLegacyPorts(t *testing.T) {
	n := &intent.NodeSpec{}
	n.Ports.HTTP, n.Ports.GRPC, n.Ports.SolidityHTTP, n.Ports.SolidityGRPC, n.Ports.JSONRPC, n.Ports.P2P, n.Ports.Metrics = 10, 11, 12, 13, 14, 15, 16
	apply.RestoreAutoPorts(n, &state.ManagedNode{HTTPPort: 1, GRPCPort: 2, P2PPort: 6, MetricsPort: 7})
	got := []int{n.Ports.HTTP, n.Ports.GRPC, n.Ports.SolidityHTTP, n.Ports.SolidityGRPC, n.Ports.JSONRPC, n.Ports.P2P, n.Ports.Metrics}
	want := []int{1, 2, 12, 13, 14, 6, 7}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
}

func TestRestoreAutoPortsAllSeven(t *testing.T) {
	n := &intent.NodeSpec{}
	s := &state.ManagedNode{HTTPPort: 1, GRPCPort: 2, SolidityHTTPPort: 3, SolidityGRPCPort: 4, JSONRPCPort: 5, P2PPort: 6, MetricsPort: 7}
	apply.RestoreAutoPorts(n, s)
	got := []int{n.Ports.HTTP, n.Ports.GRPC, n.Ports.SolidityHTTP, n.Ports.SolidityGRPC, n.Ports.JSONRPC, n.Ports.P2P, n.Ports.Metrics}
	want := []int{1, 2, 3, 4, 5, 6, 7}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
}
