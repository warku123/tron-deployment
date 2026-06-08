package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestLoadTemplate_EmbeddedFallback(t *testing.T) {
	cases := []string{"mainnet", "nile", "private"}
	for _, net := range cases {
		data, err := LoadTemplate("", net)
		if err != nil {
			t.Errorf("LoadTemplate(%q) failed: %v", net, err)
			continue
		}
		if len(data) < 100 {
			t.Errorf("LoadTemplate(%q) returned suspiciously small payload (%d bytes)", net, len(data))
		}
	}
}

func TestLoadTemplate_UnknownNetwork(t *testing.T) {
	if _, err := LoadTemplate("", "bogus"); err == nil {
		t.Error("expected error for unknown network")
	}
}

func TestRenderHOCON_PortOverrides(t *testing.T) {
	i := &intent.Intent{
		Name:    "port-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "fullnode",
		Ports: intent.PortMapping{
			HTTP: 19090,
			P2P:  28888,
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, "fullNodePort = 19090") {
		t.Error("HTTP port override not applied")
	}
	if !strings.Contains(out, "listen.port = 28888") {
		t.Error("P2P port override not applied")
	}
}

// TestRenderHOCON_JSONRPCPortAndEnable locks down the #165 fix:
// features.jsonrpc=true + ports.jsonrpc=NNNNN must produce BOTH
// `httpFullNodeEnable = true` (already worked) AND
// `httpFullNodePort = NNNNN` (the bug — was commented out in
// template and never substituted, so java-tron fell back to
// internal default 8545 while docker bound the intent port).
func TestRenderHOCON_JSONRPCPortAndEnable(t *testing.T) {
	enabled := true
	i := &intent.Intent{
		Name:    "jsonrpc-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "fullnode",
		Features: intent.Features{
			JSONRPC: &enabled,
		},
		Ports: intent.PortMapping{
			JSONRPC: 58545,
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if !strings.Contains(out, "httpFullNodeEnable = true") {
		t.Error("JSONRPC enable bit not written")
	}
	if !strings.Contains(out, "httpFullNodePort = 58545") {
		t.Errorf("JSONRPC port not wired into HOCON (#165 regression)")
	}
	// The template ships the line commented out; verify we don't have
	// BOTH the commented default AND the active override side by side.
	if strings.Contains(out, "# httpFullNodePort = 8545") &&
		strings.Contains(out, "httpFullNodePort = 58545") {
		t.Error("both commented template line and override present — render replaced wrong line")
	}
}

// TestRenderHOCON_ShadowForkRocksIntent mirrors the actual rocksdb
// shadow-fork e2e intent used on 2026-05-26 (qemu arm64 run that
// proved #166). Locks down that the WHOLE intent shape — features +
// ports + config_overrides + storage.db.engine — renders into a HOCON
// where every required wiring is present, WITHOUT needing operators
// to use config_overrides as a workaround for #165.
//
// The historical bug was: features.jsonrpc=true plus ports.jsonrpc set,
// but operators STILL had to add `"node.jsonrpc.httpFullNodePort": N`
// to config_overrides because the port wasn't propagating. After #165
// the port flows from the intent's ports.jsonrpc field directly.
func TestRenderHOCON_ShadowForkRocksIntent(t *testing.T) {
	enabled := true
	i := &intent.Intent{
		Name:    "shadow-fork-rocks",
		Network: "nile",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type: "witness",
		Features: intent.Features{
			JSONRPC: &enabled,
			Metrics: &enabled,
		},
		Ports: intent.PortMapping{
			HTTP:    58090,
			GRPC:    60051,
			JSONRPC: 58545,
			P2P:     58888,
			Metrics: 59527,
		},
		ConfigOverrides: map[string]any{
			"seed.node.ip.list":         []any{},
			"node.p2p.version":          99999,
			"node.minParticipationRate": 0,
			"storage.db.engine":         "ROCKSDB",
		},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	// Every port from the intent that has a slot in the Nile template
	// must land in the HOCON. NOTE: the Nile template (test_net_config
	// .conf) lacks a `node.metrics.prometheus` block entirely, so
	// metrics-on-nile is a separate gap not in scope here — tracked
	// elsewhere. JSON-RPC + HTTP + P2P all have slots and must wire.
	wants := map[string]string{
		"http (node.http.fullNodePort)":             "fullNodePort = 58090",
		"jsonrpc (node.jsonrpc.httpFullNodeEnable)": "httpFullNodeEnable = true",
		"jsonrpc (node.jsonrpc.httpFullNodePort)":   "httpFullNodePort = 58545",
		"p2p (node.listen.port)":                    "listen.port = 58888",
		"storage.db.engine override (rocksdb path)": "ROCKSDB",
	}
	for label, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("%s missing: expected %q in rendered HOCON", label, want)
		}
	}

	// Negative assertion: the operator-workaround used during the
	// May 25 e2e session — explicit `"node.jsonrpc.httpFullNodePort"`
	// in config_overrides — must not be REQUIRED for the port to land.
	// We deliberately do NOT set that key in ConfigOverrides above; if
	// the port still appears, #165 is fixed for the rocksdb shape too.
	if !strings.Contains(out, "httpFullNodePort = 58545") {
		t.Error("#165 regression: jsonrpc port absent despite ports.jsonrpc=58545 — " +
			"operators would need a config_overrides workaround to ship rocksdb e2e")
	}
}

// TestRenderHOCON_MetricsPort exercises the parallel fix for the
// metrics endpoint. Same shape as JSONRPC: template ships with port
// = 9527 as default; intent override must propagate.
func TestRenderHOCON_MetricsPort(t *testing.T) {
	i := &intent.Intent{
		Name:    "metrics-test",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{
		Type:  "fullnode",
		Ports: intent.PortMapping{Metrics: 59527},
	}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// The prometheus block is the only one with bare `port = N` at
	// metrics-level nesting; the value must be the override, not 9527.
	if !strings.Contains(out, "port = 59527") {
		t.Errorf("metrics port not wired into HOCON")
	}
	if strings.Contains(out, "port = 9527") {
		t.Errorf("default metrics port not replaced")
	}
}

// prometheusBlock extracts the `prometheus { ... }` sub-block from a
// rendered config so assertions can be scoped to it (the full config
// has many unrelated enable = false lines).
func prometheusBlock(t *testing.T, config string) string {
	t.Helper()
	lines := strings.Split(config, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "prometheus") && strings.Contains(l, "{") {
			for j := i + 1; j < len(lines); j++ {
				if strings.TrimSpace(lines[j]) == "}" {
					return strings.Join(lines[i:j+1], "\n")
				}
			}
		}
	}
	t.Fatalf("no prometheus block found in rendered config")
	return ""
}

// TestRenderHOCON_MetricsFeatureEnables locks the metrics-enable fix
// (symmetric to #165): features.metrics=true must flip the mainnet
// template's prometheus.enable from false to true, otherwise the bound
// docker port serves nothing. Also asserts the Nile template (no
// prometheus block, #167) is a safe no-op — render must not corrupt it.
func TestRenderHOCON_MetricsFeatureEnables(t *testing.T) {
	enabled := true

	t.Run("mainnet flips enable=true", func(t *testing.T) {
		out, err := RenderHOCON("", &intent.Intent{
			Name: "m", Network: "mainnet", Target: intent.Target{Type: "local"},
		}, &intent.NodeSpec{
			Type:     "fullnode",
			Features: intent.Features{Metrics: &enabled},
			Ports:    intent.PortMapping{Metrics: 59527},
		})
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		// Scope the assertion to the prometheus block specifically — the
		// mainnet config has several other `enable = false` lines
		// (influxdb, other features) that this fix must NOT touch.
		prom := prometheusBlock(t, out)
		if !strings.Contains(prom, "enable = true") {
			t.Errorf("prometheus.enable not flipped to true; block was:\n%s", prom)
		}
		if strings.Contains(prom, "enable = false") {
			t.Errorf("prometheus.enable left as false despite features.metrics=true; block:\n%s", prom)
		}
	})

	t.Run("nile no prometheus block is a safe no-op", func(t *testing.T) {
		out, err := RenderHOCON("", &intent.Intent{
			Name: "m", Network: "nile", Target: intent.Target{Type: "local"},
		}, &intent.NodeSpec{
			Type:     "fullnode",
			Features: intent.Features{Metrics: &enabled},
		})
		if err != nil {
			t.Fatalf("render nile: %v", err)
		}
		// Nile template has no prometheus block; render must not have
		// synthesised a stray `enable = true` line.
		if strings.Contains(out, "prometheus") {
			t.Error("nile render unexpectedly contains a prometheus block")
		}
	})
}

func TestRenderHOCON_UnknownNetwork(t *testing.T) {
	i := &intent.Intent{Network: "martian"}
	node := &intent.NodeSpec{Type: "fullnode"}
	if _, err := RenderHOCON("", i, node); err == nil {
		t.Error("expected error for unknown network")
	}
}

func TestReplaceHOCONValue(t *testing.T) {
	in := `  fullNodePort = 8090
  other = 1`
	out := replaceHOCONValue(in, "fullNodePort", "9999")
	if !strings.Contains(out, "fullNodePort = 9999") {
		t.Errorf("replacement failed: %s", out)
	}
	// Indentation is preserved
	if !strings.Contains(out, "  fullNodePort = 9999") {
		t.Errorf("indentation lost: %q", out)
	}
}

func TestEnsureMetricsEnabled_ExistingEnableFalse(t *testing.T) {
	in := `node.metrics {
  prometheus {
    enable = false
    port = 9527
  }
}`
	out := ensureMetricsEnabled(in)
	if !strings.Contains(out, "enable = true") {
		t.Errorf("expected enable = true, got:\n%s", out)
	}
}

func TestEnsureMetricsEnabled_NoEnableField(t *testing.T) {
	in := `node.metrics {
  prometheus {
    port = 9527
  }
}`
	out := ensureMetricsEnabled(in)
	if !strings.Contains(out, "enable = true") {
		t.Errorf("expected enable = true inserted, got:\n%s", out)
	}
}

func TestEnsureMetricsEnabled_NoPrometheusBlock(t *testing.T) {
	in := `node.metrics {
  something = else
}`
	out := ensureMetricsEnabled(in)
	if !strings.Contains(out, "prometheus {") || !strings.Contains(out, "enable = true") {
		t.Errorf("expected prometheus block inserted, got:\n%s", out)
	}
}

func TestEnsureMetricsEnabled_MissingMetricsBlock(t *testing.T) {
	in := `some.config = value`
	out := ensureMetricsEnabled(in)
	if !strings.Contains(out, "node.metrics {") || !strings.Contains(out, "prometheus {") {
		t.Errorf("expected full metrics block appended, got:\n%s", out)
	}
}

func TestRenderHOCON_MonitoringEnabled(t *testing.T) {
	i := &intent.Intent{
		Name:       "mon-test",
		Network:    "mainnet",
		Target:     intent.Target{Type: "local"},
		Monitoring: &intent.Monitoring{Enabled: intent.BoolPtr(true)},
	}
	node := &intent.NodeSpec{Type: "fullnode"}
	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(out, "node.metrics") {
		t.Error("missing node.metrics block")
	}
	if !strings.Contains(out, "enable = true") {
		t.Error("metrics not enabled")
	}
}

func TestRenderHOCON_MonitoringDisabled(t *testing.T) {
	i := &intent.Intent{
		Name:    "no-mon",
		Network: "mainnet",
		Target:  intent.Target{Type: "local"},
	}
	node := &intent.NodeSpec{Type: "fullnode"}
	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	// When monitoring is not enabled, metrics block should NOT be injected.
	// Template may already contain node.metrics block with enable=false.
	// When monitoring is disabled, we should NOT see enable=true.
	lines := strings.Split(out, "\n")
	inMetrics := false
	inPrometheus := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "node.metrics") && strings.Contains(trimmed, "{") {
			inMetrics = true
			continue
		}
		if inMetrics && strings.HasPrefix(trimmed, "prometheus") && strings.Contains(trimmed, "{") {
			inPrometheus = true
			continue
		}
		if inPrometheus && strings.Contains(trimmed, "enable = true") {
			t.Error("metrics enable=true should not be present when monitoring is disabled")
		}
		if inPrometheus && trimmed == "}" {
			break
		}
	}
}
