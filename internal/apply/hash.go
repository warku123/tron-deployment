package apply

import (
	"crypto/sha256"
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/tronprotocol/tron-deployment/internal/intent"
	"github.com/tronprotocol/tron-deployment/internal/state"
)

// EffectiveIntentHash hashes the fully-resolved intent used for deployment.
func EffectiveIntentHash(effective []byte) string {
	h := sha256.New()
	h.Write([]byte("intent-hash-v2\x00"))
	h.Write(effective)
	return fmt.Sprintf("v2:%x", h.Sum(nil))
}

// LegacyIntentHashMatches accepts hashes written before the versioned hash
// format, while ensuring monitoring state did not change underneath them.
func LegacyIntentHashMatches(raw, canonical []byte, existing *state.ManagedNode) bool {
	if existing == nil {
		return false
	}
	stored := existing.IntentHash
	if stored == "" || len(stored) > 3 && stored[:3] == "v2:" {
		return false
	}
	var rawIntent, canonicalIntent intent.Intent
	if err := yaml.Unmarshal(raw, &rawIntent); err != nil {
		return false
	}
	if err := yaml.Unmarshal(canonical, &canonicalIntent); err != nil {
		return false
	}
	if IntentHashFromBytes(raw) == stored {
		return monitoringEnabled(rawIntent.Monitoring) == monitoringEnabled(canonicalIntent.Monitoring) &&
			monitoringEnabled(canonicalIntent.Monitoring) == recordedMonitoringEnabled(existing)
	}
	return IntentHashFromBytes(canonical) == stored &&
		monitoringEnabled(canonicalIntent.Monitoring) == recordedMonitoringEnabled(existing)
}

func monitoringEnabled(m *intent.Monitoring) bool {
	return m != nil && m.Enabled != nil && *m.Enabled
}

func recordedMonitoringEnabled(existing *state.ManagedNode) bool {
	return existing != nil && existing.Monitoring != nil && existing.Monitoring.Enabled
}

// RestoreAutoPorts restores all persisted auto-allocated ports into an intent.
func RestoreAutoPorts(node *intent.NodeSpec, existing *state.ManagedNode) {
	if existing.HTTPPort != 0 {
		node.Ports.HTTP = existing.HTTPPort
	}
	if existing.GRPCPort != 0 {
		node.Ports.GRPC = existing.GRPCPort
	}
	if existing.P2PPort != 0 {
		node.Ports.P2P = existing.P2PPort
	}
	if existing.MetricsPort != 0 {
		node.Ports.Metrics = existing.MetricsPort
	}
	if existing.SolidityHTTPPort != 0 {
		node.Ports.SolidityHTTP = existing.SolidityHTTPPort
	}
	if existing.SolidityGRPCPort != 0 {
		node.Ports.SolidityGRPC = existing.SolidityGRPCPort
	}
	if existing.JSONRPCPort != 0 {
		node.Ports.JSONRPC = existing.JSONRPCPort
	}
}
