package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/paths"
)

// TestApplyTool_RequirePrivate_RefusesNonPrivate is the MCP-side C1 guard
// test: apply with require_private=true against a mainnet intent must refuse
// at the EARLY guard (before target resolution / HUMAN_REQUIRED) with
// PRIVATE_NETWORK_REQUIRED — not TARGET_UNREACHABLE, HUMAN_REQUIRED, or a
// re-wrapped DEPLOY_ERROR. No docker needed: it returns at guard time.
func TestApplyTool_RequirePrivate_RefusesNonPrivate(t *testing.T) {
	paths.SetBaseDir(t.TempDir())
	t.Cleanup(func() { paths.SetBaseDir("") })

	intentPath := filepath.Join(t.TempDir(), "intent.yaml")
	body := "name: guard-mcp\nnetwork: mainnet\n" +
		"target:\n  type: local\n  runtime: docker\n" +
		"nodes:\n  - type: fullnode\n    version: latest\n"
	if err := os.WriteFile(intentPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	res, _, err := applyTool(context.Background(), nil, applyArgs{Path: intentPath, RequirePrivate: true})
	if err != nil {
		t.Fatalf("applyTool returned a transport error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected an error result, got %+v", res)
	}
	text := extractText(t, res)
	if !strings.Contains(text, "PRIVATE_NETWORK_REQUIRED") {
		t.Errorf("error result should carry PRIVATE_NETWORK_REQUIRED; got:\n%s", text)
	}
}
