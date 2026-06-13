//go:build linux

package fsclone

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// cloneMethodName labels a successful Linux clone in CloneDir's result.
const cloneMethodName = "ficlone"

// cloneTree reflinks src into dst. FICLONE is per-file (not recursive
// like darwin's clonefile), so we recreate the directory structure and
// reflink each regular file.
//
// All-or-nothing semantics: the first file the filesystem can't reflink
// returns errUnsupported, which makes CloneDir discard the partial tree
// and do one clean byte copy of the whole source — simpler and more
// predictable than a half-cloned/half-copied tree, and Go's io.Copy
// already uses copy_file_range/sendfile under the hood, so the byte-copy
// fallback is itself kernel-accelerated where possible.
func cloneTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)

		if d.IsDir() {
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !d.Type().IsRegular() {
			// Symlinks / sockets / devices: FICLONE can't handle them.
			// Signal unsupported so the whole tree falls back to the byte
			// copy, keeping clone and copy semantics consistent.
			return fmt.Errorf("fsclone: non-regular file %s: %w", path, errUnsupported)
		}
		return ficloneFile(path, target)
	})
}

// ficloneFile reflinks a single regular file src -> dst via the FICLONE
// ioctl, creating dst's parent dir and preserving src's permission bits.
func ficloneFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()

	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err != nil {
		if isUnsupportedCoW(err) {
			return fmt.Errorf("fsclone: ficlone %s: %w", src, errUnsupported)
		}
		return fmt.Errorf("fsclone: ficlone %s -> %s: %w", src, dst, err)
	}
	return nil
}
