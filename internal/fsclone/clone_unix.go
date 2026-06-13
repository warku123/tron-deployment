//go:build darwin || linux

package fsclone

import (
	"errors"

	"golang.org/x/sys/unix"
)

// isUnsupportedCoW reports whether err means "this filesystem / target
// can't do a copy-on-write clone" — as opposed to a real I/O failure.
// Only these errnos trigger CloneDir's byte-copy fallback:
//
//	EXDEV       cross-device link (src and dst on different filesystems)
//	EOPNOTSUPP  filesystem doesn't implement clone (ext4, tmpfs, NFS, …)
//	ENOTSUP     darwin's spelling of the same
//	ENOTTY      ioctl not supported by this file/fs
//	EINVAL      clone args rejected (e.g. non-reflink fs returning EINVAL)
//	ENOSYS      syscall absent on this kernel
//
// Everything else (EPERM, ENOSPC, EIO, ENOENT, …) is a genuine error and
// must propagate, never be masked as a slow copy.
func isUnsupportedCoW(err error) bool {
	for _, e := range []unix.Errno{
		unix.EXDEV,
		unix.EOPNOTSUPP,
		unix.ENOTSUP,
		unix.ENOTTY,
		unix.EINVAL,
		unix.ENOSYS,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
