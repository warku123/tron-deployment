package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/tronprotocol/tron-deployment/internal/output"
)

// computePatchHash hashes the canonical concatenation of patch file
// contents (in declared order) to a deterministic hex string.
// Used as Source.PatchHash when build.patches is non-empty (FR-026)
// so the cache key becomes a pure function of (revision + patches),
// independent of the operator's local working tree.
//
// Two patches with the same byte content but different filenames
// produce the SAME hash — only the contents matter, not the path.
// Order does matter (different application order can produce
// different end-state diffs for adjacent hunks), so the caller's
// declared order is preserved.
//
// Returns an error if any patch path is unreadable; FR-028 demands
// path validation at intent-load time so this is mostly a
// defense-in-depth check.
func computePatchHash(paths []string) (string, error) {
	if len(paths) == 0 {
		return "", nil
	}
	h := sha256.New()
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", fmt.Errorf("read patch %s: %w", p, err)
		}
		// NUL separator between patches so two adjacent patches
		// `A` + `B` and the single patch `AB` (which could happen
		// to byte-equal A||B in some edge case) yield different
		// hashes. Also includes the index so reordering changes
		// the hash even if file contents collide.
		fmt.Fprintf(h, "patch %d\x00", i)
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validatePatchFile checks that path exists, is a regular file, and
// looks like a unified diff (FR-028). The header check is heuristic
// — we don't try to parse the full diff format; that's git apply's
// job. The goal is to catch the common error of pointing the
// patches: list at a non-patch file (a YAML, a JAR, a stray binary).
func validatePatchFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("patch %s is not a regular file", path)
	}
	// Sniff the first 4 KB — enough to find a unified-diff header
	// without slurping a multi-MB patch into memory just to validate.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	// Common unified-diff markers. `diff --git ` (git format) or
	// the `--- ` / `+++ ` pair (POSIX patch format) or an `Index: `
	// header (svn/p4 style — git apply accepts it).
	if strings.Contains(head, "diff --git ") ||
		(strings.Contains(head, "\n--- ") && strings.Contains(head, "\n+++ ")) ||
		strings.HasPrefix(head, "--- ") ||
		strings.Contains(head, "\nIndex: ") ||
		strings.HasPrefix(head, "Index: ") {
		return nil
	}
	return fmt.Errorf("patch %s does not look like a unified diff "+
		"(no `diff --git`, `--- ` / `+++ ` pair, or `Index:` header in the first 4 KB)",
		path)
}

// setupWorktree creates a per-cache-key git worktree at
// `${cacheDir}/worktrees/<cache-key>/`, checks out the resolved
// revision, and applies each patch via `git apply`. Returns the
// worktree path (which becomes r.buildSourceDir for the duration
// of the build) and a cleanup func the caller defers.
//
// Worktrees share the parent repo's `.git/` object database via
// git's own primitive, so per-build disk overhead is the working
// tree only (~100-200 MB for java-tron), not the full repo.
//
// Lifecycle: setup on cache miss; cleanup ALWAYS via defer
// (success OR failure). Cache hits skip both setup and cleanup
// entirely — the artifact is already in out/ and the worktree is
// just a build-time staging area.
func setupWorktree(ctx context.Context, srcPath, revision, cacheKey string, patches []string) (worktreePath string, cleanup func(), err error) {
	worktreePath = filepath.Join(CacheDir(), "worktrees", cacheKey)

	// Defensive cleanup of any stale worktree from a prior cancelled
	// run. `git worktree remove --force` handles both the worktree
	// dir + the bookkeeping link under parent's .git/worktrees/.
	_ = removeWorktree(ctx, srcPath, worktreePath)

	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o700); err != nil {
		return "", nil, fmt.Errorf("mkdir worktrees parent: %w", err)
	}

	// `git worktree add --detach <path> <revision>` creates a
	// detached HEAD checkout — no branch fuss, deterministic state.
	addCmd := exec.CommandContext(ctx, "git", "-C", srcPath,
		"worktree", "add", "--detach", worktreePath, revision)
	if out, err := addCmd.CombinedOutput(); err != nil {
		return "", nil, fmt.Errorf("git worktree add: %w: %s",
			err, strings.TrimSpace(string(out)))
	}

	cleanup = func() {
		// Best-effort: a stuck `git worktree remove` shouldn't fail
		// a build whose actual work succeeded. We log to stderr so
		// an operator can clean up manually if it persists.
		if rmErr := removeWorktree(context.Background(), srcPath, worktreePath); rmErr != nil {
			fmt.Fprintf(os.Stderr,
				"warning: cleanup worktree %s: %v\n", worktreePath, rmErr)
		}
	}

	// Apply each patch. `git apply --check` first to surface a clean
	// PATCH_FAILED error pointing at the offending file; then the
	// real `git apply` to mutate the worktree.
	for _, p := range patches {
		if err := applyPatch(ctx, worktreePath, p); err != nil {
			cleanup()
			return "", nil, err
		}
	}

	return worktreePath, cleanup, nil
}

// applyPatch runs `git apply --check` then `git apply` inside the
// worktree. Returns a PATCH_FAILED StructuredError on failure so
// the apply / build CLI flows surface a clean error envelope with
// exit code 2 (validation error).
func applyPatch(ctx context.Context, worktreePath, patchPath string) error {
	// --check is read-only; bails before any mutation if the patch
	// won't cleanly apply against the current tree.
	checkCmd := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"apply", "--check", patchPath)
	if out, err := checkCmd.CombinedOutput(); err != nil {
		return output.NewErrorf("PATCH_FAILED", output.ExitValidationError,
			"patch %s does not cleanly apply against the source revision: %s",
			patchPath, strings.TrimSpace(string(out))).
			WithSuggestions(
				"Verify the patch was generated against the target source tree's git revision",
				"If the patch is for a different revision, set build.revision: <sha> to match",
			)
	}
	applyCmd := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"apply", patchPath)
	if out, err := applyCmd.CombinedOutput(); err != nil {
		return output.NewErrorf("PATCH_FAILED", output.ExitGeneralError,
			"patch %s passed --check but failed mid-apply: %s",
			patchPath, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeWorktree tears down a worktree, also unregistering the
// bookkeeping link under parent's `.git/worktrees/`. `--force` so
// even a worktree with local edits (i.e. the patches we applied)
// is removed without fuss.
func removeWorktree(ctx context.Context, srcPath, worktreePath string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", srcPath,
		"worktree", "remove", "--force", worktreePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// `git worktree remove` errors when the worktree dir
		// doesn't exist (which is fine on first call). Distinguish
		// real failures from the "nothing to remove" case.
		if strings.Contains(string(out), "is not a working tree") ||
			strings.Contains(string(out), "no such file") {
			// Best-effort manual cleanup in case the .git/worktrees
			// link survived a previous force-kill.
			_ = os.RemoveAll(worktreePath)
			return nil
		}
		return fmt.Errorf("git worktree remove: %w: %s",
			err, strings.TrimSpace(string(out)))
	}
	return nil
}
