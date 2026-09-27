//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// This file is dl/confine-dirfrom's no-follow helpers (its init_linux.go),
// copied verbatim for the tile-sandbox init (agentfd_linux.go): whichever
// branch lands second deletes this file.

// beneath reports whether the clean absolute path p lies strictly under dir.
func beneath(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}

// nestedPoint opens the mount point name beneath dirfd (at is its path in
// the sandbox, for errors), making it when missing: a directory, or an empty
// file when isDir is false.
func nestedPoint(dirfd int, name, at string, isDir bool) (int, error) {
	flags := unix.O_PATH
	if isDir {
		flags |= unix.O_DIRECTORY
	}
	fd, err := openNoFollow(dirfd, name, flags)
	if errors.Is(err, unix.ENOENT) {
		// neither follows a symlink in name's place: both fail with EEXIST
		if isDir {
			err = unix.Mkdirat(dirfd, name, 0o755)
		} else {
			var f int
			if f, err = unix.Openat(dirfd, name, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644); err == nil {
				unix.Close(f)
			}
		}
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, must(err, "make mount point "+at)
		}
		fd, err = openNoFollow(dirfd, name, flags)
	}
	switch {
	case err == nil:
		return fd, nil
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return -1, fmt.Errorf("nested mount point %s: a symlink is in the way", at)
	case errors.Is(err, unix.ENOTDIR):
		return -1, fmt.Errorf("nested mount point %s: not a directory", at)
	default:
		return -1, must(err, "open "+at)
	}
}

// openNoFollow opens one name beneath dirfd without following a symlink:
// openat2 with RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS, or, on a kernel before
// 5.6, openat with O_NOFOLLOW and a type check, which for a single plain name
// is the same guarantee.
func openNoFollow(dirfd int, name string, flags int) (int, error) {
	fd, err := unix.Openat2(dirfd, name, &unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if !errors.Is(err, unix.ENOSYS) {
		return fd, err
	}
	if fd, err = unix.Openat(dirfd, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0); err != nil {
		return -1, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
		err = unix.ELOOP
	}
	if err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// fdPath names an open fd for mount(2): the kernel resolves the magic link
// to the very file the fd holds, whatever its path now resolves to.
func fdPath(fd int) string { return "/proc/self/fd/" + strconv.Itoa(fd) }
