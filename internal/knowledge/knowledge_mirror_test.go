package knowledge

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestKnowledgeMirror guards against drift between the operator-
// readable knowledge/ directory at the repo root and the embedded
// copies under internal/knowledge/files/. Both must stay in sync
// because:
//
//   - knowledge/ is what GitHub renders + what operators read on the
//     repo page or via `cat`.
//   - internal/knowledge/files/ is what `trond knowledge <topic>`
//     serves at runtime (bundled into the binary via go:embed).
//
// A drift means a user reading the topic via the CLI sees stale
// content vs the version on GitHub. Run `make sync-knowledge` to
// re-mirror after editing any operator doc under knowledge/.
func TestKnowledgeMirror(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	sourceDir := filepath.Join(repoRoot, "knowledge")
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatalf("read %s: %v", sourceDir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			srcPath := filepath.Join(sourceDir, e.Name())
			src, err := os.ReadFile(srcPath)
			if err != nil {
				t.Fatalf("read source %s: %v", srcPath, err)
			}
			embedded, err := knowledgeFS.ReadFile("files/" + e.Name())
			if err != nil {
				t.Fatalf("embedded %q missing from internal/knowledge/files/: %v\n"+
					"Run 'make sync-knowledge' to mirror.", e.Name(), err)
			}
			if !bytes.Equal(src, embedded) {
				t.Fatalf("drift between %s and internal/knowledge/files/%s.\n"+
					"Run 'make sync-knowledge' to re-mirror.",
					srcPath, e.Name())
			}
		})
	}
}
