// Package fsclone materialises a directory tree as an independent copy,
// preferring a copy-on-write clone (APFS clonefile on macOS, reflink /
// FICLONE on Linux) and falling back to a byte copy when the platform
// or filesystem can't reflink.
//
// Why this exists: callers that stand up throwaway copies of a large
// on-disk tree — the dbfork equivalence gate clones an 8-store Nile
// snapshot twice per run — pay a multi-GB recursive copy each time once
// the source is cached locally. A CoW clone shares the underlying
// extents, so the copy is created in milliseconds at near-zero extra
// space; the first write to a block transparently splits it off, so the
// clone is fully independent of the source.
//
// Contract (intentionally narrow — see Task #185 review):
//   - The clone preserves file CONTENT and permission bits. It does NOT
//     preserve xattrs, ACLs, hardlink topology, or ownership.
//   - Symlinks: the tree is expected to contain only regular files and
//     directories (true for LevelDB/RocksDB stores). The clone path
//     treats a non-regular entry as "unsupported" and falls back to the
//     byte copy, which dereferences symlinks. Don't rely on symlink
//     fidelity.
//   - The source must be quiescent. Cloning a live, mutating directory
//     (e.g. a running node's DB) gives an undefined point-in-time view.
//   - dst MUST NOT already exist — CloneDir refuses rather than merge or
//     overwrite, so it can never clobber caller data.
//
// CoW vs copy is transparent: the only observable difference is speed
// and the returned method string.
package fsclone

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// errUnsupported is returned (wrapped) by a platform cloneTree when the
// filesystem can't do a copy-on-write clone — a cross-device link, a
// non-reflink filesystem, or a platform with no clone syscall. It is the
// ONLY error class that triggers the byte-copy fallback; every other
// error (permission denied, ENOSPC, unreadable source) propagates so a
// real failure is never silently masked as a slow copy.
var errUnsupported = errors.New("fsclone: copy-on-write not supported")

// Method names returned by CloneDir, for observability (test logs, the
// CI CoW probe). Coarse by design: "the whole tree cloned" vs "fell back
// to a byte copy". cloneMethodName is the platform's CoW label
// ("clonefile" on darwin, "ficlone" on linux); methodCopy is the
// fallback.
const methodCopy = "copy"

// platformClone is the copy-on-write implementation for the current OS,
// bound to the build-tagged cloneTree. It is a package var (not a direct
// call) purely as a test seam: clone_test.go swaps it to return
// errUnsupported so the fallback orchestration is exercised
// deterministically on any host, including CoW filesystems where the
// real clone would always succeed.
var platformClone = cloneTree

// CloneDir materialises src at dst as an independent copy and reports the
// method used ("clonefile" | "ficlone" | "copy").
//
// It stages the work in a temp directory it creates as a sibling of dst
// (so the temp lands on dst's filesystem — required for the rename to be
// atomic and for the CoW clone to share extents), then renames the
// staged tree into place. On any failure only the primitive's own temp
// dir is removed; caller data at or around dst is never touched.
//
// dst must not exist. src must exist and be readable.
func CloneDir(src, dst string) (method string, err error) {
	if _, statErr := os.Stat(src); statErr != nil {
		return "", fmt.Errorf("fsclone: source %s: %w", src, statErr)
	}
	if _, statErr := os.Stat(dst); statErr == nil {
		return "", fmt.Errorf("fsclone: destination %s already exists", dst)
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return "", fmt.Errorf("fsclone: stat destination %s: %w", dst, statErr)
	}

	parent := filepath.Dir(dst)
	if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
		return "", fmt.Errorf("fsclone: create parent %s: %w", parent, mkErr)
	}
	// Stage in a temp dir on dst's filesystem. The clone/copy builds into
	// staged, then we rename staged -> dst (atomic, same filesystem).
	tmp, mkErr := os.MkdirTemp(parent, ".fsclone-*")
	if mkErr != nil {
		return "", fmt.Errorf("fsclone: create staging dir in %s: %w", parent, mkErr)
	}
	// Removes the empty husk after a successful rename, or the partial
	// tree on error. Best-effort temp cleanup — a failure here is
	// non-actionable, so the error is deliberately discarded.
	defer func() { _ = os.RemoveAll(tmp) }()
	staged := filepath.Join(tmp, "payload")

	method = cloneMethodName
	if cloneErr := platformClone(src, staged); cloneErr != nil {
		if !errors.Is(cloneErr, errUnsupported) {
			return "", cloneErr // real error — do NOT mask as a slow copy
		}
		// CoW genuinely unavailable: drop any partial clone and do a full
		// byte copy into a clean staging path.
		if rmErr := os.RemoveAll(staged); rmErr != nil {
			return "", fmt.Errorf("fsclone: clean partial clone %s: %w", staged, rmErr)
		}
		if cpErr := recursiveCopy(src, staged); cpErr != nil {
			return "", cpErr
		}
		method = methodCopy
	}

	if renErr := os.Rename(staged, dst); renErr != nil {
		return "", fmt.Errorf("fsclone: finalize %s -> %s: %w", staged, dst, renErr)
	}
	return method, nil
}
