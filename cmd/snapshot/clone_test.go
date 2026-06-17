package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// runCloneCapture runs runClone with a cobra command carrying the given
// output format, capturing stdout, and returns the raw stdout + run error.
func runCloneCapture(t *testing.T, src, dst, format string) (string, error) {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().String("output", format, "")
	c.SetContext(context.Background())

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := runClone(c, []string{src, dst})
	_ = w.Close()
	os.Stdout = old
	raw, _ := io.ReadAll(r)
	return string(raw), err
}

// invokeClone is the JSON-output convenience wrapper: it parses stdout into
// a map. Error cases return a nil map + the error.
func invokeClone(t *testing.T, src, dst string) (map[string]any, error) {
	t.Helper()
	raw, err := runCloneCapture(t, src, dst, "json")
	var m map[string]any
	if len(raw) > 0 {
		if jsonErr := json.Unmarshal([]byte(raw), &m); jsonErr != nil && err == nil {
			t.Fatalf("clone JSON parse: %v\n%s", jsonErr, raw)
		}
	}
	return m, err
}

func wantValidationError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError, got %T: %v", err, err)
	}
	if se.ExitCode != output.ExitValidationError {
		t.Errorf("exit code = %d, want %d (validation); err=%v", se.ExitCode, output.ExitValidationError, err)
	}
}

func TestRunClone_SrcMissing(t *testing.T) {
	tmp := t.TempDir()
	_, err := invokeClone(t, filepath.Join(tmp, "nope"), filepath.Join(tmp, "dst"))
	wantValidationError(t, err)
}

func TestRunClone_DstExists(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	mustMkdir(t, src)
	mustMkdir(t, dst) // dst already present → refuse
	_, err := invokeClone(t, src, dst)
	wantValidationError(t, err)
}

func TestRunClone_DstInsideSrc(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	mustMkdir(t, src)
	dst := filepath.Join(src, "sub") // dst is inside src → reject
	_, err := invokeClone(t, src, dst)
	wantValidationError(t, err)
	// dst must not have been created
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Errorf("dst should not exist after a rejected clone")
	}
}

// TestRunClone_HappyRoundTrip clones a small tree into an ADJACENT directory
// (same filesystem, so CoW can fire) and asserts: the wire JSON shape, that
// a method was reported (clonefile|ficlone|copy — any is valid; CI
// filesystems return copy even for adjacent dirs), and that data is
// byte-identical in the clone.
func TestRunClone_HappyRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst") // adjacent to src → same FS
	mustMkdir(t, filepath.Join(src, "database"))
	payload := []byte("MANIFEST-000001\nleveldb-data")
	if err := os.WriteFile(filepath.Join(src, "database", "CURRENT"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	m, err := invokeClone(t, src, dst)
	if err != nil {
		t.Fatalf("runClone: %v", err)
	}
	if m["source"] != src || m["dest"] != dst {
		t.Errorf("source/dest = %v/%v; want %s/%s", m["source"], m["dest"], src, dst)
	}
	method, _ := m["method"].(string)
	switch method {
	case "clonefile", "ficlone", "copy":
	default:
		t.Errorf("method = %q; want one of clonefile|ficlone|copy", method)
	}
	if _, ok := m["duration_ms"]; !ok {
		t.Errorf("duration_ms missing from clone JSON")
	}
	got, readErr := os.ReadFile(filepath.Join(dst, "database", "CURRENT"))
	if readErr != nil {
		t.Fatalf("read cloned file: %v", readErr)
	}
	if string(got) != string(payload) {
		t.Errorf("cloned content = %q; want %q", got, payload)
	}
}

// TestRunClone_TextOutput covers the non-JSON (human) output branch.
func TestRunClone_TextOutput(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	dst := filepath.Join(tmp, "dst")
	mustMkdir(t, src)
	out, err := runCloneCapture(t, src, dst, "text")
	if err != nil {
		t.Fatalf("runClone: %v", err)
	}
	if !strings.Contains(out, "Cloned") || !strings.Contains(out, "method:") {
		t.Errorf("text output = %q; want a 'Cloned ... (method: ...)' line", out)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
