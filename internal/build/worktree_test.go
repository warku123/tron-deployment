package build

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// TestComputePatchHash_Deterministic is the FR-026 reproducibility
// guard: same patches in same order → same hash, regardless of
// machine / file path / surrounding state. Without this, two
// operators with the same intent would NOT hit the same cache key,
// defeating the whole point of declarative patches.
func TestComputePatchHash_Deterministic(t *testing.T) {
	d := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a1 := mk("a1.patch", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-old\n+new\n")
	a2 := mk("a2.patch", "diff --git a/y b/y\n--- a/y\n+++ b/y\n@@\n-old\n+new\n")

	h1, err := computePatchHash([]string{a1, a2})
	if err != nil {
		t.Fatalf("computePatchHash: %v", err)
	}
	h2, err := computePatchHash([]string{a1, a2})
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("hash drift on repeated call: %q vs %q", h1, h2)
	}

	t.Run("order matters", func(t *testing.T) {
		swapped, _ := computePatchHash([]string{a2, a1})
		if swapped == h1 {
			t.Error("hash must change when patches reorder (different application order can produce different end-state for adjacent hunks)")
		}
	})

	t.Run("identical contents different filenames hash same", func(t *testing.T) {
		copyA1 := mk("copy-of-a1.patch", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-old\n+new\n")
		copyA2 := mk("copy-of-a2.patch", "diff --git a/y b/y\n--- a/y\n+++ b/y\n@@\n-old\n+new\n")
		h3, err := computePatchHash([]string{copyA1, copyA2})
		if err != nil {
			t.Fatal(err)
		}
		if h3 != h1 {
			t.Errorf("identical patch contents should hash identically regardless of filename: %q vs %q",
				h3, h1)
		}
	})

	t.Run("empty list returns empty string", func(t *testing.T) {
		h, err := computePatchHash(nil)
		if err != nil || h != "" {
			t.Errorf("empty patches → empty hash, no err; got (%q, %v)", h, err)
		}
	})
}

// TestValidatePatchFile pins FR-028's heuristic header check —
// catches the common error of pointing patches: at a non-patch file.
func TestValidatePatchFile(t *testing.T) {
	d := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name      string
		body      string
		wantValid bool
	}{
		{"git-format diff", "diff --git a/x b/x\n--- a/x\n+++ b/x\n", true},
		{"posix patch format", "--- a/x.txt\t2024-01-01\n+++ b/x.txt\t2024-01-02\n@@\n", true},
		{"svn-style Index header", "Index: foo/bar.java\n===================================================================\n--- a/foo/bar.java\n+++ b/foo/bar.java\n", true},
		{"yaml masquerading as patch", "name: foo\nnetwork: mainnet\n", false},
		{"random text", "hello world\n", false},
		{"binary garbage", "\x00\x01\x02\x03", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := mk(c.name+".file", c.body)
			err := validatePatchFile(p)
			if (err == nil) != c.wantValid {
				t.Errorf("validatePatchFile(%q): wantValid=%v; err=%v", c.name, c.wantValid, err)
			}
		})
	}

	t.Run("missing path errors", func(t *testing.T) {
		if err := validatePatchFile("/definitely/not/here.patch"); err == nil {
			t.Error("expected error for missing file")
		}
	})
}

// TestSetupWorktree_AppliesPatchesAndCleansUp is the full lifecycle
// test: create a small git repo with a file, write a patch that
// modifies it, run setupWorktree, verify the worktree exists with
// the patch applied, then run cleanup and verify the worktree is
// gone.
func TestSetupWorktree_AppliesPatchesAndCleansUp(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatal(err)
	}

	srcDir := initSmallRepo(t)
	patchPath := writePatch(t, t.TempDir(), "hello.patch", `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-original
+patched
`)

	rev := gitHEAD(t, srcDir)
	wt, cleanup, err := setupWorktree(context.Background(), srcDir, rev, "test-key", []string{patchPath})
	if err != nil {
		t.Fatalf("setupWorktree: %v", err)
	}
	defer cleanup()

	body, err := os.ReadFile(filepath.Join(wt, "file.txt"))
	if err != nil {
		t.Fatalf("read patched file: %v", err)
	}
	if strings.TrimSpace(string(body)) != "patched" {
		t.Errorf("file.txt = %q; want 'patched'", string(body))
	}

	cleanup()
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Errorf("cleanup did not remove worktree at %s: %v", wt, err)
	}
}

// TestSetupWorktree_BadPatchSurfacesStructuredError verifies that
// a patch that doesn't apply produces a PATCH_FAILED envelope with
// the patch path and a helpful suggestion, not a generic git error.
func TestSetupWorktree_BadPatchSurfacesStructuredError(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatal(err)
	}

	srcDir := initSmallRepo(t)
	// Patch references a line that doesn't exist — won't apply.
	badPatch := writePatch(t, t.TempDir(), "bad.patch", `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-this-line-does-not-exist
+something-else
`)
	rev := gitHEAD(t, srcDir)

	_, cleanup, err := setupWorktree(context.Background(), srcDir, rev, "bad-key", []string{badPatch})
	if cleanup != nil {
		// shouldn't be set on error, but defensive
		cleanup()
	}
	if err == nil {
		t.Fatal("expected error when patch doesn't apply")
	}
	var se *output.StructuredError
	if !errors.As(err, &se) {
		t.Fatalf("expected *output.StructuredError; got %T: %v", err, err)
	}
	if se.Code != "PATCH_FAILED" {
		t.Errorf("error_code = %q; want PATCH_FAILED", se.Code)
	}
	if !strings.Contains(se.Message, "bad.patch") {
		t.Errorf("error message should mention which patch failed; got %q", se.Message)
	}

	// And the worktree dir must NOT linger after the failed setup.
	wtPath := filepath.Join(CacheDir(), "worktrees", "bad-key")
	if _, statErr := os.Stat(wtPath); !os.IsNotExist(statErr) {
		t.Errorf("failed setupWorktree leaked dir %s; stat err = %v", wtPath, statErr)
	}
}

// TestResolveBuild_PatchesOverridesPatchHash is the integration-
// level guard: req.Patches non-empty makes PatchHash deterministic
// from the patch contents, NOT the source tree's git diff. This is
// the cross-machine cache reuse property FR-026 guarantees.
func TestResolveBuild_PatchesOverridesPatchHash(t *testing.T) {
	srcDir := initSmallRepo(t)
	d := t.TempDir()
	patchPath := writePatch(t, d, "p.patch", `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-original
+patched
`)
	// Intentionally make the source tree dirty in a way that would
	// shift the WORKING-TREE-derived PatchHash. If req.Patches
	// overrides correctly, the cache key should be invariant to
	// this dirty noise.
	if err := os.WriteFile(filepath.Join(srcDir, "unrelated-wip.txt"),
		[]byte("local WIP scribbles"), 0o600); err != nil {
		t.Fatal(err)
	}

	mk := func() (string, string) {
		req := Request{
			SourcePath:           srcDir,
			RevisionSpec:         "HEAD",
			ArtifactKind:         "jar",
			JDKVersion:           "8",
			Builder:              "docker",
			GradleTask:           "shadowJar",
			Platform:             "linux/amd64",
			Patches:              []string{patchPath},
			BuilderImageOverride: "test-image@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		}
		r, err := resolveBuild(context.Background(), req)
		if err != nil {
			t.Fatalf("resolveBuild: %v", err)
		}
		return r.src.PatchHash, r.cacheKeyStr
	}

	hash1, key1 := mk()

	// Mutate the unrelated WIP — would shift the working-tree
	// PatchHash if it leaked in.
	if err := os.WriteFile(filepath.Join(srcDir, "unrelated-wip.txt"),
		[]byte("completely different scribbles"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash2, key2 := mk()

	if hash1 != hash2 || key1 != key2 {
		t.Errorf("PatchHash/cache key drifted when unrelated WIP changed:\n  hash: %q vs %q\n  key:  %q vs %q\n"+
			"(req.Patches MUST override the working-tree-derived hash so cache is cross-machine reusable)",
			hash1, hash2, key1, key2)
	}
}

// --- helpers ---

func initSmallRepo(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", d}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(d, "file.txt"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("add", "file.txt")
	run("commit", "-q", "-m", "initial")
	return d
}

func gitHEAD(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse HEAD: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func writePatch(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
