package render

import (
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/intent"
)

func TestLoadTemplate_EmbeddedFallback(t *testing.T) {
	cases := []string{"mainnet", "nile", "private", "system-test"}
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

func TestRenderHOCON_SystemTest(t *testing.T) {
	// system-test template carries stest-specific markers: pre-funded
	// genesis accounts, three local witness keys, fast 20s maintenance
	// interval. If those disappear from the rendered output something
	// has stripped or replaced the embedded template.
	i := &intent.Intent{
		Name:    "stest-singlenode",
		Network: "system-test",
		Target:  intent.Target{Type: "local", Runtime: "jar"},
	}
	node := &intent.NodeSpec{Type: "witness"}

	out, err := RenderHOCON("", i, node)
	if err != nil {
		t.Fatalf("render system-test: %v", err)
	}

	// Markers from the upstream stest config-system-test.conf
	// (release_workflow branch). If any disappear, the embed wiring or
	// upstream sync has stripped something load-bearing.
	wantSubstrs := []string{
		"localwitness",                     // single-SR self-signing keys
		"maintenanceTimeInterval = 300000", // 5min cadence used by stest CI
		"port = 50051",                     // gRPC fullnode
		"fullNodePort = 8090",              // HTTP fullnode
		"httpFullNodePort = 8545",          // jsonrpc (stest CI value; differs from
		// testng.conf's 50545 — known config drift, see openspec change
		// system-test-integration design.md D1)
	}
	for _, s := range wantSubstrs {
		if !strings.Contains(out, s) {
			t.Errorf("system-test render missing %q\n--- output ---\n%s", s, out)
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
