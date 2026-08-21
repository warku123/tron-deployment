package apply

import (
	"github.com/tronprotocol/tron-deployment/internal/intent"
	"os"
)

// ResolveEnvVars resolves the legacy witness-key environment reference.
func ResolveEnvVars(node *intent.NodeSpec) map[string]string {
	env := map[string]string{}
	if node.WitnessKeyEnv != "" {
		if value := os.Getenv(node.WitnessKeyEnv); value != "" {
			env[node.WitnessKeyEnv] = value
		}
	}
	return env
}
