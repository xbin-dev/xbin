package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The fallbacks (non-Linux, pre-5.6 kernels, a seccomp profile that
// refuses openat2): what must never follow a link walks one step at a time
// from the root's fd with O_NOFOLLOW (walkDir, beneath_unix.go) — no
// lstat-then-open; only OpenBeneath's in-tree links (rel) and
// OpenResolved still resolve and compare, racy where openat2 is not.

func openInFallback(root, sub, rel string) (*os.File, error) {
	base, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	// sub: walked from root's fd one step at a time, never through a link
	// (walkDir) — what OpenRootIn and a tile's own directory rest on
	dfd, err := walkDir(root, filepath.Clean(filepath.FromSlash(sub)))
	if err != nil {
		return nil, err
	}
	if rel == "" || rel == "." {
		return fileOf(dfd, filepath.Join(root, filepath.FromSlash(sub)))
	}
	closeFd(dfd)
	// rel beneath sub, in-tree links allowed (OpenBeneath's contract): the
	// resolve-and-compare of old, racy where openat2 is not
	dir := filepath.Join(base, filepath.FromSlash(sub))
	full, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return nil, err
	}
	if !within(dir, full) {
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir, rel), Err: ErrEscapes}
	}
	return openChecked(full)
}

// resolveIn resolves root and root/rel and checks the result: inside root,
// and allow(root-relative path). Returns the resolved root, the resolved
// path, and the path relative to the resolved root (slash-separated).
func resolveIn(root, rel string, allow func(string) bool) (base, real, r string, err error) {
	if base, err = filepath.EvalSymlinks(root); err != nil {
		return "", "", "", err
	}
	if real, err = filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		return "", "", "", err
	}
	if !within(base, real) {
		return "", "", "", &os.PathError{Op: "open", Path: real, Err: ErrEscapes}
	}
	if r, err = filepath.Rel(base, real); err != nil {
		return "", "", "", err
	}
	r = filepath.ToSlash(r)
	if allow != nil && !allow(r) {
		return "", "", "", &os.PathError{Op: "open", Path: real, Err: ErrEscapes}
	}
	return base, real, r, nil
}

func openResolvedFallback(real, r string) (*os.File, string, error) {
	f, err := openChecked(real)
	return f, r, err
}

func within(dir, p string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// openChecked opens without blocking and admits only a regular file or a
// directory.
func openChecked(full string) (*os.File, error) {
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() && !fi.IsDir() {
		f.Close()
		return nil, &os.PathError{Op: "open", Path: full, Err: ErrNotRegular}
	}
	return f, nil
}
