package snapshot

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tronprotocol/tron-deployment/internal/fsclone"
	"github.com/tronprotocol/tron-deployment/internal/output"
	"github.com/tronprotocol/tron-deployment/internal/target"
)

// clone wires `trond snapshot clone <src> <dst>` — a thin presentation
// layer over internal/fsclone.CloneDir. It exists so an operator/agent can
// build a warm pool of chain-DB fixtures: fork a cached snapshot (or a
// STOPPED node's data dir) into a fresh isolated directory in seconds via
// a copy-on-write clone, instead of re-downloading or re-copying 30-90GB.
//
//	src ──stat/guard──> CloneDir(src,dst) ──> {clonefile|ficlone|copy}
//	     same FS? ─no─> free-space preflight (full copy is guaranteed)
//	     dst ⊄ src?  (canonical-path containment guard)
//
// Mutating (creates dst) so it is CLI-only — deliberately NOT an MCP tool,
// keeping filesystem-mutating capability out of the read-only agent fleet.
var cloneCmd = &cobra.Command{
	Use:   "clone <src> <dst>",
	Short: "Copy-on-write clone a chain-DB directory into a fresh path",
	Long: `Clone an existing chain database directory (a downloaded snapshot, or a
STOPPED node's data dir) into a new, independent directory. When source
and destination live on the same filesystem this is a copy-on-write
clone (APFS clonefile / Linux FICLONE): seconds and near-zero extra disk
even for a 30-90GB store. Across filesystems (or on a filesystem without
CoW) it falls back to a full byte copy — a warning is printed and the
"method" field reports "copy".

<src> is cloned verbatim: point it at whatever level you want
materialised (the snapshot's output-directory, or its parent). <dst>
must not already exist — clone refuses rather than overwrite.

The source must be QUIESCENT. Cloning a running node's live database
yields an undefined point-in-time view; stop the node first.`,
	Example: `  # Fork a cached snapshot into an isolated fixture (instant on APFS/btrfs/xfs)
  trond snapshot clone ./output-directory ./rig-a/output-directory

  # Clone a stopped node's data dir, JSON output
  trond snapshot clone ~/.trond/deployments/fn0/output-directory ./fixture -o json`,
	Args: cobra.ExactArgs(2),
	RunE: runClone,
}

func runClone(cmd *cobra.Command, args []string) error {
	outputFmt, _ := cmd.Flags().GetString("output")
	src, dst := args[0], args[1]

	// Source must exist and be a directory. CloneDir re-checks, but we
	// want a clean VALIDATION_ERROR before any staging work.
	srcInfo, err := os.Stat(src)
	if err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("source %s: %v", src, err))
	}
	if !srcInfo.IsDir() {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("source %s is not a directory", src))
	}

	// Refuse a pre-existing destination — clone never overwrites (matches
	// CloneDir's contract). No --force / destructive path in trond.
	if _, statErr := os.Stat(dst); statErr == nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("destination %s already exists; choose a fresh path or remove it first", dst))
	} else if !os.IsNotExist(statErr) {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError,
			fmt.Sprintf("stat destination %s: %v", dst, statErr))
	}

	// Containment guard on CANONICAL paths. A dst inside src would make the
	// clone walk re-enter its own staging area (corruption/bloat). Raw
	// string-prefix checks are bypassable via symlinks/relative paths, so
	// resolve to absolute, symlink-free paths first.
	if err := assertDisjoint(src, dst); err != nil {
		return output.NewError("VALIDATION_ERROR", output.ExitValidationError, err.Error())
	}

	// Free-space preflight, but ONLY when CoW cannot fire (src and dst on
	// different filesystems → a full byte copy is guaranteed). A same-FS
	// CoW clone shares extents and needs ~no space, so a free>=size check
	// there would falsely refuse on a near-full disk.
	anc := firstExistingAncestor(dst)
	if sameFS, devErr := sameFilesystem(src, anc); devErr == nil && !sameFS {
		need, sizeErr := dirSize(src)
		free, freeErr := target.NewLocalTarget().DiskFree(cmd.Context(), anc)
		if sizeErr == nil && freeErr == nil && need > 0 && free < need {
			return output.NewError("DISK_SPACE_ERROR", output.ExitGeneralError,
				fmt.Sprintf("cross-filesystem clone of %s requires a full byte copy (~%d bytes) "+
					"but only %d bytes are free at %s", src, need, free, anc))
		}
	}

	start := time.Now()
	method, err := fsclone.CloneDir(src, dst)
	if err != nil {
		return output.NewError("CLONE_ERROR", output.ExitGeneralError, err.Error())
	}
	durMs := time.Since(start).Milliseconds()

	// CoW didn't fire — surface it loudly. The "fast clone" became a slow
	// full copy; the caller (and any human watching) should know.
	if method == methodCopyName {
		fmt.Fprintf(os.Stderr,
			"warning: copy-on-write unavailable (cross-filesystem or unsupported FS) — "+
				"fell back to a full byte copy of %s; this is slow and uses full disk\n", src)
	}

	res := map[string]any{
		"source":      src,
		"dest":        dst,
		"method":      method,
		"duration_ms": durMs,
	}
	if outputFmt == "json" {
		return output.WriteJSON(os.Stdout, res)
	}
	fmt.Printf("Cloned %s -> %s (method: %s, %dms)\n", src, dst, method, durMs)
	return nil
}

// methodCopyName mirrors fsclone's "copy" fallback label. Kept as a local
// const so the warning trigger doesn't hard-code a bare string twice.
const methodCopyName = "copy"

// assertDisjoint rejects src==dst, dst-inside-src, and src-inside-dst using
// canonical (absolute, symlink-resolved) paths. dst need not exist yet, so
// its nearest existing ancestor is resolved and the remainder re-appended.
func assertDisjoint(src, dst string) error {
	cs, err := canonical(src)
	if err != nil {
		return fmt.Errorf("resolve source %s: %v", src, err)
	}
	cd, err := canonicalDst(dst)
	if err != nil {
		return fmt.Errorf("resolve destination %s: %v", dst, err)
	}
	if cs == cd {
		return fmt.Errorf("source and destination resolve to the same path: %s", cs)
	}
	if within(cd, cs) {
		return fmt.Errorf("destination %s is inside source %s; choose a destination outside the source tree", dst, src)
	}
	if within(cs, cd) {
		return fmt.Errorf("source %s is inside destination %s", src, dst)
	}
	return nil
}

// canonical returns the absolute, symlink-resolved form of an existing path.
func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// canonicalDst canonicalises a path that may not exist yet: resolve symlinks
// on the nearest existing ancestor, then re-join the not-yet-created tail.
func canonicalDst(dst string) (string, error) {
	abs, err := filepath.Abs(dst)
	if err != nil {
		return "", err
	}
	anc := firstExistingAncestor(abs)
	resolvedAnc, err := filepath.EvalSymlinks(anc)
	if err != nil {
		return "", err
	}
	rest, err := filepath.Rel(anc, abs)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedAnc, rest), nil
}

// within reports whether child is strictly inside parent (not equal).
func within(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// firstExistingAncestor walks up from an absolute path until it finds a
// directory that exists (the filesystem root always does).
func firstExistingAncestor(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = p
	}
	for {
		if _, statErr := os.Stat(abs); statErr == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return abs
		}
		abs = parent
	}
}

// sameFilesystem reports whether two existing paths sit on the same device,
// i.e. whether a copy-on-write clone between them can share extents.
func sameFilesystem(a, b string) (bool, error) {
	var sa, sb syscall.Stat_t
	if err := syscall.Stat(a, &sa); err != nil {
		return false, err
	}
	if err := syscall.Stat(b, &sb); err != nil {
		return false, err
	}
	return sa.Dev == sb.Dev, nil
}

// dirSize sums the apparent size of all regular files under root.
func dirSize(root string) (uint64, error) {
	var total uint64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type().IsRegular() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}
