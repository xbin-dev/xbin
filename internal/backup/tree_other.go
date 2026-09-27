//go:build !unix

package backup

import (
	"os"
	"path/filepath"
)

// openNoFollow without openat: check, then open. A swap between the two can
// still win here; xbind runs on Linux (tree_unix.go).
func openNoFollow(dir *os.File, name string, wantDir bool) (*os.File, error) {
	p := filepath.Join(dir.Name(), name)
	fi, err := os.Lstat(p)
	if err != nil || fi.IsDir() != wantDir || (!wantDir && !fi.Mode().IsRegular()) {
		return nil, errGone
	}
	return os.Open(p)
}
