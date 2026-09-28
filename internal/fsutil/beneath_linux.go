//go:build linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const readFlags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC

// openat2Tries bounds openat2's retries on EAGAIN.
const openat2Tries = 16

// openat2 is unix.Openat2, tried again while it answers EAGAIN: under
// RESOLVE_BENEATH the kernel gives up on a ".." step that races a rename
// anywhere on the host and asks the caller to retry (openat2(2)). Without
// the retry a legitimate in-tree "../x" fails as a plain error on a busy
// host, and a link that climbs out (a checkpoint's deps/) is never seen as
// ErrEscapes. Past openat2Tries the EAGAIN is the answer.
func openat2(dirfd int, path string, how *unix.OpenHow) (fd int, err error) {
	for try := 0; ; try++ {
		fd, err = unix.Openat2(dirfd, path, how)
		if !errors.Is(err, unix.EAGAIN) || try+1 >= openat2Tries {
			return fd, err
		}
	}
}

func openIn(root, sub, rel string) (*os.File, error) {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}
	defer unix.Close(rfd)
	dfd, name := rfd, root
	if sub != "" && sub != "." {
		name = filepath.Join(root, filepath.FromSlash(sub))
		sfd, err := openat2(rfd, filepath.FromSlash(sub), &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		switch {
		case errors.Is(err, unix.ENOSYS):
			return openInFallback(root, sub, rel) // pre-5.6 kernel
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return nil, &os.PathError{Op: "open", Path: name, Err: ErrEscapes}
		case err != nil:
			return nil, &os.PathError{Op: "open", Path: name, Err: err}
		}
		defer unix.Close(sfd)
		dfd = sfd
	}
	if rel == "" {
		rel = "."
	}
	full := filepath.Join(name, filepath.FromSlash(rel))
	fd, err := openat2(dfd, filepath.FromSlash(rel), &unix.OpenHow{
		Flags:   readFlags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	switch {
	case errors.Is(err, unix.ENOSYS):
		return openInFallback(root, sub, rel)
	case errors.Is(err, unix.EXDEV):
		return nil, &os.PathError{Op: "open", Path: full, Err: ErrEscapes}
	case err != nil:
		return nil, &os.PathError{Op: "open", Path: full, Err: err}
	}
	return fileOf(fd, full)
}

// mkdirAllIn makes each step of sub with mkdirat on the directory fd the
// previous step opened with RESOLVE_NO_SYMLINKS: mkdir never follows its
// last component, and the open refuses a link, so nothing is made or
// entered anywhere but beneath root, along sub, whatever races.
func mkdirAllIn(root, sub string, perm os.FileMode) error {
	fd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &os.PathError{Op: "open", Path: root, Err: err}
	}
	name := root
	for _, seg := range strings.Split(filepath.ToSlash(sub), "/") {
		name = filepath.Join(name, seg)
		if err := unix.Mkdirat(fd, seg, uint32(perm.Perm())); err != nil && !errors.Is(err, unix.EEXIST) {
			unix.Close(fd)
			return &os.PathError{Op: "mkdir", Path: name, Err: err}
		}
		next, err := unix.Openat2(fd, seg, &unix.OpenHow{
			Flags:   unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC,
			Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
		})
		unix.Close(fd)
		switch {
		case errors.Is(err, unix.ENOSYS):
			return mkdirAllInFallback(root, sub, perm) // pre-5.6 kernel
		case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
			return &os.PathError{Op: "mkdir", Path: name, Err: ErrEscapes}
		case err != nil:
			return &os.PathError{Op: "mkdir", Path: name, Err: err}
		}
		fd = next
	}
	unix.Close(fd)
	return nil
}

func openResolved(root, rel string, allow func(string) bool) (*os.File, string, error) {
	base, real, r, err := resolveIn(root, rel, allow)
	if err != nil {
		return nil, "", err
	}
	bfd, err := unix.Open(base, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", &os.PathError{Op: "open", Path: base, Err: err}
	}
	defer unix.Close(bfd)
	fd, err := openat2(bfd, filepath.FromSlash(r), &unix.OpenHow{
		Flags:   readFlags,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	switch {
	case errors.Is(err, unix.ENOSYS):
		return openResolvedFallback(real, r)
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		// The resolved path grew a symlink (or left root) after it was
		// resolved: a racing writer. Refuse; never re-resolve.
		return nil, "", &os.PathError{Op: "open", Path: real, Err: ErrEscapes}
	case err != nil:
		return nil, "", &os.PathError{Op: "open", Path: real, Err: err}
	}
	f, err := fileOf(fd, real)
	return f, r, err
}

// fileOf admits a freshly opened (non-blocking) fd as a regular file or a
// directory — anything else is closed and refused — and hands it back in
// blocking mode.
func fileOf(fd int, name string) (*os.File, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &os.PathError{Op: "fstat", Path: name, Err: err}
	}
	if t := st.Mode & unix.S_IFMT; t != unix.S_IFREG && t != unix.S_IFDIR {
		unix.Close(fd)
		return nil, &os.PathError{Op: "open", Path: name, Err: ErrNotRegular}
	}
	_ = unix.SetNonblock(fd, false)
	return os.NewFile(uintptr(fd), name), nil
}
