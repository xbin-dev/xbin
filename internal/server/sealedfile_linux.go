//go:build linux

package server

import (
	"os"

	"golang.org/x/sys/unix"
)

// memFile is an anonymous in-memory file holding data, positioned at its
// start (memfd_create: no name in any filesystem).
func memFile(data []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("xbin-sealed", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "xbin-sealed")
	if _, err := f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if _, err := f.Seek(0, 0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
