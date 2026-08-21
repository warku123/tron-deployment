package intent

import (
	"os"
	"strings"
	"testing"
)

func TestParseRejectsUnknownTopLevelAndNestedFields(t *testing.T) {
	for _, body := range []string{
		"name: n\nnetwork: mainnet\nunknown: true\n",
		"name: n\nnetwork: mainnet\nnodes:\n  - type: fullnode\n    resources:\n      memory: 8GB\n      unknown: true\n",
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Fatalf("accepted unknown field: %s", body)
		}
	}
}

func TestLoadWithOverlayPreservesUntouchedTargetFields(t *testing.T) {
	dir := t.TempDir()
	base := dir + "/base.yaml"
	overlay := dir + "/overlay.yaml"
	baseData := []byte("name: n\nnetwork: mainnet\ntarget:\n  type: ssh\n  host: example\n  port: 22\n  user: tron\n  identity_file: /key\nnodes:\n  - type: fullnode\n    resources:\n      memory: 8GB\n")
	if err := os.WriteFile(base, baseData, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(overlay, []byte("target:\n  user: other\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWithOverlay(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if got.Target.Host != "example" || got.Target.IdentityFile != "/key" || got.Target.User != "other" {
		t.Fatalf("target=%+v", got.Target)
	}
	if strings.TrimSpace(got.Target.Type) != "ssh" {
		t.Fatalf("target type=%q", got.Target.Type)
	}
}
