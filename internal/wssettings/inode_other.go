//go:build !unix

package wssettings

import "os"

// inode: none here; mtime and size tell a change.
func inode(os.FileInfo) uint64 { return 0 }
