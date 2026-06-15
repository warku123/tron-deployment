//go:build darwin

package fsclone

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// cloneMethodName labels a successful darwin clone in CloneDir's result.
const cloneMethodName = "clonefile"

// cloneTree clones src to dst via clonefile(2). Unlike Linux's per-file
// FICLONE, clonefile recursively clones an entire directory hierarchy in
// a single syscall when src is a directory, so there's no manual walk on
// darwin. dst must not exist (clonefile creates it).
//
// flags=0: clonefile follows a top-level symlink src and clones nested
// entries structurally. The fsclone contract assumes regular files +
// dirs only, so this is fine for the snapshot stores we clone.
func cloneTree(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if err == nil {
		return nil
	}
	if isUnsupportedCoW(err) {
		// e.g. cloning across volumes (EXDEV) or onto a non-APFS target.
		return fmt.Errorf("fsclone: clonefile %s: %w", src, errUnsupported)
	}
	return fmt.Errorf("fsclone: clonefile %s -> %s: %w", src, dst, err)
}
