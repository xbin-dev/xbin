//go:build linux

package fsutil

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func openBeneath(dir, rel string) (*os.File, error) {
	dfd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir, Err: err}
	}
	defer unix.Close(dfd)
	if rel == "" {
		rel = "."
	}
	fd, err := unix.Openat2(dfd, filepath.FromSlash(rel), &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_MAGICLINKS,
	})
	if errors.Is(err, unix.ENOSYS) {
		return openBeneathFallback(dir, rel) // pre-5.6 kernel
	}
	if errors.Is(err, unix.EXDEV) {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, rel), Err: ErrEscapes}
	}
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, rel), Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir, rel)), nil
}
