package fsclone

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// recursiveCopy is the byte-copy fallback used when a copy-on-write clone
// isn't available. It is the single recursive-copy implementation in the
// tree (the dbfork equivalence gate used to carry its own copyDir; that
// has been deleted in favour of this).
//
// It mirrors the clone contract: preserves content and permission bits,
// creates dst, and dereferences symlinks (os.Open follows the link and
// copies the target's bytes as a regular file). xattrs, ACLs, ownership,
// and hardlink topology are NOT preserved.
//
// On Linux io.Copy uses copy_file_range/sendfile under the hood for
// regular *os.File pairs, so even this "fallback" is kernel-accelerated
// where the kernel supports it — there's no separate copy_file_range
// path to maintain.
func recursiveCopy(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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
		return copyFile(path, target)
	})
}

// copyFile copies one file src -> dst, creating dst's parent directory
// and preserving src's permission bits.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := os.Stat(src)
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
	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("fsclone: copy %s -> %s: %w", src, dst, err)
	}
	return nil
}
