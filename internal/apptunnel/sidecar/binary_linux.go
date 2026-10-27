//go:build linux

package sidecar

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// openExe returns the binary to run, skipping the extraction kinds before
// from (a kind whose exec failed at launch time).
func (o Options) openExe(from string) (*exe, error) {
	if p := o.binaryPath(); p != "" {
		f, err := os.Open(p)
		if err != nil {
			return nil, fmt.Errorf("XBIN_IROH: %w", err)
		}
		if err := canExec(f); err != nil {
			f.Close()
			return nil, fmt.Errorf("XBIN_IROH %s: %w", p, err)
		}
		return &exe{f: f, how: "path"}, nil
	}
	if len(embedded) == 0 {
		return nil, ErrUnavailable
	}
	bin, err := gunzip(embedded)
	if err != nil {
		return nil, fmt.Errorf("the embedded iroh sidecar: %w", err)
	}
	var errs []error
	skip := from != ""
	for _, kind := range execKinds {
		if skip && kind != from {
			continue
		}
		skip = false
		var f *os.File
		switch kind {
		case "memfd":
			f, err = memfdExe(bin)
		case "rundir":
			f, err = fileExe(o.RunDir, "iroh", bin)
		case "workspace":
			f, err = fileExe(o.Workspace, filepath.Join(".xbin", "iroh"), bin)
		}
		if err == nil {
			return &exe{f: f, how: kind}, nil
		}
		errs = append(errs, fmt.Errorf("%s: %w", kind, err))
	}
	return nil, fmt.Errorf("no place can exec the iroh sidecar: %w", errors.Join(errs...))
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(zr)
}

// canExec asks the kernel whether f can be exec'd: its mode, and the
// noexec flag of the mount it lives on (access(2) checks both for X_OK).
// An LSM may still refuse at exec; the supervisor falls back then.
func canExec(f *os.File) error {
	return unix.Access("/proc/self/fd/"+strconv.Itoa(int(f.Fd())), unix.X_OK)
}

// memfdExe puts bin in a memfd sealed against every change.
func memfdExe(bin []byte) (*os.File, error) {
	fd, err := unix.MemfdCreate("xbin-iroh", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_EXEC)
	if err != nil {
		// before 6.3 the kernel doesn't know MFD_EXEC (EINVAL); exec is
		// then a memfd's default. Under vm.memfd_noexec=2 it is refused
		// (EACCES) and the retry makes a noexec one, which canExec rejects.
		fd, err = unix.MemfdCreate("xbin-iroh", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	}
	if err != nil {
		return nil, fmt.Errorf("memfd_create: %w", err)
	}
	f := os.NewFile(uintptr(fd), "xbin-iroh")
	if err := writeAll(f, bin); err != nil {
		f.Close()
		return nil, err
	}
	seals := unix.F_SEAL_SEAL | unix.F_SEAL_SHRINK | unix.F_SEAL_GROW | unix.F_SEAL_WRITE
	if _, err := unix.FcntlInt(f.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		f.Close()
		return nil, fmt.Errorf("seal: %w", err)
	}
	if err := canExec(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("exec from a memfd: %w", err)
	}
	return f, nil
}

func writeAll(f *os.File, b []byte) error {
	for len(b) > 0 {
		n, err := f.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

// fileExe writes bin to <root>/<sub>/xbin-iroh (the directory 0700, the
// file 0700, by rename so a running copy is never rewritten) unless an
// identical copy is there, and opens it read-only.
func fileExe(root, sub string, bin []byte) (*os.File, error) {
	if root == "" {
		return nil, errors.New("no directory configured")
	}
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "xbin-iroh")
	if f, err := openSame(path, bin); err == nil {
		return f, nil
	}
	tmp, err := os.CreateTemp(dir, ".xbin-iroh-*")
	if err != nil {
		return nil, err
	}
	err = writeAll(tmp, bin)
	if err == nil {
		err = tmp.Chmod(0o700)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	f, err := openSame(path, bin)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// openSame opens path read-only if it holds exactly bin and can exec;
// the check reads the open file, so it is what runs.
func openSame(path string, bin []byte) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, err
	}
	if want := sha256.Sum256(bin); !bytes.Equal(h.Sum(nil), want[:]) {
		f.Close()
		return nil, errors.New("a different binary is there")
	}
	if err := canExec(f); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
