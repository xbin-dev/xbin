//go:build unix

package fsutil

// beneath_unix.go — the fallback walk (re-review of 2026-11-20, L3): where
// openat2 is unavailable, a path that must hold no link is opened one step
// at a time, each step openat'd on the directory fd the step before
// opened, with O_NOFOLLOW (a link at any step fails the open itself) and
// O_DIRECTORY on the way: nothing is checked by name and then opened, so a
// link swapped in between is never followed.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	walkDirFlags  = unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	walkFileFlags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_NOFOLLOW | unix.O_CLOEXEC
)

func closeFd(fd int) { _ = unix.Close(fd) }

// walkErr is a step's failure, an openat of seg on dfd: a link there is
// ErrEscapes — ELOOP (EMLINK on the BSDs), or ENOTDIR where O_DIRECTORY
// met it first, told apart from a file by an fstatat after the open has
// already refused it (only the word depends on it).
func walkErr(op string, dfd int, seg, name string, err error) error {
	link := errors.Is(err, unix.ELOOP) || errors.Is(err, unix.EMLINK)
	if errors.Is(err, unix.ENOTDIR) {
		var st unix.Stat_t
		link = unix.Fstatat(dfd, seg, &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK
	}
	if link {
		return &os.PathError{Op: op, Path: name, Err: ErrEscapes}
	}
	return &os.PathError{Op: op, Path: name, Err: err}
}

// walkDir opens root (the trust boundary: it may be reached through links)
// and then each step of rel from the fd before, never through a link: the
// directory's fd, the caller's to close.
func walkDir(root, rel string) (int, error) {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &os.PathError{Op: "open", Path: root, Err: err}
	}
	if rel == "" || rel == "." {
		return fd, nil
	}
	name := root
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == "" || seg == "." {
			continue
		}
		name = filepath.Join(name, seg)
		if seg == ".." {
			closeFd(fd)
			return -1, &os.PathError{Op: "open", Path: name, Err: ErrEscapes}
		}
		next, err := unix.Openat(fd, seg, walkDirFlags, 0)
		if err != nil {
			err = walkErr("open", fd, seg, name, err)
			closeFd(fd)
			return -1, err
		}
		closeFd(fd)
		fd = next
	}
	return fd, nil
}

// openNoSymlinksFallback is OpenNoSymlinks by the walk: every directory of
// rel by walkDir, the last step openat'd with O_NOFOLLOW and O_NONBLOCK.
func openNoSymlinksFallback(root, rel string) (*os.File, error) {
	rel = filepath.Clean(filepath.FromSlash(rel))
	if rel == "." {
		fd, err := walkDir(root, "")
		if err != nil {
			return nil, err
		}
		return fileOf(fd, root)
	}
	dir, base := filepath.Split(rel)
	dfd, err := walkDir(root, filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	defer closeFd(dfd)
	full := filepath.Join(root, rel)
	if base == ".." {
		return nil, &os.PathError{Op: "open", Path: full, Err: ErrEscapes}
	}
	fd, err := unix.Openat(dfd, base, walkFileFlags, 0)
	if err != nil {
		return nil, walkErr("open", dfd, base, full, err)
	}
	return fileOf(fd, full)
}

// mkdirAllInFallback is MkdirAllIn by the walk: each step made (mkdirat
// never follows its last component) and then openat'd with O_NOFOLLOW on
// the fd before it.
func mkdirAllInFallback(root, sub string, perm os.FileMode) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: root, Err: err}
	}
	name := root
	for _, seg := range strings.Split(filepath.ToSlash(sub), "/") {
		if seg == "" || seg == "." {
			continue
		}
		name = filepath.Join(name, seg)
		if err := unix.Mkdirat(fd, seg, uint32(perm.Perm())); err != nil && !errors.Is(err, unix.EEXIST) {
			closeFd(fd)
			return &os.PathError{Op: "mkdir", Path: name, Err: err}
		}
		next, err := unix.Openat(fd, seg, walkDirFlags, 0)
		if err != nil {
			err = walkErr("mkdir", fd, seg, name, err)
			closeFd(fd)
			return err
		}
		closeFd(fd)
		fd = next
	}
	closeFd(fd)
	return nil
}

// fileOf admits a freshly opened (non-blocking) fd as a regular file or a
// directory — anything else is closed and refused — and hands it back in
// blocking mode.
func fileOf(fd int, name string) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		closeFd(fd)
		return nil, &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if t := st.Mode & unix.S_IFMT; t != unix.S_IFREG && t != unix.S_IFDIR {
		closeFd(fd)
		return nil, &os.PathError{Op: "open", Path: name, Err: ErrNotRegular}
	}
	_ = unix.SetNonblock(fd, false)
	return os.NewFile(uintptr(fd), name), nil
}
