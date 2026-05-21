package build

import (
	"bytes"
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
//
// Drives through buildPatchRecords + computePatchHashFromRecords —
// the same two-step flow resolveBuild uses in production.
func TestComputePatchHash_Deterministic(t *testing.T) {
	d := t.TempDir()
	mk := func(name, body string) string {
		p := filepath.Join(d, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hashOf := func(paths []string) string {
		t.Helper()
		recs, err := buildPatchRecords(paths)
		if err != nil {
			t.Fatalf("buildPatchRecords: %v", err)
		}
		return computePatchHashFromRecords(recs)
	}
	a1 := mk("a1.patch", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-old\n+new\n")
	a2 := mk("a2.patch", "diff --git a/y b/y\n--- a/y\n+++ b/y\n@@\n-old\n+new\n")

	h1 := hashOf([]string{a1, a2})
	h2 := hashOf([]string{a1, a2})
	if h1 != h2 {
		t.Errorf("hash drift on repeated call: %q vs %q", h1, h2)
	}

	t.Run("order matters", func(t *testing.T) {
		swapped := hashOf([]string{a2, a1})
		if swapped == h1 {
			t.Error("hash must change when patches reorder (different application order can produce different end-state for adjacent hunks)")
		}
	})

	t.Run("identical contents different filenames hash same", func(t *testing.T) {
		copyA1 := mk("copy-of-a1.patch", "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@\n-old\n+new\n")
		copyA2 := mk("copy-of-a2.patch", "diff --git a/y b/y\n--- a/y\n+++ b/y\n@@\n-old\n+new\n")
		h3 := hashOf([]string{copyA1, copyA2})
		if h3 != h1 {
			t.Errorf("identical patch contents should hash identically regardless of filename: %q vs %q",
				h3, h1)
		}
	})

	t.Run("empty list returns empty string", func(t *testing.T) {
		recs, err := buildPatchRecords(nil)
		if err != nil || len(recs) != 0 {
			t.Errorf("nil patches → empty records, no err; got (%v, %v)", recs, err)
		}
		if h := computePatchHashFromRecords(recs); h != "" {
			t.Errorf("empty records → empty hash; got %q", h)
		}
	})
}

// TestBuildPatchRecords pins the Manifest-side fingerprint shape:
// each record stores the basename + content sha256 so `trond build
// inspect` shows portable identifiers even when the cache is pulled
// from a shared TROND_STATE_DIR and the absolute paths no longer
// exist on the inspecting machine.
func TestBuildPatchRecords(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "sub", "01-skip-foo.patch")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "diff --git a/file b/file\n--- a/file\n+++ b/file\n@@\n-a\n+b\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	recs, err := buildPatchRecords([]string{p})
	if err != nil {
		t.Fatalf("buildPatchRecords: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("len(recs) = %d; want 1", len(recs))
	}
	if recs[0].Name != "01-skip-foo.patch" {
		t.Errorf("Name = %q; want basename only (no nested path components)", recs[0].Name)
	}
	if len(recs[0].SHA256) != 64 {
		t.Errorf("SHA256 length = %d; want 64 (full hex sha256)", len(recs[0].SHA256))
	}
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

	// Build a `git format-patch` style file whose `diff --git` line
	// is past the prior 4 KB sniff window — review-pass-6 regression
	// guard for the format-patch rejection bug.
	longBody := strings.Repeat("Long commit message body line.\n", 200) // ~6 KB
	formatPatch := "From abc1234567890abcdef1234567890abcdef1234 Mon Sep 17 00:00:00 2001\n" +
		"From: Author <author@example.com>\n" +
		"Date: Mon, 17 Sep 2001 00:00:00 -0000\n" +
		"Subject: [PATCH] Example fix\n\n" +
		longBody + "\n" +
		"---\n file | 2 +-\n 1 file changed, 1 insertion(+), 1 deletion(-)\n\n" +
		"diff --git a/file b/file\n--- a/file\n+++ b/file\n@@\n-a\n+b\n"

	cases := []struct {
		name      string
		body      string
		wantValid bool
	}{
		{"git-format diff", "diff --git a/x b/x\n--- a/x\n+++ b/x\n", true},
		{"posix patch format", "--- a/x.txt\t2024-01-01\n+++ b/x.txt\t2024-01-02\n@@\n", true},
		{"svn-style Index header", "Index: foo/bar.java\n===================================================================\n--- a/foo/bar.java\n+++ b/foo/bar.java\n", true},
		{"git format-patch with long body", formatPatch, true},
		{"yaml masquerading as patch", "name: foo\nnetwork: mainnet\n", false},
		{"random text", "hello world\n", false},
		{"binary garbage", "\x00\x01\x02\x03", false},
		{"From <not-a-sha>", "From this-isnt-a-sha Mon Sep ...\n", false},
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

// TestResolveBuild_PatchesProducesStableCacheKey is the review-pass-6
// regression guard for the headline PR benefit: same intent + same
// patch contents → same cache_key on every machine. Calls
// resolveBuild twice with the SAME patches (different paths to
// otherwise-identical files) and asserts the cache key matches.
//
// Without this, the cross-machine cache reuse story is unverified;
// the existing TestResolveBuild_PatchesOverridesPatchHash only
// proves dirty-tree noise doesn't shift the key, not that two
// separate intents-with-patches converge.
func TestResolveBuild_PatchesProducesStableCacheKey(t *testing.T) {
	srcDir := initSmallRepo(t)
	body := "diff --git a/file.txt b/file.txt\n--- a/file.txt\n+++ b/file.txt\n@@ -1 +1 @@\n-original\n+patched\n"

	// Two separate copies of the same patch contents at different
	// paths — models two operators with the same logical patch
	// living in different filesystem locations.
	d1, d2 := t.TempDir(), t.TempDir()
	pA := filepath.Join(d1, "alice-checkout", "patches", "p.patch")
	pB := filepath.Join(d2, "bobs-different-layout", "p.patch")
	for _, p := range []string{pA, pB} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	mk := func(patches []string) string {
		r, err := resolveBuild(context.Background(), Request{
			SourcePath:           srcDir,
			RevisionSpec:         "HEAD",
			ArtifactKind:         "jar",
			JDKVersion:           "8",
			Builder:              "docker",
			GradleTask:           "shadowJar",
			Platform:             "linux/amd64",
			Patches:              patches,
			BuilderImageOverride: "test-image@sha256:0000000000000000000000000000000000000000000000000000000000000000",
		})
		if err != nil {
			t.Fatalf("resolveBuild: %v", err)
		}
		return r.cacheKeyStr
	}

	keyA := mk([]string{pA})
	keyB := mk([]string{pB})
	if keyA != keyB {
		t.Errorf("identical patch contents at different paths must produce the SAME cache key\n  got %q vs %q\n  (cache reuse across operator machines is the headline benefit of declarative patches)",
			keyA, keyB)
	}

	// Two-sided: patches MUST actually participate in the key, not
	// be silently dropped. Without this assertion, the test above
	// would pass trivially if a future refactor accidentally skipped
	// the patches dimension of resolveBuild's cache key.
	keyNoPatches := mk(nil)
	if keyA == keyNoPatches {
		t.Errorf("patches did not affect the cache key — silent drop?\n  with patches:    %q\n  without patches: %q",
			keyA, keyNoPatches)
	}
}

// TestSetupWorktree_AgainstNonHeadRevision is the review-pass-8 M5
// guard: patches MUST apply against the revision the user pinned in
// build.revision, NOT against whatever HEAD happens to be. This is
// the common case "I want to test patch X against the v4.7.7 tag"
// — if the user moves HEAD forward and the surrounding code drifted,
// the patch should still cleanly apply at the pinned older revision.
//
// Set-up: repo with two commits. Patches generated against the FIRST
// commit's content. Worktree pinned to the first commit. Apply must
// succeed even though HEAD has drifted to a different content shape.
func TestSetupWorktree_AgainstNonHeadRevision(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatal(err)
	}
	srcDir := initSmallRepo(t) // initial: file.txt = "original\n"
	firstRev := gitHEAD(t, srcDir)

	// Drift HEAD: replace file.txt entirely so the patch (which
	// matches the original content) would FAIL against current HEAD
	// but SUCCEEDS against firstRev.
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"),
		[]byte("DRIFTED CONTENT — totally different\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, srcDir, "add", "file.txt")
	gitInRepo(t, srcDir, "commit", "-q", "-m", "drift HEAD")

	// Patch matches the FIRST-commit content.
	patch := writePatch(t, t.TempDir(), "p.patch", `diff --git a/file.txt b/file.txt
--- a/file.txt
+++ b/file.txt
@@ -1 +1 @@
-original
+patched-at-pinned-revision
`)

	wt, cleanup, err := setupWorktree(context.Background(), srcDir, firstRev, "non-head-key", []string{patch})
	if err != nil {
		t.Fatalf("setupWorktree against pinned revision: %v "+
			"(the worktree should checkout firstRev and apply the patch there, NOT against current HEAD)", err)
	}
	defer cleanup()

	body, err := os.ReadFile(filepath.Join(wt, "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(body)) != "patched-at-pinned-revision" {
		t.Errorf("worktree contents = %q; want patch applied against the pinned revision", string(body))
	}
}

// TestSetupWorktree_BinaryPatch confirms a unified diff that includes
// a binary-file hunk (git's `GIT binary patch` block) is accepted by
// validatePatchFile + applies via git apply. Catches a regression
// where the sniff heuristic rejects the binary-patch header style.
func TestSetupWorktree_BinaryPatch(t *testing.T) {
	withTempBaseDir(t)
	if err := EnsureCacheDirs(); err != nil {
		t.Fatal(err)
	}
	srcDir := initSmallRepo(t)

	// Add a binary file at HEAD so the patch has something to
	// modify. Then craft a git-format binary patch that replaces
	// its contents. Producing a real `GIT binary patch` block by
	// hand is fiddly, so we use `git diff --binary` against a
	// temporary mutation.
	binPath := filepath.Join(srcDir, "data.bin")
	if err := os.WriteFile(binPath, []byte{0x00, 0x01, 0x02, 0x03}, 0o600); err != nil {
		t.Fatal(err)
	}
	gitInRepo(t, srcDir, "add", "data.bin")
	gitInRepo(t, srcDir, "commit", "-q", "-m", "add binary")
	rev := gitHEAD(t, srcDir)

	// Mutate the binary then capture a binary diff.
	if err := os.WriteFile(binPath, []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}, 0o600); err != nil {
		t.Fatal(err)
	}
	diffCmd := exec.Command("git", "-C", srcDir, "diff", "--binary", "HEAD", "--", "data.bin")
	diffOut, err := diffCmd.Output()
	if err != nil {
		t.Fatalf("git diff --binary: %v", err)
	}
	gitInRepo(t, srcDir, "checkout", "--", "data.bin") // restore so worktree starts clean
	patchPath := writePatch(t, t.TempDir(), "binary.patch", string(diffOut))

	// Validation should accept the binary patch (sniff finds
	// `diff --git`).
	if err := validatePatchFile(patchPath); err != nil {
		t.Fatalf("validatePatchFile rejected a git binary patch: %v", err)
	}

	// setupWorktree should also apply it cleanly.
	wt, cleanup, err := setupWorktree(context.Background(), srcDir, rev, "binary-key", []string{patchPath})
	if err != nil {
		t.Fatalf("setupWorktree with binary patch: %v", err)
	}
	defer cleanup()
	got, err := os.ReadFile(filepath.Join(wt, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xff, 0xfe, 0xfd, 0xfc, 0xfb}
	if !bytes.Equal(got, want) {
		t.Errorf("worktree binary content = %x; want %x", got, want)
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

// gitInRepo runs `git <args>` inside dir, failing the test on error.
// Mirrors initSmallRepo's pattern; pulled out so the new
// non-HEAD-revision and binary-patch tests can drive git directly.
func gitInRepo(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
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
