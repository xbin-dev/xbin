//go:build linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const readFlags = unix.O_RDONLY | unix.O_NONBLOCK | unix.O_NOCTTY | unix.O_CLOEXEC

func openIn(root, sub, rel string) (*os.File, error) {
	rfd, err := unix.Open(root, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: root, Err: err}
	}
	defer unix.Close(rfd)
	dfd, name := rfd, root
	if sub != "" && sub != "." {
		name = filepath.Join(root, filepath.FromSlash(sub))
		sfd, err := unix.Openat2(rfd, filepath.FromSlash(sub), &unix.OpenHow{
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
	fd, err := unix.Openat2(dfd, filepath.FromSlash(rel), &unix.OpenHow{
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
	fd, err := unix.Openat2(bfd, filepath.FromSlash(r), &unix.OpenHow{
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
