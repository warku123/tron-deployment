package mcp

import (
	"strings"
	"testing"
)

func TestRedactLiveConfigForMCP(t *testing.T) {
	key := strings.Repeat("a", 64)
	config := "node.p2p.version = 1\n" + `localwitness = ["` + key + `"]` + "\nseed.node.ip.list = []\n"
	got := redactLiveConfigForMCP(config)
	if strings.Contains(got, key) {
		t.Fatal("live config redaction leaked witness private key")
	}
	if !strings.Contains(got, `localwitness = ["<REDACTED>"]`) {
		t.Fatalf("redacted config lost readable witness structure: %q", got)
	}
	if !strings.Contains(got, "node.p2p.version = 1") || !strings.Contains(got, "seed.node.ip.list = []") {
		t.Fatalf("redaction changed unrelated config: %q", got)
	}
}

func TestRedactLiveConfigForMCP_ColonWitnessSyntax(t *testing.T) {
	key := strings.Repeat("b", 64)
	got := redactLiveConfigForMCP(`localwitness : ["` + key + `"]`)
	if strings.Contains(got, key) || got != `localwitness = ["<REDACTED>"]` {
		t.Fatalf("colon-separated live witness config was not redacted: %q", got)
	}
}
