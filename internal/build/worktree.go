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

// buildPatchRecords reads each patch file once and returns a parallel
// list of (Name, SHA256) records that downstream code uses for both:
//
//   - the deterministic PatchHash that flows into the cache key
//     (see computePatchHashFromRecords)
//   - the Manifest.Patches list that `trond build inspect` exposes
//     (so an operator can verify their local patch files still
//     match the content sha256 that produced the cached artifact —
//     independent of where on disk those files now live).
//
// One pass over each file is enough: the per-patch SHA256 is the
// only fingerprint we ever need.
func buildPatchRecords(paths []string) ([]PatchRecord, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	out := make([]PatchRecord, 0, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("read patch %s: %w", p, err)
		}
		sum := sha256.Sum256(data)
		out = append(out, PatchRecord{
			Name:   filepath.Base(p),
			SHA256: hex.EncodeToString(sum[:]),
		})
	}
	return out, nil
}

// computePatchHashFromRecords folds an ordered list of PatchRecord
// SHA256s into a single deterministic hex hash used as Source.PatchHash
// when build.patches is non-empty (FR-026).
//
// Pure function of inputs. Two operators with the same patch file
// contents in the same order get the same hash, regardless of the
// patches' filesystem paths or surrounding working-tree state.
//
// Order matters (different application order can produce different
// end-state diffs for adjacent hunks); identical contents under
// different filenames hash identically (only sha256 participates).
func computePatchHashFromRecords(records []PatchRecord) string {
	if len(records) == 0 {
		return ""
	}
	h := sha256.New()
	for i, r := range records {
		// `patch <i>\x00<hex-sha256>\x00` per record. The index
		// prefix forces reorderings into different hashes even
		// when the sha256 set is unchanged; the trailing NUL
		// terminates the variable-length sha so two adjacent
		// records can't concatenate ambiguously.
		fmt.Fprintf(h, "patch %d\x00", i)
		h.Write([]byte(r.SHA256))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// validatePatchFile checks that path exists, is a regular file, and
// looks like a unified diff (FR-028). The header check is heuristic
// — we don't try to parse the full diff format; that's git apply's
// job. The goal is to catch the common error of pointing the
// patches: list at a non-patch file (a YAML, a JAR, a stray binary).
//
// Accepted formats (sufficient for `git apply` to consume):
//   - `diff --git a/... b/...` (git diff / git diff HEAD output)
//   - `--- ` / `+++ ` filename pair (POSIX unified diff)
//   - `Index: <path>` header (svn / p4 / git apply tolerates this)
//   - `From <40-hex-sha> Mon Sep ...` (git format-patch — mbox-style
//     emails with the diff embedded after the body). The actual
//     diff may be tens of KB into the file if the commit message is
//     long (release notes, signed-off-by chains), so we use a 256 KB
//     sniff window AND recognize the email prologue at byte 0.
func validatePatchFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("patch %s is not a regular file", path)
	}
	// Sniff up to 256 KB. `git format-patch` files can have commit
	// messages of arbitrary length before the diff body — release-
	// note commits, signed-off-by chains, and conventional-commit
	// "BREAKING CHANGE: ..." prose stack up. 64 KB was occasionally
	// too small in practice; 256 KB is enough for any realistic
	// commit message body without slurping a multi-MB patch entirely.
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("patch %s: %w", path, err)
	}
	defer f.Close()
	buf := make([]byte, 256*1024)
	n, _ := f.Read(buf)
	head := string(buf[:n])

	// Standard unified-diff markers.
	if strings.Contains(head, "diff --git ") ||
		(strings.Contains(head, "\n--- ") && strings.Contains(head, "\n+++ ")) ||
		strings.HasPrefix(head, "--- ") ||
		strings.Contains(head, "\nIndex: ") ||
		strings.HasPrefix(head, "Index: ") {
		return nil
	}
	// `git format-patch` mbox prologue: starts with `From <40-hex-sha>`
	// followed by mbox-style headers (Subject:, From:, Date:) and an
	// optional long commit body before the diff. The presence of the
	// `From <sha>` line at byte 0 is the canonical marker.
	if isGitFormatPatchHeader(head) {
		return nil
	}
	return fmt.Errorf("patch %s does not look like a unified diff "+
		"(no `diff --git`, `--- ` / `+++ ` pair, `Index:` header, or "+
		"`From <sha>` git format-patch header in the first 256 KB)",
		path)
}

// isGitFormatPatchHeader checks for the `From <40-hex-sha>` prologue
// that `git format-patch` writes at byte 0. The standard layout is
// `From abc123... Mon Sep 17 00:00:00 2001\n` followed by mbox headers.
func isGitFormatPatchHeader(head string) bool {
	if !strings.HasPrefix(head, "From ") {
		return false
	}
	rest := head[5:]
	// Need at least a 40-hex sha after "From ".
	if len(rest) < 41 || rest[40] != ' ' {
		return false
	}
	for i := range 40 {
		c := rest[i]
		// Accept both cases. `git format-patch` always emits
		// lowercase, but a downstream tool (cosign attest, custom
		// commit signers) may normalize to uppercase. Defensive
		// — the check is just disambiguating from non-sha prologues
		// like "From: Author Name <email>".
		isLower := c >= '0' && c <= '9' || c >= 'a' && c <= 'f'
		isUpper := c >= 'A' && c <= 'F'
		if !isLower && !isUpper {
			return false
		}
	}
	return true
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
