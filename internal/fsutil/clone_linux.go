//go:build linux

package fsutil

// clone_linux.go — copying a VM disk image and swapping two directories
// (plans/tile-sandbox-runtime.md §3.9). A tile sandbox's disk is a sparse
// file of up to hundreds of GiB that is mostly holes: a snapshot or a
// clone of it shares its extents where the filesystem can (a reflink), and
// otherwise copies only its data, so the copy stays as sparse as the
// original. A restore swaps a staged state dir with the live one in one
// rename, so a crash leaves one or the other.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// cloneChunk is the most one copy_file_range call moves: a cancelled copy
// stops within one.
const cloneChunk = 64 << 20

// CloneSparse copies the regular file src to dst, which it creates (0600;
// never over an existing file, never through a symlink at either path),
// and reports whether the copy is a reflink. Where the filesystem can
// share extents (FICLONE: btrfs, XFS) nothing is copied; otherwise only
// src's data is — SEEK_DATA/SEEK_HOLE find it, copy_file_range moves it
// (a read and write where the kernel can't) — and its holes stay holes.
// dst is fsynced. ctx cancels a copy between chunks; on any error dst is
// removed. Neither file is parsed: a disk is copied, never read as one.
func CloneSparse(ctx context.Context, src, dst string) (reflinked bool, err error) {
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("clone %s: not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return false, err
	}
	defer func() {
		if cerr := out.Close(); err == nil && cerr != nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(dst)
		}
	}()
	if err := unix.IoctlFileClone(int(out.Fd()), int(in.Fd())); err == nil {
		reflinked = true
	} else if err := copySparse(ctx, in, out, fi.Size()); err != nil {
		return false, fmt.Errorf("clone %s: %w", src, err)
	}
	if err := out.Sync(); err != nil {
		return false, err
	}
	return reflinked, nil
}

// copySparse sizes out to size and copies in's data extents into it at the
// same offsets: what lies between them stays a hole.
func copySparse(ctx context.Context, in, out *os.File, size int64) error {
	if err := out.Truncate(size); err != nil {
		return err
	}
	fd := int(in.Fd())
	for off := int64(0); off < size; {
		data, err := unix.Seek(fd, off, unix.SEEK_DATA)
		switch {
		case errors.Is(err, unix.ENXIO): // no data past off
			return nil
		case errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP): // no hole reporting: all of it is data
			return copyRange(ctx, in, out, off, size-off)
		case err != nil:
			return err
		}
		hole, err := unix.Seek(fd, data, unix.SEEK_HOLE)
		if err != nil {
			hole = size
		}
		if err := copyRange(ctx, in, out, data, min(hole, size)-data); err != nil {
			return err
		}
		off = hole
	}
	return nil
}

// copyRange copies n bytes at off from in to out, at the same offset.
func copyRange(ctx context.Context, in, out *os.File, off, n int64) error {
	roff, woff := off, off
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := unix.CopyFileRange(int(in.Fd()), &roff, int(out.Fd()), &woff, int(min(n, cloneChunk)), 0)
		switch {
		case err == nil && c == 0: // src shrank under us: what is left stays a hole
			return nil
		case err == nil:
			n -= int64(c)
		case errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EINVAL):
			return copyPlain(ctx, in, out, roff, n)
		case errors.Is(err, unix.EINTR):
		default:
			return err
		}
	}
	return nil
}

// copyPlain is copyRange by reads and writes.
func copyPlain(ctx context.Context, in, out *os.File, off, n int64) error {
	buf := make([]byte, 1<<20)
	for n > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		c, err := in.ReadAt(buf[:min(n, int64(len(buf)))], off)
		if c > 0 {
			if _, werr := out.WriteAt(buf[:c], off); werr != nil {
				return werr
			}
			off, n = off+int64(c), n-int64(c)
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// Exchange swaps the paths a and b — both must exist — in one
// renameat2(RENAME_EXCHANGE): at each name a reader finds one of the two,
// never neither, and a crash leaves them swapped or not.
func Exchange(a, b string) error {
	if err := unix.Renameat2(unix.AT_FDCWD, a, unix.AT_FDCWD, b, unix.RENAME_EXCHANGE); err != nil {
		return &os.LinkError{Op: "exchange", Old: a, New: b, Err: err}
	}
	return nil
}
