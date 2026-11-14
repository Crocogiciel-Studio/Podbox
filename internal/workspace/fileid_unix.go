//go:build !windows

package workspace

import (
	"io/fs"
	"syscall"
)

// fileID returns the device and inode of fi, which a rename keeps.
func fileID(fi fs.FileInfo) (dev, ino uint64, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return uint64(st.Dev), uint64(st.Ino), true //nolint:unconvert // Dev is int32 on darwin
}
