//go:build !darwin && !linux

package fsclone

import "fmt"

// cloneMethodName is unused on platforms without a clone syscall — every
// CloneDir call falls back to the byte copy — but must exist so clone.go
// compiles. The "copy" value keeps the result string honest if it ever
// surfaces.
const cloneMethodName = methodCopy

// cloneTree always reports unsupported on platforms with no copy-on-write
// syscall (e.g. Windows), so CloneDir does a plain byte copy.
func cloneTree(src, dst string) error {
	return fmt.Errorf("fsclone: no copy-on-write syscall on this platform: %w", errUnsupported)
}
