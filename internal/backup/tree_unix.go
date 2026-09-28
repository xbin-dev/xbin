//go:build unix

package backup

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// openNoFollow opens dir/name relative to dir's fd without following a
// symlink at name, admitting only a directory (wantDir) or a regular file.
// Anything else — the entry was swapped since the listing — is errGone. The
// open never blocks (a FIFO swapped in must not hang xbind).
func openNoFollow(dir *os.File, name string, wantDir bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC
	if wantDir {
		flags |= unix.O_DIRECTORY
	}
	full := filepath.Join(dir.Name(), name)
	fd, err := unix.Openat(int(dir.Fd()), name, flags, 0)
	switch {
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EMLINK), errors.Is(err, unix.ENOTDIR),
		errors.Is(err, unix.ENOENT), errors.Is(err, unix.ENXIO):
		return nil, errGone // ELOOP (EMLINK on the BSDs): a symlink now
	case err != nil:
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: full, Err: err}
	}
	want := uint32(unix.S_IFREG)
	if wantDir {
		want = unix.S_IFDIR
	}
	if uint32(st.Mode)&unix.S_IFMT != want {
		unix.Close(fd)
		return nil, errGone
	}
	_ = unix.SetNonblock(fd, false)
	return os.NewFile(uintptr(fd), full), nil
}
