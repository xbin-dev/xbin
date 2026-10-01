//go:build unix

package wssettings

import (
	"os"
	"syscall"
)

// inode is the file's inode number: an atomic write (a rename) changes it.
func inode(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
