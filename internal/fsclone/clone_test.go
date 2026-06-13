package fsclone

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeTree lays down a small fixture tree under root and returns the
// name→content map it created.
func writeTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := map[string][]byte{
		"a.txt":          []byte("alpha"),
		"sub/b.txt":      []byte("bravo"),
		"sub/deep/c.txt": []byte("charlie"),
	}
	for name, content := range files {
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func assertTree(t *testing.T, dst string, want map[string][]byte) {
	t.Helper()
	for name, content := range want {
		got, err := os.ReadFile(filepath.Join(dst, name))
		if err != nil {
			t.Errorf("read %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, content) {
			t.Errorf("%s: got %q, want %q", name, got, content)
		}
	}
}

// TestCloneDir_RoundTrip exercises whichever real path the host takes
// (CoW on APFS/btrfs/xfs, byte copy otherwise): content must be
// identical AND the clone must be independent — writing it must not
// touch the source. Independence is the property the dbfork gate relies
// on (two separately-mutable scratch copies).
func TestCloneDir_RoundTrip(t *testing.T) {
	src := t.TempDir()
	want := writeTree(t, src)

	dst := filepath.Join(t.TempDir(), "clone")
	method, err := CloneDir(src, dst)
	if err != nil {
		t.Fatalf("CloneDir: %v", err)
	}
	t.Logf("CloneDir method: %s", method)
	if method != cloneMethodName && method != methodCopy {
		t.Errorf("unexpected method %q", method)
	}
	assertTree(t, dst, want)

	// Independence: mutate the clone, source must be unchanged.
	if err := os.WriteFile(filepath.Join(dst, "a.txt"), []byte("CHANGED"), 0o644); err != nil {
		t.Fatal(err)
	}
	srcA, err := os.ReadFile(filepath.Join(src, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(srcA, []byte("alpha")) {
		t.Errorf("clone not independent: source a.txt became %q", srcA)
	}
}

// TestCloneDir_ForcedFallback swaps the platform clone seam to report
// unsupported, so the fallback orchestration (discard partial → byte
// copy → atomic rename) runs deterministically on ANY host — including
// CoW filesystems where the real clone would always succeed and this
// branch would otherwise never execute.
func TestCloneDir_ForcedFallback(t *testing.T) {
	orig := platformClone
	t.Cleanup(func() { platformClone = orig })
	platformClone = func(src, dst string) error { return errUnsupported }

	src := t.TempDir()
	want := writeTree(t, src)
	dst := filepath.Join(t.TempDir(), "clone")

	method, err := CloneDir(src, dst)
	if err != nil {
		t.Fatalf("CloneDir (forced fallback): %v", err)
	}
	if method != methodCopy {
		t.Errorf("forced fallback should report %q, got %q", methodCopy, method)
	}
	assertTree(t, dst, want)
}

// TestCloneDir_RealErrorPropagates proves a non-CoW error from the
// platform clone is NOT masked as a slow copy — it propagates, so a
// genuine I/O failure can't masquerade as success.
func TestCloneDir_RealErrorPropagates(t *testing.T) {
	orig := platformClone
	t.Cleanup(func() { platformClone = orig })
	boom := os.ErrPermission
	platformClone = func(src, dst string) error { return boom }

	src := t.TempDir()
	writeTree(t, src)
	dst := filepath.Join(t.TempDir(), "clone")

	if _, err := CloneDir(src, dst); err == nil {
		t.Fatal("expected the real error to propagate, got nil")
	}
	// And dst must not have been created from a silent fallback.
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dst should not exist after a propagated error, stat err=%v", err)
	}
}

// TestCloneDir_RefusesExistingDst pins the no-clobber contract.
func TestCloneDir_RefusesExistingDst(t *testing.T) {
	src := t.TempDir()
	writeTree(t, src)
	dst := t.TempDir() // already exists
	if _, err := CloneDir(src, dst); err == nil {
		t.Fatal("expected CloneDir to refuse an existing destination")
	}
}

// TestCloneDir_MissingSrc pins that an unreadable/absent source is a
// clean error, not a panic or a silent empty clone.
func TestCloneDir_MissingSrc(t *testing.T) {
	src := filepath.Join(t.TempDir(), "does-not-exist")
	dst := filepath.Join(t.TempDir(), "clone")
	if _, err := CloneDir(src, dst); err == nil {
		t.Fatal("expected error for missing source")
	}
}

// TestRecursiveCopy_RoundTrip pins the byte-copy fallback directly,
// independent of any CoW path.
func TestRecursiveCopy_RoundTrip(t *testing.T) {
	src := t.TempDir()
	want := writeTree(t, src)
	dst := filepath.Join(t.TempDir(), "copy")
	if err := recursiveCopy(src, dst); err != nil {
		t.Fatalf("recursiveCopy: %v", err)
	}
	assertTree(t, dst, want)
}
