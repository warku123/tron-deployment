package render

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

// NetworkTemplate maps network names to their base config template file.
// "system-test" is the canonical private-chain template used by the
// tronprotocol/system-test test suite — selecting it gives a single-SR
// private network with stest's genesis, committee flags, and 5-minute
// maintenance interval, sourced from stest's release_workflow branch.
var NetworkTemplate = map[string]string{
	"mainnet":     "main_net_config.conf",
	"nile":        "test_net_config.conf",
	"private":     "private_net_config.conf",
	"system-test": "system-test.conf",
}

// RenderHOCON loads the base template for the network and applies intent-driven overrides.
// Returns the final HOCON config as a string. templateDir may be empty, in
// which case the embedded template is used.
//
// Override layering (last write wins, per HOCON spec):
//
//  1. Per-key line-level rewrites for ports + features. These keep the
//     surrounding template comments and structure intact so the output is
//     still legible to a human.
//  2. An appended "trond overrides" block carrying everything from
//     network_overrides + witness_key + config_overrides. HOCON merges by
//     dotted-key, so anything written here trumps earlier values.
//
// Two layers exist because some keys (ports) need to stay in their original
// place to keep the file diff-friendly, while bulk overrides are simpler
// and safer to express as a final-section append.
func RenderHOCON(templateDir string, i *intent.Intent, node *intent.NodeSpec) (string, error) {
	data, err := LoadTemplate(templateDir, i.Network)
	if err != nil {
		return "", err
	}

	config := string(data)

	// 1. Targeted line-level rewrites.
	config = applyPortOverrides(config, node)
	config = applyFeatureOverrides(config, node)

	// 2. Trailing override block (network_overrides + witness_key + config_overrides).
	if appendix := renderHOCONAppendix(node); appendix != "" {
		if !strings.HasSuffix(config, "\n") {
			config += "\n"
		}
		config += "\n" + appendix
	}

	return config, nil
}

// renderHOCONAppendix produces the "trond overrides" block. Returns an
// empty string when nothing is configured so the rendered HOCON stays
// identical to today's output for users who don't use the new fields.
//
// Witness-key handling: java-tron parses HOCON via typesafe-config but
// does NOT enable environment-variable substitution — `${VAR}` is treated
// as an internal config-reference and fails when the referenced key
// doesn't exist (the witness silently shuts down with "private key must
// be 64 hex string, actual: 9", that 9 being the literal length of
// `${SR_KEY}`). So we inline the env value at render time. The resulting
// .conf file holds the secret in cleartext; we protect it via the
// existing 0600 perms on the deployment dir and don't echo it back to
// stdout.
func renderHOCONAppendix(node *intent.NodeSpec) string {
	var lines []string

	// --- network_overrides ---
	no := &node.NetworkOverrides
	if no.Seeds != nil {
		lines = append(lines, "seed.node.ip.list = "+hoconStringList(*no.Seeds))
	}
	if no.ActivePeers != nil {
		lines = append(lines, "node.active = "+hoconStringList(*no.ActivePeers))
	}
	if no.PassivePeers != nil {
		lines = append(lines, "node.passive = "+hoconStringList(*no.PassivePeers))
	}
	if no.P2PVersion != nil {
		lines = append(lines, fmt.Sprintf("node.p2p.version = %d", *no.P2PVersion))
	}
	if no.Discovery != nil {
		lines = append(lines, fmt.Sprintf("node.discovery.enable = %t", *no.Discovery))
	}
	if no.MaxConnections != nil {
		lines = append(lines, fmt.Sprintf("node.maxConnections = %d", *no.MaxConnections))
	}
	if no.MaxActiveSameIP != nil {
		lines = append(lines, fmt.Sprintf("node.maxActiveNodesWithSameIp = %d", *no.MaxActiveSameIP))
	}
	if no.NeedSyncCheck != nil {
		lines = append(lines, fmt.Sprintf("block.needSyncCheck = %t", *no.NeedSyncCheck))
	}

	// --- witness_key ---
	if node.Type == "witness" {
		// Resolve from either the structured block or the legacy field.
		envName := ""
		keystore := ""
		var accountAddress string
		if node.WitnessKey != nil {
			envName = node.WitnessKey.PrivateKeyEnv
			keystore = node.WitnessKey.KeystorePath
			accountAddress = node.WitnessKey.AccountAddress
		}
		if envName == "" {
			envName = node.WitnessKeyEnv
		}

		switch {
		case envName != "":
			// Inline the resolved value at render time. typesafe-config
			// won't substitute ${ENV} for us, so we have to do it here.
			// If the env is unset at render time we emit a single-quoted
			// placeholder that java-tron will reject loudly — better than
			// silently rendering an empty key.
			val := os.Getenv(envName)
			if val == "" {
				val = "<UNSET:" + envName + ">"
			}
			lines = append(lines, fmt.Sprintf(`localwitness = [%q]`, val))
		case keystore != "":
			lines = append(lines, fmt.Sprintf("localwitnesskeystore = [%q]", keystore))
		}
		if accountAddress != "" {
			lines = append(lines, fmt.Sprintf("localWitnessAccountAddress = %q", accountAddress))
		}
	}

	// --- config_overrides (sorted for determinism) ---
	if len(node.ConfigOverrides) > 0 {
		keys := make([]string, 0, len(node.ConfigOverrides))
		for k := range node.ConfigOverrides {
			keys = append(keys, k)
		}
		sortStrings(keys)
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", k, hoconValue(node.ConfigOverrides[k])))
		}
	}

	if len(lines) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("# === trond overrides (last-write-wins) ===\n")
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteString("\n")
	}
	return sb.String()
}

// hoconStringList serialises a Go []string as a HOCON list of quoted strings.
func hoconStringList(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// hoconValue renders an arbitrary intent value (from config_overrides) as
// the closest HOCON literal. Strings are double-quoted; numbers and bools
// pass through; lists / maps are JSON-serialised, which HOCON accepts.
func hoconValue(v any) string {
	switch x := v.(type) {
	case string:
		return fmt.Sprintf("%q", x)
	case bool:
		return fmt.Sprintf("%t", x)
	case int, int32, int64, float32, float64:
		return fmt.Sprintf("%v", x)
	default:
		// Fall back to %v which works for slices/maps (HOCON accepts
		// JSON-style for those).
		return fmt.Sprintf("%v", x)
	}
}

// sortStrings is a thin wrapper around sort.Strings so the appendix
// renderer reads cleanly. (Earlier this file hand-rolled an insertion
// sort to "avoid importing sort twice"; Go imports are per-file so the
// rationale was wrong.)
func sortStrings(s []string) {
	sort.Strings(s)
}

// applyPortOverrides patches port settings in the HOCON config.
func applyPortOverrides(config string, node *intent.NodeSpec) string {
	ports := node.Ports

	if ports.HTTP != 0 {
		config = replaceHOCONValue(config, "fullNodePort", fmt.Sprintf("%d", ports.HTTP))
	}
	if ports.GRPC != 0 {
		config = replaceRPCPort(config, ports.GRPC)
	}
	if ports.SolidityHTTP != 0 {
		config = replaceHOCONValue(config, "solidityPort", fmt.Sprintf("%d", ports.SolidityHTTP))
	}
	if ports.P2P != 0 {
		config = replaceListenPort(config, ports.P2P)
	}

	return config
}

// applyFeatureOverrides enables/disables features in the HOCON config.
func applyFeatureOverrides(config string, node *intent.NodeSpec) string {
	features := node.Features

	if features.JSONRPC != nil && *features.JSONRPC {
		// Ensure jsonrpc block has httpFullNodeEnable = true
		config = ensureJSONRPCEnabled(config)
	}

	return config
}

// replaceHOCONValue replaces a simple key = value pattern in HOCON.
func replaceHOCONValue(config, key, newValue string) string {
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+" =") || strings.HasPrefix(trimmed, key+"=") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%s%s = %s", indent, key, newValue)
			break
		}
	}
	return strings.Join(lines, "\n")
}

// replaceRPCPort replaces the gRPC port in the rpc block.
func replaceRPCPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	inRPC := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "rpc {" || strings.HasPrefix(trimmed, "rpc {") {
			inRPC = true
			continue
		}
		if inRPC && strings.HasPrefix(trimmed, "port") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%sport = %d", indent, port)
			break
		}
		if inRPC && trimmed == "}" {
			inRPC = false
		}
	}
	return strings.Join(lines, "\n")
}

// replaceListenPort replaces the P2P listen port.
func replaceListenPort(config string, port int) string {
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "listen.port") {
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = fmt.Sprintf("%slisten.port = %d", indent, port)
			break
		}
	}
	return strings.Join(lines, "\n")
}

// ensureJSONRPCEnabled ensures the jsonrpc block has httpFullNodeEnable = true.
func ensureJSONRPCEnabled(config string) string {
	lines := strings.Split(config, "\n")
	inJSONRPC := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "jsonrpc") && strings.Contains(trimmed, "{") {
			inJSONRPC = true
			continue
		}
		if inJSONRPC {
			if strings.Contains(trimmed, "httpFullNodeEnable") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = fmt.Sprintf("%shttpFullNodeEnable = true", indent)
				return strings.Join(lines, "\n")
			}
			if trimmed == "}" {
				// Insert before closing brace
				indent := "    "
				lines[i] = indent + "httpFullNodeEnable = true\n" + line
				return strings.Join(lines, "\n")
			}
		}
	}
	return config
}
